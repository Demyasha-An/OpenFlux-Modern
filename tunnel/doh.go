package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"openflux/utils"
)

// TunnelDoHResolver resolves hostnames with DNS-over-HTTPS *through the local
// SOCKS5 port*, i.e. the queries leave the device via the exit node. That
// matters on censorship/poisoning networks: the phone's own resolver (and DoT,
// whose TCP/853 is often blocked) returns garbage or times out, while the exit
// node is a normal host in a normal country.
//
// Bootstrap is safe: OpenFlux's own traffic to Yandex is pinned with
// --resolve (or DoT) and never needs this resolver, so by the time a SOCKS5
// client asks for a name the tunnel is already up.
type TunnelDoHResolver struct {
	client   *http.Client
	cache    map[string]dohEntry
	cacheMu  sync.Mutex
	ttl      time.Duration
	negative time.Duration
}

type dohEntry struct {
	ips     []net.IP
	expires time.Time
}

// NewTunnelDoHResolver builds a resolver that tunnels DoH through socksAddr
// (e.g. "127.0.0.1:1080"). ttl caps the cache lifetime.
func NewTunnelDoHResolver(socksAddr string, ttl time.Duration) *TunnelDoHResolver {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	proxyURL, err := url.Parse("socks5://" + socksAddr)
	if err != nil {
		proxyURL = nil
	}
	return &TunnelDoHResolver{
		client: &http.Client{
			Timeout: 12 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyURL(proxyURL),
				TLSHandshakeTimeout:   8 * time.Second,
				IdleConnTimeout:       60 * time.Second,
				DisableKeepAlives:     false,
				ResponseHeaderTimeout: 10 * time.Second,
			},
		},
		cache:    make(map[string]dohEntry),
		ttl:      ttl,
		negative: 15 * time.Second,
	}
}

// dohEndpoint addresses a DoH resolver by IP so that reaching the resolver
// never needs a DNS answer itself (chicken-and-egg: the resolver would ask
// the tunnel, whose DialTCP asks the resolver). sni/host keep TLS and HTTP
// virtual-hosting correct while the URL stays an IP literal.
type dohEndpoint struct {
	url  string
	sni  string
	host string
}

var dohEndpoints = []dohEndpoint{
	{url: "https://8.8.8.8/resolve?name=%s&type=A", sni: "dns.google", host: "dns.google"},
	{url: "https://1.1.1.1/dns-query?name=%s&type=A", sni: "cloudflare-dns.com", host: "cloudflare-dns.com"},
	{url: "https://9.9.9.9:5053/dns-query?name=%s&type=A", sni: "dns.quad9.net", host: "dns.quad9.net"},
}

type dohAnswer struct {
	Status int `json:"Status"`
	Answer []struct {
		Type uint16 `json:"type"`
		Data string `json:"data"`
	} `json:"Answer"`
}

// resolving guards against reentrancy: while a DoH lookup is in flight, any
// nested DialTCP (including the one to reach the DoH endpoint) must not call
// back into this resolver -- it would deadlock.
var resolving atomic.Bool

// LookupIPv4 implements HostnameResolver.
func (r *TunnelDoHResolver) LookupIPv4(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return []net.IP{v4}, nil
		}
		return nil, fmt.Errorf("IPv6 literal not supported: %s", host)
	}
	if resolving.Load() {
		return net.DefaultResolver.LookupIP(ctx, "ip4", host)
	}
	resolving.Store(true)
	defer resolving.Store(false)

	r.cacheMu.Lock()
	if e, ok := r.cache[host]; ok && time.Now().Before(e.expires) {
		r.cacheMu.Unlock()
		if len(e.ips) == 0 {
			return nil, fmt.Errorf("no IPv4 address for %s", host)
		}
		return e.ips, nil
	}
	r.cacheMu.Unlock()

	var lastErr error
	for _, ep := range dohEndpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf(ep.url, url.QueryEscape(host)), nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("accept", "application/dns-json")
		req.Host = ep.host
		resp, err := r.clientDo(req, ep.sni)
		if err != nil {
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("doh %s: status %d", ep.host, resp.StatusCode)
			continue
		}
		var parsed dohAnswer
		if err := json.Unmarshal(body, &parsed); err != nil {
			lastErr = fmt.Errorf("doh %s: %w", ep.host, err)
			continue
		}
		var ips []net.IP
		for _, a := range parsed.Answer {
			if a.Type != 1 { // A
				continue
			}
			if ip := net.ParseIP(a.Data); ip != nil {
				if v4 := ip.To4(); v4 != nil {
					ips = append(ips, v4)
				}
			}
		}
		if len(ips) == 0 {
			lastErr = fmt.Errorf("no A record for %s", host)
			continue
		}
		utils.Debugf("[DNS/DoH] %s -> %s (via tunnel)", host, ips[0])
		r.put(host, ips, r.ttl)
		return ips, nil
	}
	r.put(host, nil, r.negative)
	return nil, lastErr
}

// clientDo performs the request against an IP-literal DoH endpoint while
// presenting the real hostname for TLS verification (SNI) and HTTP Host.
func (r *TunnelDoHResolver) clientDo(req *http.Request, sni string) (*http.Response, error) {
	tr := r.client.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{ServerName: sni, MinVersion: tls.VersionTLS12}
	c := &http.Client{Transport: tr, Timeout: r.client.Timeout}
	resp, err := c.Do(req)
	if err != nil {
		tr.CloseIdleConnections()
		return nil, err
	}
	resp.Body = &closeOnCloseBody{ReadCloser: resp.Body, tr: tr}
	return resp, nil
}

// closeOnCloseBody releases the per-endpoint transport when the body closes,
// so a dead resolver IP does not pin a pooled connection forever.
type closeOnCloseBody struct {
	io.ReadCloser
	tr *http.Transport
}

func (c *closeOnCloseBody) Close() error {
	err := c.ReadCloser.Close()
	c.tr.CloseIdleConnections()
	return err
}

func (r *TunnelDoHResolver) put(host string, ips []net.IP, ttl time.Duration) {
	r.cacheMu.Lock()
	r.cache[host] = dohEntry{ips: ips, expires: time.Now().Add(ttl)}
	// Cheap bound: the cache only ever holds hostnames seen by this process.
	if len(r.cache) > 2048 {
		for k, e := range r.cache {
			if time.Now().After(e.expires) {
				delete(r.cache, k)
			}
		}
	}
	r.cacheMu.Unlock()
}
