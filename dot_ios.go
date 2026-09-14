//go:build ios

package main

import (
	"net"
)

// EnableSecureDNS re-applies the DoT resolver. The ios build already
// installs it in init (export_ios.go); this shim only exists so main.go
// (--mobile) compiles under -tags ios by reusing dialSecureDNS/dotServers
// defined there instead of duplicating them.
func EnableSecureDNS() {
	net.DefaultResolver = &net.Resolver{
		PreferGo:     true,
		StrictErrors: false,
		Dial:         dialSecureDNS,
	}
}
