package netregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func networkFS(t *testing.T, name string, m Manifest, root []byte) fstest.MapFS {
	t.Helper()
	return fstest.MapFS{
		"embedded/" + name + "/" + ManifestFile:    {Data: marshalManifest(t, m)},
		"embedded/" + name + "/" + ReleaseRootFile: {Data: root},
		"embedded/README.md":                       {Data: []byte("not a network")},
	}
}

func TestLoadRegistry_loadsNetworksAndIgnoresFiles(t *testing.T) {
	r, err := LoadFS(networkFS(t, "teststage", validManifest(), testRoot), "embedded")
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.Get("teststage")
	if err != nil {
		t.Fatal(err)
	}
	if !n.Builtin || n.Source != PublishedBaseURL+"teststage/manifest.json" {
		t.Errorf("built-in network = %+v", n)
	}
	if got := r.Names(); len(got) != 1 || got[0] != "teststage" {
		t.Errorf("Names = %v", got)
	}
}

func TestLoadRegistry_refusesBrokenNetworks(t *testing.T) {
	good := validManifest()
	for name, fsys := range map[string]fstest.MapFS{
		"directory and manifest name differ": networkFS(t, "other", good, testRoot),
		"root is not the pinned one":         networkFS(t, "teststage", good, []byte(`{"tampered":true}`)),
		"manifest is invalid":                networkFS(t, "teststage", Manifest{Name: "teststage"}, testRoot),
		"root is missing": {
			"embedded/teststage/" + ManifestFile: {Data: marshalManifest(t, good)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFS(fsys, "embedded"); err == nil {
				t.Fatal("loadRegistry accepted a broken network")
			}
		})
	}
}

func TestLoadRegistry_aPinnedTorNetworkIsLoadedAndVerified(t *testing.T) {
	file := testTorNetwork(t)
	fsys := networkFS(t, "teststage", withTorNetwork(file), testRoot)
	fsys["embedded/teststage/"+TorNetworkFile] = &fstest.MapFile{Data: file}
	r, err := LoadFS(fsys, "embedded")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := r.Get("teststage")
	if !bytes.Equal(n.TorNetwork, file) {
		t.Errorf("the network carries %d bytes of Tor network, want the pinned file", len(n.TorNetwork))
	}
	plain, err := LoadFS(networkFS(t, "teststage", validManifest(), testRoot), "embedded")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := plain.Get("teststage"); n.TorNetwork != nil {
		t.Errorf("a network that pins none carries %d bytes", len(n.TorNetwork))
	}
}

func TestLoadRegistry_aBrokenTorNetworkRefusesTheNetwork(t *testing.T) {
	file := testTorNetwork(t)
	pinned := withTorNetwork(file)
	missing := networkFS(t, "teststage", pinned, testRoot)
	tampered := networkFS(t, "teststage", pinned, testRoot)
	tampered["embedded/teststage/"+TorNetworkFile] = &fstest.MapFile{Data: append(file[:len(file):len(file)], ' ')}
	for name, fsys := range map[string]fstest.MapFS{"the pinned file is missing": missing, "the file is not the pinned one": tampered} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFS(fsys, "embedded"); err == nil {
				t.Fatal("a network whose Tor network file fails its pin was loaded")
			}
		})
	}
}

func TestRegistryGet_unknownNameListsTheKnownOnes(t *testing.T) {
	r, _ := LoadFS(networkFS(t, "teststage", validManifest(), testRoot), "embedded")
	_, err := r.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(nope) = %v, want ErrNotFound", err)
	}
	if want := "teststage"; !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Errorf("error %q does not list %q", err, want)
	}
	empty := &Registry{nets: map[string]*Network{}}
	if _, err := empty.Get("x"); err == nil {
		t.Error("an empty registry found a network")
	}
}

func TestRegistryMerge_refusesAShadowedName(t *testing.T) {
	a, _ := LoadFS(networkFS(t, "teststage", validManifest(), testRoot), "embedded")
	b, _ := LoadFS(networkFS(t, "teststage", validManifest(), testRoot), "embedded")
	if _, err := a.Merge(b); err == nil {
		t.Error("Merge let a custom network shadow a built-in one")
	}
	empty := &Registry{nets: map[string]*Network{}}
	merged, err := a.Merge(empty)
	if err != nil || len(merged.Names()) != 1 {
		t.Errorf("Merge with an empty registry = %v, %v", merged, err)
	}
}

func TestNetworkGenesisURL_besideTheManifest(t *testing.T) {
	n := &Network{Source: "https://example.org/nets/dev/manifest.json"}
	got, err := n.GenesisURL()
	if err != nil || got != "https://example.org/nets/dev/genesis.json" {
		t.Errorf("GenesisURL = %q, %v", got, err)
	}
}

// The embedded registry is a build copy of networks/ at the repository root.
// Go cannot embed outside the module, so a copy that drifted would put a
// different network in the binary than the website serves.
func TestEmbedded_matchesPublishedNetworks(t *testing.T) {
	if _, err := Embedded(); err != nil {
		t.Fatalf("the embedded registry does not load: %v", err)
	}
	published := filepath.Join("..", "..", "testdata", "repo", "networks")
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("the repository's networks/ directory is missing: %v", err)
	}
	for _, side := range []struct{ from, to string }{{published, embeddedDir}, {embeddedDir, published}} {
		assertNetworkFilesMatch(t, side.from, side.to)
	}
}

func assertNetworkFilesMatch(t *testing.T, from, to string) {
	t.Helper()
	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, file := range publishedNetworkFiles(t, filepath.Join(from, e.Name())) {
			want, err := os.ReadFile(filepath.Join(from, e.Name(), file))
			if err != nil {
				t.Errorf("%s/%s: %v", from, e.Name(), err)
				continue
			}
			got, err := os.ReadFile(filepath.Join(to, e.Name(), file))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("%s/%s differs from %s: run `make -C core sync-networks`", to, e.Name(), from)
			}
		}
	}
}

// publishedNetworkFiles are the files a network directory carries into the binary: the manifest,
// the release root and, when the manifest pins one, the Tor network file.
func publishedNetworkFiles(t *testing.T, dir string) []string {
	t.Helper()
	files := []string{ManifestFile, ReleaseRootFile}
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.TorNetworkSHA256 != "" {
		files = append(files, TorNetworkFile)
	}
	return files
}
