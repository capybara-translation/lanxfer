// Package netguard decides whether a remote address may talk to lanxfer.
// It is separate from the server package so that discovery can use it
// without importing the HTTP server.
package netguard

import "net"

// IsAllowedRemote reports whether http.Request.RemoteAddr ("host:port")
// belongs to a LAN-equivalent address. Only private (RFC 1918 / ULA),
// loopback, and link-local addresses are allowed; everything else
// (global addresses, unparsable input) is rejected.
// lanxfer has no authentication, so this acts as a safety net in case the
// server is accidentally exposed to the internet.
func IsAllowedRemote(remoteAddr string) bool {
	ip := parseRemote(remoteAddr)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// IsLoopback reports whether remoteAddr ("host:port") is a loopback
// address. Unparsable input is not loopback.
func IsLoopback(remoteAddr string) bool {
	ip := parseRemote(remoteAddr)
	return ip != nil && ip.IsLoopback()
}

// parseRemote extracts the IP from a "host:port" string, or returns nil.
func parseRemote(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}
