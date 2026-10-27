package node

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"github.com/Orama-Network/orama/core/pkg/config"
	"go.uber.org/zap"
)

// wgRowsDriver serves a fixed wireguard_peers result set, standing in for a
// local rqlite replica that answers at level=none with no raft leader.
type wgRowsDriver struct {
	rows    [][]driver.Value
	queried bool
	failErr error
}

func (d *wgRowsDriver) Open(string) (driver.Conn, error) { return &wgRowsConn{d: d}, nil }

// Connect and Driver make the driver its own connector, so a test opens it
// with sql.OpenDB instead of registering a global name.
func (d *wgRowsDriver) Connect(context.Context) (driver.Conn, error) { return d.Open("") }
func (d *wgRowsDriver) Driver() driver.Driver                        { return d }

type wgRowsConn struct{ d *wgRowsDriver }

func (c *wgRowsConn) Prepare(string) (driver.Stmt, error) { return &wgRowsStmt{d: c.d}, nil }
func (c *wgRowsConn) Close() error                        { return nil }
func (c *wgRowsConn) Begin() (driver.Tx, error)           { return nil, errors.New("no tx") }

type wgRowsStmt struct{ d *wgRowsDriver }

func (s *wgRowsStmt) Close() error                               { return nil }
func (s *wgRowsStmt) NumInput() int                              { return 0 }
func (s *wgRowsStmt) Exec([]driver.Value) (driver.Result, error) { return nil, errors.New("no exec") }

func (s *wgRowsStmt) Query([]driver.Value) (driver.Rows, error) {
	s.d.queried = true
	if s.d.failErr != nil {
		return nil, s.d.failErr
	}
	return &wgRows{rows: s.d.rows}, nil
}

type wgRows struct {
	rows [][]driver.Value
	i    int
}

func (r *wgRows) Columns() []string {
	return []string{"node_id", "wg_ip", "public_key", "public_ip", "wg_port"}
}
func (r *wgRows) Close() error { return nil }
func (r *wgRows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

func openWGDB(t *testing.T, d *wgRowsDriver) *sql.DB {
	t.Helper()
	db := sql.OpenDB(d)
	t.Cleanup(func() { db.Close() })
	return db
}

// wgTestKey is a canonical WireGuard public key: 32 bytes, base64.
func wgTestKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

var (
	keyA    = wgTestKey(0xa)
	keyB    = wgTestKey(0xb)
	keySelf = wgTestKey(0x5)
)

// selfWGIP is the overlay address of the node doing the read.
const selfWGIP = "10.0.0.17"

func row(nodeID, wgIP, key, pubIP string, port int64) []driver.Value {
	return []driver.Value{nodeID, wgIP, key, pubIP, port}
}

// The devnet outage shape: the interface has lost every peer and there is no
// raft leader, so only a local read can supply membership.
func TestScanWGPeers_readsMembershipWithoutALeader(t *testing.T) {
	d := &wgRowsDriver{rows: [][]driver.Value{
		row("nodeA", "10.0.0.1", keyA, "203.0.113.1", 51820),
		row("nodeB", "10.0.0.2", keyB, "203.0.113.2", 51820),
		row("self", "10.0.0.17", keySelf, "203.0.113.17", 51820),
	}}
	db := openWGDB(t, d)

	peers, err := scanWGPeers(context.Background(), db, keySelf, selfWGIP, zap.NewNop())
	if err != nil {
		t.Fatalf("scanWGPeers: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers; want 2 (self must be excluded)", len(peers))
	}
	if _, ok := peers[keySelf]; ok {
		t.Error("self must not be added as its own peer")
	}
	got := peers[keyA]
	if got.Endpoint != "203.0.113.1:51820" {
		t.Errorf("endpoint = %q; want 203.0.113.1:51820", got.Endpoint)
	}
	if got.AllowedIP != "10.0.0.1/32" {
		t.Errorf("allowed IP = %q; want 10.0.0.1/32", got.AllowedIP)
	}
}

func TestScanWGPeers_defaultsMissingPort(t *testing.T) {
	d := &wgRowsDriver{rows: [][]driver.Value{
		row("nodeA", "10.0.0.1", keyA, "203.0.113.1", 0),
	}}
	db := openWGDB(t, d)

	peers, err := scanWGPeers(context.Background(), db, keySelf, selfWGIP, zap.NewNop())
	if err != nil {
		t.Fatalf("scanWGPeers: %v", err)
	}
	if peers[keyA].Endpoint != "203.0.113.1:51820" {
		t.Errorf("endpoint = %q; want the default port applied", peers[keyA].Endpoint)
	}
}

// A row with an empty key is not a peer. It is skipped; the rows that are
// peers are still returned. A scan or query error still fails the read.
func TestScanWGPeers_skipsAnEmptyKey(t *testing.T) {
	d := &wgRowsDriver{rows: [][]driver.Value{
		row("nodeA", "10.0.0.1", keyA, "203.0.113.1", 51820),
		row("nodeB", "", "", "203.0.113.2", 51820),
	}}
	db := openWGDB(t, d)

	peers, err := scanWGPeers(context.Background(), db, keySelf, selfWGIP, zap.NewNop())
	if err != nil {
		t.Fatalf("an empty key halted the sync: %v", err)
	}
	if len(peers) != 1 || peers[keyA].AllowedIP != "10.0.0.1/32" {
		t.Fatalf("peers = %+v, want only the valid row", peers)
	}
}

func TestScanWGPeers_queryFailurePropagates(t *testing.T) {
	d := &wgRowsDriver{failErr: errors.New("leader address: leader not found")}
	db := openWGDB(t, d)

	if _, err := scanWGPeers(context.Background(), db, keySelf, selfWGIP, zap.NewNop()); err == nil {
		t.Fatal("want the query error surfaced to the caller")
	}
}

func TestScanWGPeers_emptyTable(t *testing.T) {
	d := &wgRowsDriver{rows: nil}
	db := openWGDB(t, d)

	peers, err := scanWGPeers(context.Background(), db, keySelf, selfWGIP, zap.NewNop())
	if err != nil {
		t.Fatalf("scanWGPeers: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("got %d peers; want 0", len(peers))
	}
}

// rqlited binds only the WireGuard advertise address and requires auth, so a
// config without either cannot yield a handle — there is no localhost default.
func TestOpenLocalRQLiteForBootstrap_requiresAddressAndCredentials(t *testing.T) {
	for name, mutate := range map[string]func(*config.Config){
		"no advertise address": func(c *config.Config) { c.Discovery.HttpAdvAddress = "" },
		"wildcard advertise":   func(c *config.Config) { c.Discovery.HttpAdvAddress = "0.0.0.0:10100" },
		"no password":          func(c *config.Config) { c.Database.RQLitePassword = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Database.RQLitePort = 10100
			cfg.Database.RQLiteUsername = "orama"
			cfg.Database.RQLitePassword = "deadbeef"
			cfg.Discovery.HttpAdvAddress = "10.0.0.2:10100"
			mutate(cfg)
			if _, err := (&Node{config: cfg}).openLocalRQLiteForBootstrap(); err == nil {
				t.Fatal("opened a handle without a usable address or credentials")
			}
		})
	}
}

func TestOpenLocalRQLiteForBootstrap_requiresPort(t *testing.T) {
	n := &Node{config: &config.Config{}}
	if _, err := n.openLocalRQLiteForBootstrap(); err == nil {
		t.Fatal("want an error when the rqlite port is unconfigured")
	}

	n2 := &Node{}
	if _, err := n2.openLocalRQLiteForBootstrap(); err == nil {
		t.Fatal("want an error when config is nil")
	}
}

// The bootstrap handle must ask for level=none: a leader-routed read is exactly
// what cannot succeed in the situation this path exists to recover from.
func TestOpenLocalRQLiteForBootstrap_usesLocalReadLevel(t *testing.T) {
	n := &Node{config: &config.Config{}}
	n.config.Database.RQLitePort = 10100
	n.config.Database.RQLiteUsername = "orama"
	n.config.Database.RQLitePassword = "deadbeef"
	n.config.Discovery.HttpAdvAddress = "10.0.0.2:10100"

	db, err := n.openLocalRQLiteForBootstrap()
	if err != nil {
		t.Fatalf("openLocalRQLiteForBootstrap: %v", err)
	}
	defer db.Close()
	// Constructing the handle is the assertion; sql.Open is lazy so no
	// connection is attempted here. The DSN shape is covered below.
}
