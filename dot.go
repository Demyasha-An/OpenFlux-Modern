//go:build !ios

package main

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	"universal-bypass-tool/utils"
)

// Secure DNS-over-TLS resolver for censored / poisoned mobile networks.
// Same server list as the iOS bridge (export_ios.go): the client's local
// DNS may resolve censored hosts to reserved addresses (observed:
// ifconfig.me -> 240.0.1.72), which then poisons DialTCP for the exit node.
// Enabled by --mobile; desktop default stays on the system resolver.

type dotServer struct {
	addr string
	sni  string
}

var dotServers = []dotServer{
	{"77.88.8.8:853", "common.dot.dns.yandex.net"}, // Yandex, reachable in-region
	{"8.8.8.8:853", "dns.google"},
	{"1.1.1.1:853", "cloudflare-dns.com"},
}

func dialSecureDNS(ctx context.Context, _, _ string) (net.Conn, error) {
	var lastErr error
	for _, s := range dotServers {
		d := tls.Dialer{
			NetDialer: &net.Dialer{Timeout: 6 * time.Second},
			Config:    &tls.Config{ServerName: s.sni, MinVersion: tls.VersionTLS12},
		}
		conn, err := d.DialContext(ctx, "tcp", s.addr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		utils.Debugf("[DNS] DoT %s failed: %v", s.addr, err)
	}
	return nil, lastErr
}

// EnableSecureDNS routes all Go resolver traffic over DNS-over-TLS.
func EnableSecureDNS() {
	net.DefaultResolver = &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial:         dialSecureDNS,
	}
}
