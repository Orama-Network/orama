package netclass_test

import (
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/Orama-Network/orama/chain/netclass"
)

// inetAton reads host the way a C resolver's inet_aton does: one to four dot-separated numbers,
// each decimal, 0x hex or 0 octal, the last one filling the rest of the 32 bits. It reports false
// for anything else. A host that parses here can reach an address through the OS resolver.
func inetAton(host string) (uint32, bool) {
	parts := strings.Split(strings.TrimRight(host, "."), ".")
	if len(parts) == 0 || len(parts) > 4 {
		return 0, false
	}
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		if p == "" {
			return 0, false
		}
		base := 10
		switch {
		case len(p) > 1 && (p[:2] == "0x" || p[:2] == "0X"):
			base, p = 16, p[2:]
			if p == "" {
				p = "0"
			}
		case len(p) > 1 && p[0] == '0':
			base, p = 8, p[1:]
		}
		v, err := strconv.ParseUint(p, base, 32)
		if err != nil {
			return 0, false
		}
		vals[i] = v
	}
	var addr uint64
	for i := 0; i < len(vals)-1; i++ {
		if vals[i] > 255 {
			return 0, false
		}
		addr |= vals[i] << (8 * (3 - i))
	}
	last := vals[len(vals)-1]
	if last >= 1<<(8*(5-len(vals))) {
		return 0, false
	}
	return uint32(addr | last), true
}

func strictDottedQuad(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 || strconv.Itoa(n) != p {
			return false
		}
	}
	return true
}

// FuzzNetclassCheckHost: a host CheckHost accepts is one no resolver can turn into a non-public
// address. It has no character that a URL parser or resolver reads specially, a literal one is a
// public address (also by the standard library's own tests), and anything a C resolver reads as an
// IPv4 number other than a canonical dotted quad is refused. It never panics, and its verdict does
// not depend on letter case.
func FuzzNetclassCheckHost(f *testing.F) {
	for _, h := range []string{
		"example.com", "93.184.216.34", "2606:4700::1111", "127.0.0.1", "0x7f.1", "2130706433", "127.1.", "010.0.0.1",
		"localhost", "a.local", "::ffff:10.0.0.1", "fe80::1%eth0", "1.2.3", "intranet", "xn--bcher-kva.example", "a..b",
		"100.64.0.1", "64:ff9b::7f00:1", "example.com.", "EXAMPLE.COM", "0.0.0.0", "999.1.1.1", "1.2.3.4.5",
	} {
		f.Add(h)
	}
	f.Fuzz(func(t *testing.T, host string) {
		err := netclass.CheckHost(host)
		if (netclass.CheckHost(strings.ToUpper(host)) == nil) != (err == nil) && !strings.ContainsAny(host, "xX") {
			t.Fatalf("the verdict on %q depends on letter case", host)
		}
		if err != nil {
			return
		}
		if host == "" || strings.ContainsAny(host, "%/?#@\\ ") {
			t.Fatalf("accepted %q", host)
		}
		for i := 0; i < len(host); i++ {
			if host[i] < 0x21 || host[i] > 0x7e {
				t.Fatalf("accepted %q with a byte outside printable ASCII", host)
			}
		}
		if addr, err := netip.ParseAddr(host); err == nil {
			un := addr.Unmap()
			if !netclass.IsPublic(addr) || !un.IsGlobalUnicast() || un.IsPrivate() || un.IsLoopback() || un.IsLinkLocalUnicast() ||
				un.IsMulticast() || un.IsUnspecified() || un.IsInterfaceLocalMulticast() {
				t.Fatalf("accepted %q, which the standard library does not call a public unicast address", host)
			}
		}
		if v4, ok := inetAton(host); ok && !strictDottedQuad(host) {
			t.Fatalf("accepted %q, which a C resolver reads as the IPv4 number %#x", host, v4)
		}
		if v4, ok := inetAton(host); ok {
			addr := netip.AddrFrom4([4]byte{byte(v4 >> 24), byte(v4 >> 16), byte(v4 >> 8), byte(v4)})
			if !netclass.IsPublic(addr) {
				t.Fatalf("accepted %q, which a resolver reads as %s", host, addr)
			}
		}
		if lit, ok := netclass.Literal(host); ok && !netclass.IsPublic(lit) {
			t.Fatalf("accepted %q whose literal %s is not public", host, lit)
		}
	})
}

// FuzzNetclassIsPublic: IsPublic on any 4- or 16-byte address (with or without a zone) is false for
// every range the package lists as special and for a zoned address, is stable under unmapping an
// unzoned IPv4-mapped form, and agrees with the standard library on the classes it also knows.
func FuzzNetclassIsPublic(f *testing.F) {
	f.Add([]byte{8, 8, 8, 8}, "")
	f.Add([]byte{10, 0, 0, 1}, "")
	f.Add([]byte{0x20, 0x01, 0x48, 0x60, 0x48, 0x60, 0, 0, 0, 0, 0, 0, 0, 0, 0x88, 0x88}, "")
	f.Add([]byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, "eth0")
	f.Fuzz(func(t *testing.T, raw []byte, zone string) {
		var addr netip.Addr
		switch len(raw) {
		case 4:
			addr = netip.AddrFrom4([4]byte(raw))
		case 16:
			addr = netip.AddrFrom16([16]byte(raw))
			if zone != "" {
				addr = addr.WithZone(zone)
			}
		default:
			return
		}
		got := netclass.IsPublic(addr)
		if addr.Zone() == "" && got != netclass.IsPublic(addr.Unmap()) {
			t.Fatalf("%s: verdict changes when unmapped", addr)
		}
		un := addr.Unmap()
		if got {
			if addr.Zone() != "" {
				t.Fatalf("%s: a zoned address is public", addr)
			}
			for _, p := range netclass.SpecialPrefixes() {
				if p.Contains(un) {
					t.Fatalf("%s is public but inside the special range %s", addr, p)
				}
			}
			if !un.IsGlobalUnicast() || un.IsPrivate() || un.IsLoopback() || un.IsLinkLocalUnicast() || un.IsMulticast() || un.IsUnspecified() {
				t.Fatalf("%s is public but the standard library calls it a special address", addr)
			}
		}
	})
}
