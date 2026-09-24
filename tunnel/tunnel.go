package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/waiter"

	"openflux/transport"
	"openflux/utils"
)

// ExitMode выбирает, как выходная нода общается с интернетом.
type ExitMode int

const (
	ExitModeL3 ExitMode = iota // L3: SNAT/DNAT без gVisor (Linux)
	ExitModeL4                 // L4: gVisor TCP-терминация + net.Dial (работает везде)
)

func (m ExitMode) String() string {
	switch m {
	case ExitModeL3:
		return "l3"
	default:
		return "l4"
	}
}

// ParseExitMode разбирает строку из флага --mode.
func ParseExitMode(s string) (ExitMode, error) {
	switch s {
	case "", "l4", "proxy":
		// "proxy" is a deprecated alias kept for one release.
		return ExitModeL4, nil
	case "l3":
		return ExitModeL3, nil
	default:
		return ExitModeL4, fmt.Errorf("unknown mode %q (want l3|l4)", s)
	}
}

type TCPTunnel struct {
	gvisorStack *stack.Stack
	tunnelEP    *TunnelLinkEndpoint
	transport   transport.Transport
	isExitNode  bool
	exitMode    ExitMode
	startTime   time.Time
	packetCount atomic.Uint64
}

// TCP buffer size range for gvisor stacks.
var (
	TCPBufMin     = 4 * 1024 * 1024
	TCPBufDefault = 16 * 1024 * 1024
	TCPBufMax     = 64 * 1024 * 1024
)

// UseMobileBuffers shrinks the gVisor TCP buffer range for phones and 4G.
// 16MB sockets on a lossy radio link mean bufferbloat: huge in-flight,
// long retransmits, OOM on small devices. Call before NewTCPTunnelMode.
func UseMobileBuffers() {
	TCPBufMin = 256 * 1024
	TCPBufDefault = 1024 * 1024
	TCPBufMax = 4 * 1024 * 1024
}

// SetTCPBuffers applies the configured TCP send/receive buffer ranges to s.
func SetTCPBuffers(s *stack.Stack) {
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: TCPBufMin, Default: TCPBufDefault, Max: TCPBufMax}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv); err != nil {
		utils.Debugf("[TUNNEL] set recv buffer: %v", err)
	}
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: TCPBufMin, Default: TCPBufDefault, Max: TCPBufMax}
	if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd); err != nil {
		utils.Debugf("[TUNNEL] set send buffer: %v", err)
	}
}

func NewTCPTunnel(trans transport.Transport, isExitNode bool) *TCPTunnel {
	return NewTCPTunnelMode(trans, isExitNode, ExitModeL4)
}

func NewTCPTunnelMode(trans transport.Transport, isExitNode bool, mode ExitMode) *TCPTunnel {
	t := &TCPTunnel{
		transport:  trans,
		isExitNode: isExitNode,
		exitMode:   mode,
		startTime:  time.Now(),
	}

	utils.Debugf("[TUNNEL] Net stack init...")
	t.gvisorStack = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})

	SetTCPBuffers(t.gvisorStack)

	tunnelEP := NewTunnelLinkEndpoint()
	tunnelEP.onOutgoingPacket = func(data []byte) {
		if err := trans.Send(data); err != nil {
			utils.Debugf("[TUNNEL] trans.Send error: %v", err)
		}
	}
	t.tunnelEP = tunnelEP

	tunnelNIC := tcpip.NICID(1)
	if err := t.gvisorStack.CreateNIC(tunnelNIC, tunnelEP); err != nil {
		utils.Debugf("[TUNNEL] CreateNIC tunnel error: %v", err)
	}

	if isExitNode {
		t.setupExitNodeProxy(tunnelNIC)
	} else {
		t.setupClient(tunnelNIC)
	}

	trans.Receive(func(data []byte) {
		tunnelEP.InjectInbound(data)
	})

	utils.SafeGo("tunnel.printStats", t.printStats)
	return t
}

// ---- exit node: proxy ----

func (t *TCPTunnel) setupExitNodeProxy(tunnelNIC tcpip.NICID) {
	utils.Debugf("[TUNNEL] EXIT NODE - proxy mode (no raw sockets)")

	t.gvisorStack.SetPromiscuousMode(tunnelNIC, true)
	t.gvisorStack.SetSpoofing(tunnelNIC, true)
	t.gvisorStack.AddRoute(tcpip.Route{
		Destination: header.IPv4EmptySubnet,
		NIC:         tunnelNIC,
	})

	fwd := tcp.NewForwarder(t.gvisorStack, 0, 8192, t.handleExitTCP)
	t.gvisorStack.SetTransportProtocolHandler(tcp.ProtocolNumber, fwd.HandlePacket)
}

func (t *TCPTunnel) handleExitTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dest := fmt.Sprintf("%s:%d", id.LocalAddress.String(), id.LocalPort)

	var wq waiter.Queue
	ep, tErr := r.CreateEndpoint(&wq)
	if tErr != nil {
		utils.Debugf("[EXIT] CreateEndpoint %s: %v", dest, tErr)
		r.Complete(true)
		return
	}
	r.Complete(false)
	local := gonet.NewTCPConn(&wq, ep)

	utils.SafeGo("exit.flow", func() {
		remote, err := net.DialTimeout("tcp", dest, 10*time.Second)
		if err != nil {
			utils.Debugf("[EXIT] dial %s failed: %v", dest, err)
			local.Close()
			return
		}
		if tc, ok := remote.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetReadBuffer(16 * 1024 * 1024)
			_ = tc.SetWriteBuffer(16 * 1024 * 1024)
		}
		utils.Debugf("[EXIT] %s connected", dest)

		go func() {
			buf := make([]byte, 256*1024)
			io.CopyBuffer(remote, local, buf)
			remote.Close()
			local.Close()
		}()
		buf := make([]byte, 256*1024)
		io.CopyBuffer(local, remote, buf)
		local.Close()
		remote.Close()
	})
}

// ---- client ----

func (t *TCPTunnel) setupClient(tunnelNIC tcpip.NICID) {
	clientAddr := tcpip.AddrFrom4([4]byte{10, 10, 10, 2})
	t.gvisorStack.AddProtocolAddress(tunnelNIC, tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   clientAddr,
			PrefixLen: 24,
		},
	}, stack.AddressProperties{})

	t.gvisorStack.AddRoute(tcpip.Route{
		Destination: header.IPv4EmptySubnet,
		NIC:         tunnelNIC,
	})
}

// HostnameResolver resolves a hostname to IPv4 addresses. Returning an error
// makes DialTCP fail fast, so implementations should fall back internally
// rather than propagate transient upstream failures.
type HostnameResolver interface {
	LookupIPv4(ctx context.Context, host string) ([]net.IP, error)
}

var (
	resolverMu sync.RWMutex
	resolver   HostnameResolver
)

// SetHostnameResolver installs a custom resolver for SOCKS5 target hostnames
// (nil restores the system resolver). Must be called before serving.
func SetHostnameResolver(r HostnameResolver) {
	resolverMu.Lock()
	resolver = r
	resolverMu.Unlock()
}

func resolveHostIPv4(host string) ([]net.IP, error) {
	// Literal address: nothing to resolve.
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []net.IP{v4}, nil
		}
		return nil, fmt.Errorf("IPv6 literal not supported: %s", host)
	}
	resolverMu.RLock()
	r := resolver
	resolverMu.RUnlock()
	if r != nil {
		return r.LookupIPv4(context.Background(), host)
	}
	return net.DefaultResolver.LookupIP(context.Background(), "ip4", host)
}

func (t *TCPTunnel) DialTCP(address string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("bad address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("bad port in %q: %w", address, err)
	}

	// The tunnel is IPv4-only (10.10.10.2/24 on an ipv4 link endpoint), so
	// resolve with an explicit "ip4" query instead of net.ResolveTCPAddr,
	// which returns whatever the resolver lists first. On dual-stack hosts
	// (github.com and friends) an AAAA-first answer used to fail the whole
	// CONNECT with "IPv6 not supported" even though A records existed --
	// pages that resolve remotely through SOCKS never loaded in browsers.
	//
	// The resolver is pluggable (SetHostnameResolver): on a phone the system
	// resolver is poisoned, and a SOCKS5 client (INCY) hands us bare
	// hostnames, so the default is not an option there -- see
	// NewTunnelDoHResolver, which resolves DoH through this very SOCKS5 port.
	ips, err := resolveHostIPv4(host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IPv4 address for %s", host)
	}
	ip := ips[0].To4()
	if ip == nil {
		return nil, fmt.Errorf("no IPv4 address for %s", host)
	}
	utils.Debugf("[TUNNEL] DialTCP %s -> %s:%d", address, ip.String(), port)

	nic := tcpip.NICID(1)
	if t.isExitNode && false {
		nic = tcpip.NICID(2)
	}

	conn, err := gonet.DialTCP(t.gvisorStack, tcpip.FullAddress{
		NIC:  nic,
		Addr: tcpip.AddrFrom4([4]byte{ip[0], ip[1], ip[2], ip[3]}),
		Port: uint16(port),
	}, ipv4.ProtocolNumber)

	return conn, err
}

func (t *TCPTunnel) ListenTCP(port uint16) (net.Listener, error) {
	return gonet.ListenTCP(t.gvisorStack, tcpip.FullAddress{
		NIC:  1,
		Port: port,
	}, ipv4.ProtocolNumber)
}

func (t *TCPTunnel) printStats() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		stats := t.gvisorStack.Stats()
		utils.Debugf("[STATS] uptime=%v mode=%s packets=%d connected=%d established=%d retrans=%d",
			time.Since(t.startTime).Round(time.Second),
			t.exitMode.String(),
			t.packetCount.Load(),
			stats.TCP.CurrentConnected.Value(),
			stats.TCP.CurrentEstablished.Value(),
			stats.TCP.Retransmits.Value(),
		)
	}
}

// ---- local IP helpers (only needed for raw mode) ----

// localIPOverride, when set, is the address the exit node uses as its egress
// IP (both for source rewriting and the return-packet filter).
var localIPOverride string

// SetLocalIP overrides the auto-detected egress IP for the exit node.
func SetLocalIP(ip string) { localIPOverride = ip }

func getLocalIP() string {
	if localIPOverride != "" {
		return localIPOverride
	}
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "192.168.1.100"
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}
