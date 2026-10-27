package netclass_test

import (
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Orama-Network/orama/chain/netclass"
)

func TestCheckHost_refusesEveryNonPublicOrObfuscatedForm(t *testing.T) {
	refused := []string{
		// Plain special ranges.
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254",
		"0.0.0.0", "0.1.2.3", "255.255.255.255",
		"100.64.0.1", "100.127.255.255", // carrier-grade NAT
		"198.18.0.1", "198.19.255.255", // benchmarking
		"192.0.0.8",                                // IETF protocol assignments
		"192.0.2.1", "198.51.100.7", "203.0.113.9", // documentation
		"192.88.99.1",                                            // 6to4 relay
		"224.0.0.1", "239.255.255.250", "240.0.0.1", "250.1.1.1", // multicast and reserved
		// IPv6.
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "fec0::1",
		"64:ff9b::7f00:1", "64:ff9b::a00:1", // NAT64 to 127.0.0.1 and 10.0.0.1
		"2001:db8::1", "2001::1", "2002:7f00:1::1", "100::1", "3fff::1", "5f00::1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:7f00:1", // IPv4-mapped
		"::7f00:1", // IPv4-compatible
		// Obfuscated IPv4: the OS resolver reads all of these as an address.
		"2130706433", "0x7f000001", "0x7f.1", "0x7f.0.0.1", "127.1", "127.0.1",
		"010.0.0.1", "01.2.3.4", "0177.0.0.1", "1.2.3.04", "1.2.3", "4294967295",
		"10.0.0.1.", // a trailing dot does not change what it resolves to
		// A zone id or anything else that is not a bare host.
		"fe80::1%eth0", "fe80::1%25eth0", "2606:4700::1%eth0",
		"10.0.0.1/x", "1.2.3.4/24", "1.2.3.4?x", "1.2.3.4#x", "user@1.2.3.4", "1.2.3.4 ", "",
		// Local names.
		"localhost", "LOCALHOST", "a.localhost", "printer.local",
		"localhost.localdomain", "host.localdomain", "db.internal", "internal", "router.lan", "lan", "nas.home.arpa", "home.arpa",
		"LOCALHOST.", "localhost..", "db.internal.", "a.b.local.",
		// Every trailing dot is dropped before classification, so extra dots hide nothing.
		"127.1..", "10.0.0.1..", "2130706433...", "0x7f.1..",
		// A single label with no dot: a resolver may complete it with a search domain into a private
		// host, and it is not a public name.
		"intranet", "db", "nas", "INTRANET", "intranet.", "xn--bcher-kva",
		// An empty label is not a name.
		".", "..", "example..com", ".example.com", "example.com..",
		// Non-ASCII hosts: a resolver may normalize them into something the checks never saw.
		"exämple.com", "１２７.０.０.１", "127.0.0.１", "ｅxample.com", "example.com\u200b", "tab\there.example", "ctl\x01.example",
		// A malformed IPv6 literal is not a name either.
		"2606:4700::zz", "1:2:3",
	}
	for _, host := range refused {
		require.Errorf(t, netclass.CheckHost(host), "%q must be refused", host)
	}
}

func TestCheckHost_acceptsPublicNamesAndAddresses(t *testing.T) {
	accepted := []string{
		"example.com", "node-1.example.org", "a.b.c.example.net", "xn--bcher-kva.example",
		"1.1.1.1", "8.8.8.8", "45.33.32.156", "100.63.255.255", "100.128.0.1", "172.15.255.255", "172.32.0.1",
		"198.17.255.255", "198.20.0.1", "223.255.255.254",
		"2606:4700:4700::1111", "2a00:1450:4001:81b::200e", "2001:4860:4860::8888",
		"2001:200::1", // outside 2001::/23
		"1e3.example.com", "example.com.", "abc.def",
		"a2b3c4.onion", "example.0x7f.example",
	}
	for _, host := range accepted {
		require.NoErrorf(t, netclass.CheckHost(host), "%q must be accepted", host)
	}
}

func TestLiteral_isStrictAndCanonical(t *testing.T) {
	addr, ok := netclass.Literal("8.8.8.8")
	require.True(t, ok)
	require.Equal(t, netip.MustParseAddr("8.8.8.8"), addr)

	addr, ok = netclass.Literal("::ffff:8.8.8.8")
	require.True(t, ok)
	require.Equal(t, netip.MustParseAddr("8.8.8.8"), addr, "an IPv4-mapped address is its IPv4 form")

	for _, host := range []string{"", "example.com", "2130706433", "0x7f.1", "127.1", "010.0.0.1", "01.2.3.4",
		"fe80::1%eth0", "1.2.3.4/24", "[::1]", "8.8.8.8 "} {
		_, ok := netclass.Literal(host)
		require.Falsef(t, ok, "%q is not a strict literal", host)
	}
}

func TestIsPublic_zeroAndZonedAddressesAreNot(t *testing.T) {
	require.False(t, netclass.IsPublic(netip.Addr{}))
	require.False(t, netclass.IsPublic(netip.MustParseAddr("fe80::1%eth0")))
	require.False(t, netclass.IsPublic(netip.MustParseAddr("2606:4700::1%eth0")))
	require.True(t, netclass.IsPublic(netip.MustParseAddr("2606:4700::1")))
}

// core cannot import this module, so core/pkg/netguard keeps its own list of reserved ranges. Every
// range this classifier refuses must be in it (it may add more: AS112, AMT and the IPv6 loopback and
// unspecified addresses), or a tenant could reach through core what the chain refuses.
func TestNetclassRangesAreCoveredByCoreNetguard(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "repo", "core", "pkg", "netguard", "netguard.go"))
	require.NoError(t, err)
	core := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\t"([0-9a-f:.]+/[0-9]+)",$`).FindAllStringSubmatch(string(src), -1) {
		core[netip.MustParsePrefix(m[1]).String()] = true
	}
	require.NotEmpty(t, core, "no ranges found in core/pkg/netguard/netguard.go")
	for _, p := range netclass.SpecialPrefixes() {
		require.Truef(t, core[p.String()], "netclass refuses %s but core/pkg/netguard does not list it", p)
	}
}
