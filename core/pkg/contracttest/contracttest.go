// Package contracttest loads the request/response fixtures in the repository's
// contracts/ directory.
//
// Those fixtures are the shared contract between the gateway and the TypeScript
// SDK. Nothing used to check that a body the SDK sends is a body a handler
// parses: the unit tests on either side were written against that side alone,
// and the only thing exercising both was an end-to-end suite that needed a live
// cluster. A field renamed on one side and not the other therefore reached
// production.
//
// Each fixture is read twice. A Go test decodes `request` into the handler's own
// struct with unknown fields rejected, so the gateway must understand every
// field and the SDK must send no field the gateway ignores. A TypeScript test
// drives the SDK method and asserts the body it sends equals the same JSON, and
// feeds `response` back to assert what the method returns.
//
// The fixtures live above the Go module. They are read through the module's
// testdata/repo/contracts link, a path inside the module, so the go test cache
// treats every fixture a test read as an input of that test: editing a fixture
// alone reruns the tests that read it.
package contracttest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fixture is one route's contract.
type Fixture struct {
	// Name is the fixture's path under contracts/, e.g. "db/query".
	Name string `json:"-"`
	// Route is the gateway path, e.g. "/v1/rqlite/query".
	Route string `json:"route"`
	// Method is the HTTP method.
	Method string `json:"method"`
	// SDK is the client method that produces this request, for the failure message.
	SDK string `json:"sdk"`
	// GoStruct names the handler struct the request decodes into. Empty when the
	// handler has no named request type.
	GoStruct string `json:"goStruct"`
	// Call is how the TypeScript test drives the SDK. Null when the request
	// cannot be produced by a single call (a token refresh, say).
	Call *Call `json:"call"`
	// Request is the body the SDK sends.
	Request json.RawMessage `json:"request"`
	// Response is the body the gateway answers with.
	Response json.RawMessage `json:"response"`
	// Returns is what the SDK method resolves to, given that response.
	Returns json.RawMessage `json:"returns"`
}

// Call names an SDK method and its arguments.
type Call struct {
	Module string            `json:"module"`
	Method string            `json:"method"`
	Args   []json.RawMessage `json:"args"`
}

// linkedContracts is where a module links in the repository's contracts/
// directory, relative to the module root.
var linkedContracts = filepath.Join("testdata", "repo", "contracts")

// Dir returns the repository's contracts directory as the core module links
// it in: it walks up from startingAt to the module root holding
// testdata/repo/contracts.
func Dir(startingAt string) (string, error) {
	dir, err := filepath.Abs(startingAt)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, linkedContracts)
		if isDir(candidate) {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no module with %s above %s", linkedContracts, startingAt)
		}
		dir = parent
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Load reads every fixture, sorted by name.
func Load(startingAt string) ([]Fixture, error) {
	dir, err := Dir(startingAt)
	if err != nil {
		return nil, err
	}

	// dir is a link; os.DirFS follows it where filepath.Walk would stop at it.
	contracts := os.DirFS(dir)
	var fixtures []Fixture
	err = fs.WalkDir(contracts, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil
		}
		body, err := fs.ReadFile(contracts, name)
		if err != nil {
			return err
		}
		var fixture Fixture
		if err := json.Unmarshal(body, &fixture); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		fixture.Name = strings.TrimSuffix(name, ".json")
		fixtures = append(fixtures, fixture)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Name < fixtures[j].Name })
	return fixtures, nil
}

// For returns the fixtures whose name starts with the given prefix, so a
// package's test can pick out the routes it owns.
func For(startingAt, prefix string) ([]Fixture, error) {
	all, err := Load(startingAt)
	if err != nil {
		return nil, err
	}
	var out []Fixture
	for _, fixture := range all {
		if strings.HasPrefix(fixture.Name, prefix) {
			out = append(out, fixture)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no fixtures under %q", prefix)
	}
	return out, nil
}

// DecodeStrict decodes a fixture's request into target, rejecting any field the
// target does not declare.
//
// That is the assertion worth making in both directions: an unknown field means
// the SDK sends something the gateway silently drops.
func (f Fixture) DecodeStrict(target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(f.Request)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s (%s): %w", f.Name, f.SDK, err)
	}
	return nil
}
