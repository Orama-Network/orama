package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A code the SDK switches on and the docs do not mention is a code nobody can
// act on. The list in docs/whitepaper/technical-reference/vol1/14-authorization.md is the one clients read, and it drifts the
// moment a code is added without a line there — which is how the auth docs got
// into the state this epic had to correct.
func TestAuthCodes_areAllInTheDocs(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(linkedRepo, "docs/whitepaper/technical-reference/vol1/14-authorization.md"))
	if err != nil {
		t.Fatalf("read docs/whitepaper/technical-reference/vol1/14-authorization.md: %v", err)
	}
	page := string(doc)

	for _, file := range []string{
		"core/pkg/gateway/auth_errors.go",
		"core/pkg/gateway/handlers/auth/challenge_errors.go",
		"core/pkg/gateway/handlers/auth/errors.go",
		"core/pkg/gateway/handlers/auth/signin_errors.go",
		"core/pkg/gateway/handlers/auth/device_errors.go",
		"core/pkg/gateway/handlers/attest/errors.go",
		"core/pkg/gateway/handlers/storage/fetch_cap_errors.go",
		"core/pkg/gateway/relay_errors.go",
	} {
		for name, code := range wireCodesIn(t, filepath.Join(repoRootFor(t), file)) {
			if !strings.Contains(page, "`"+code+"`") {
				t.Errorf("%s (%s, %s) is a code a client can receive and docs/whitepaper/technical-reference/vol1/14-authorization.md does not "+
					"list it", code, name, file)
			}
		}
	}
}

// wireCodesIn returns the string constants in a file whose value is an
// UPPER_SNAKE code — the form every wire code takes.
func wireCodesIn(t *testing.T, path string) map[string]string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, s := range decl.Specs {
			spec, ok := s.(*ast.ValueSpec)
			if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
				continue
			}
			lit, ok := spec.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil || !isWireCode(value) {
				continue
			}
			out[spec.Names[0].Name] = value
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("no codes found in %s; this test is not reading it", path)
	}
	return out
}

// isWireCode reports whether a string is an UPPER_SNAKE wire code.
func isWireCode(s string) bool {
	if s == "" || !strings.ContainsRune(s, '_') {
		return false
	}
	for _, c := range s {
		if (c < 'A' || c > 'Z') && c != '_' {
			return false
		}
	}
	return true
}
