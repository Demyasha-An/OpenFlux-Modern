package socks5

import (
	"fmt"
	"io"
	"net"
	"sync"

	"openflux/utils"
)

type Dialer interface {
	DialTCP(address string) (net.Conn, error)
}

type SOCKS5Server struct {
	listenAddr string
	dialer     Dialer
	auth       *socksAuth

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

// socksAuth holds optional RFC 1929 username/password credentials.
type socksAuth struct {
	user string
	pass string
}

func NewSOCKS5Server(addr string, dialer Dialer) *SOCKS5Server {
	return &SOCKS5Server{listenAddr: addr, dialer: dialer}
}

// SetAuth enables username/password authentication (RFC 1929). Empty values
// disable it. Use it whenever the listener is not loopback-only, e.g. when a
// phone reaches the SOCKS5 port of a laptop acting as the tunnel client.
func (s *SOCKS5Server) SetAuth(user, pass string) {
	if user == "" {
		s.auth = nil
		return
	}
	s.auth = &socksAuth{user: user, pass: pass}
}

// Bind reserves the listen address so callers can detect "address already in
// use" synchronously, before serving. Safe to call once; Start binds lazily if
// it wasn't called.
func (s *SOCKS5Server) Bind() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return net.ErrClosed
	}
	if s.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	s.listener = listener
	return nil
}

func (s *SOCKS5Server) Start() error {
	if err := s.Bind(); err != nil {
		return err
	}

	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	defer listener.Close()

	utils.Debugf("[SOCKS5] Listening on %s", s.listenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				utils.Debugf("[SOCKS5] Listener closed, stopping")
				return net.ErrClosed
			}
			utils.Debugf("[SOCKS5] Accept error: %v", err)
			continue
		}
		go s.handleConnection(conn)
	}
}

// Close stops the server, unblocking Start's accept loop.
func (s *SOCKS5Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *SOCKS5Server) handleConnection(clientConn net.Conn) {
	// A malformed request must never crash the host process; contain any
	// panic to this connection.
	defer func() {
		if r := recover(); r != nil {
			utils.Debugf("[SOCKS5] Recovered from panic in handler: %v", r)
		}
	}()
	defer clientConn.Close()

	// Greeting: VER(1) NMETHODS(1) METHODS(NMETHODS). TCP may deliver the
	// request in fragments, so read exactly what the header announces instead
	// of trusting a single Read.
	var hdr [2]byte
	if _, err := io.ReadFull(clientConn, hdr[:]); err != nil {
		return
	}
	if hdr[0] != 0x05 || hdr[1] == 0 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(clientConn, methods); err != nil {
		return
	}
	// No client auth: always reply "no acceptable methods" (0xFF) unless the
	// client offers NO-AUTH (0x00), which we accept.
	acceptsNoAuth := false
	acceptsUserPass := false
	for _, m := range methods {
		switch m {
		case 0x00:
			acceptsNoAuth = true
		case 0x02:
			acceptsUserPass = true
		}
	}
	switch {
	case s.auth != nil && acceptsUserPass:
		// RFC 1929: VER(1) ULEN(1) UNAME PLEN(1) PASSWD
		if err := s.checkUserPass(clientConn); err != nil {
			return
		}
	case acceptsNoAuth && s.auth == nil:
		// open proxy on loopback: fine
	default:
		clientConn.Write([]byte{0x05, 0xFF})
		return
	}
	if _, err := clientConn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// Request: VER(1) CMD(1) RSV(1) ATYP(1) ADDR VAR PORT(2).
	var req [4]byte
	if _, err := io.ReadFull(clientConn, req[:]); err != nil {
		return
	}
	if req[0] != 0x05 || req[1] != 0x01 { // CONNECT only
		socksReply(clientConn, 0x07) // command not supported
		return
	}
	var targetAddr string
	switch req[3] {
	case 0x01: // IPv4
		var addr [6]byte // 4 addr + 2 port
		if _, err := io.ReadFull(clientConn, addr[:]); err != nil {
			return
		}
		targetAddr = fmt.Sprintf("%d.%d.%d.%d:%d",
			addr[0], addr[1], addr[2], addr[3],
			uint16(addr[4])<<8|uint16(addr[5]))
	case 0x03: // domain
		var l [1]byte
		if _, err := io.ReadFull(clientConn, l[:]); err != nil {
			return
		}
		domainLen := int(l[0])
		if domainLen == 0 {
			utils.Debugf("[SOCKS5] Bad domain request (len=0)")
			socksReply(clientConn, 0x01)
			return
		}
		rest := make([]byte, domainLen+2) // domain + port
		if _, err := io.ReadFull(clientConn, rest); err != nil {
			return
		}
		targetAddr = fmt.Sprintf("%s:%d",
			string(rest[:domainLen]),
			uint16(rest[domainLen])<<8|uint16(rest[domainLen+1]))
	case 0x04: // IPv6 — the tunnel is IPv4-only; refuse cleanly so browsers
		// fall back to the A record instead of stalling on a dead family.
		var drain [18]byte
		io.ReadFull(clientConn, drain[:])
		utils.Debugf("[SOCKS5] IPv6 CONNECT refused (tunnel is IPv4-only): %s", net.IP(drain[:16]))
		socksReply(clientConn, 0x08) // address type not supported
		return
	default:
		socksReply(clientConn, 0x08) // address type not supported
		return
	}

	utils.Debugf("[SOCKS5] CONNECT %s", targetAddr)

	targetConn, err := s.dialer.DialTCP(targetAddr)
	if err != nil {
		utils.Debugf("[SOCKS5] Dial failed: %v", err)
		socksReply(clientConn, 0x04) // host unreachable
		return
	}
	defer targetConn.Close()

	socksReply(clientConn, 0x00) // success

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		defer targetConn.Close()
		io.Copy(targetConn, clientConn)
	}()

	go func() {
		defer wg.Done()
		defer clientConn.Close()
		io.Copy(clientConn, targetConn)
	}()

	wg.Wait()
}

// checkUserPass performs the RFC 1929 username/password exchange.
func (s *SOCKS5Server) checkUserPass(conn net.Conn) error {
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return err
	}
	if head[0] != 0x01 { // auth version
		return fmt.Errorf("bad auth version %d", head[0])
	}
	user := make([]byte, head[1])
	if _, err := io.ReadFull(conn, user); err != nil {
		return err
	}
	var plen [1]byte
	if _, err := io.ReadFull(conn, plen[:]); err != nil {
		return err
	}
	pass := make([]byte, plen[0])
	if _, err := io.ReadFull(conn, pass); err != nil {
		return err
	}
	if string(user) != s.auth.user || string(pass) != s.auth.pass {
		utils.Debugf("[SOCKS5] auth failed for user %q", string(user))
		conn.Write([]byte{0x01, 0x01}) // status: failure
		return fmt.Errorf("socks5 auth failed")
	}
	conn.Write([]byte{0x01, 0x00}) // status: success
	return nil
}

// socksReply writes a minimal SOCKS5 reply with the given status code.
func socksReply(conn net.Conn, code byte) {
	conn.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}
