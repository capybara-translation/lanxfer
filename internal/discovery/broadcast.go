package discovery

import (
	"net"
	"net/netip"
)

// broadcastAddr returns the directed broadcast address of the private IPv4
// network that ipnet's host address belongs to. It reports false for
// anything that has no useful broadcast address: IPv6, non-private ranges,
// and /31 or /32 prefixes.
func broadcastAddr(ipnet *net.IPNet) (netip.Addr, bool) {
	ip4 := ipnet.IP.To4()
	if ip4 == nil || !ip4.IsPrivate() {
		return netip.Addr{}, false
	}
	mask := ipnet.Mask
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if ones, bits := mask.Size(); bits != 32 || ones >= 31 {
		return netip.Addr{}, false
	}
	var bc [4]byte
	for i := range bc {
		bc[i] = ip4[i] | ^mask[i] // set every host bit to 1
	}
	return netip.AddrFrom4(bc), true
}

// localBroadcastAddrs lists the directed broadcast address of every network
// this machine is attached to through an interface that is up and
// broadcast-capable. 255.255.255.255 is deliberately not used: with several
// interfaces the OS sends it out of only one of them.
//
// FlagBroadcast is absent on loopback and point-to-point (VPN) interfaces,
// so checking it excludes both.
func localBroadcastAddrs() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	seen := make(map[netip.Addr]bool)
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if bc, ok := broadcastAddr(ipnet); ok && !seen[bc] {
				seen[bc] = true
				out = append(out, bc)
			}
		}
	}
	return out, nil
}
