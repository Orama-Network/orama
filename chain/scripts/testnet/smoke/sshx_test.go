package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellQuote_neutralisesShellSyntax(t *testing.T) {
	for in, want := range map[string]string{
		"plain":       `'plain'`,
		"a b":         `'a b'`,
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": `'$(rm -rf /)'`,
		"a;b`c`":      "'a;b`c`'",
		"":            `''`,
	} {
		require.Equal(t, want, shellQuote(in))
	}
}

func TestAgentCommand_pipesTheKeyOnTheNodeAndNeverPrintsIt(t *testing.T) {
	cmd := agentCommand()
	require.True(t, strings.HasPrefix(cmd, "sudo sh -c '"))
	require.Contains(t, cmd, "keys")
	require.Contains(t, cmd, "--unarmored-hex")
	require.Contains(t, cmd, "| "+nodeHelper+" agent", "the export feeds the agent on the same host")
	require.Contains(t, cmd, agentSock+":0", "root's own socket, for `orama` run on the node")
	require.Contains(t, cmd, agentFwdSock)
}

func TestNamespaceAddr_isTheCoreConstant(t *testing.T) {
	// core/pkg/constants.GlobalNetnsAddr; chain cannot import core, so the literal is checked here.
	data, err := os.ReadFile("../../../testdata/repo/core/pkg/constants/global.go")
	require.NoError(t, err)
	require.Contains(t, string(data), `GlobalNetnsAddr = "`+namespaceAddr+`"`)
}

func TestAbbreviate(t *testing.T) {
	require.Equal(t, "short", abbreviate("short"))
	require.Len(t, abbreviate(strings.Repeat("x", 200)), 83)
}

// Readiness is the agent answering through the socket, not the socket file existing: a stale file
// from an earlier run exists before the new agent listens.
func TestAgentProbeCommand_asksTheAgentThroughTheForwardedSocket(t *testing.T) {
	got := agentProbeCommand()
	for _, want := range []string{"curl", "--unix-socket", agentFwdSock, "http://agent" + agentAccountPath} {
		if !strings.Contains(got, want) {
			t.Errorf("probe %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "test -S") {
		t.Errorf("probe %q only checks the file", got)
	}
}
