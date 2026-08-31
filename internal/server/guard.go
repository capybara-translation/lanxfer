package server

import "net"

// IsAllowedRemote reports whether http.Request.RemoteAddr ("host:port")
// belongs to a LAN-equivalent address. Only private (RFC 1918 / ULA),
// loopback, and link-local addresses are allowed; everything else
// (global addresses, unparsable input) is rejected.
// lanxfer has no authentication, so this acts as a safety net in case the
// server is accidentally exposed to the internet.
func IsAllowedRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
