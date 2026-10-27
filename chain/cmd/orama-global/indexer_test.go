package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRequireLocalOnly_acceptsOnlyLoopbackAndTheNamespaceAddress(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:31015", "[::1]:31015", "127.0.0.2:1", "198.18.0.2:31015"} {
		require.NoError(t, requireLocalOnly(ok), ok)
	}
	for _, bad := range []string{"0.0.0.0:31015", ":31015", "localhost:31015", "10.0.0.1:31015", "[::]:31015", "127.0.0.1",
		"198.18.0.1:31015", "198.18.0.3:31015", "203.0.113.5:31015"} {
		require.Error(t, requireLocalOnly(bad), bad)
	}
}

func TestNamespaceAddr_isTheCoreConstant(t *testing.T) {
	data, err := os.ReadFile("../../testdata/repo/core/pkg/constants/global.go")
	require.NoError(t, err)
	require.Contains(t, string(data), `GlobalNetnsAddr = "`+namespaceAddr+`"`)
}

func TestIndexerCmd_defaultsAndRegistration(t *testing.T) {
	cmd := indexerCmd()
	require.Equal(t, defaultIndexerListen, cmd.Flag("listen").DefValue)
	require.Equal(t, "0", cmd.Flag("start-height").DefValue)
	require.Equal(t, defaultRPC, cmd.Flag("rpc").DefValue)
	found, _, err := rootCmd().Find([]string{"indexer"})
	require.NoError(t, err)
	require.Equal(t, "indexer", found.Name())
}

func TestRunIndexer_refusesBadFlagsBeforeTouchingAnything(t *testing.T) {
	home := t.TempDir()
	err := runIndexer(context.Background(), defaultRPC, home, "0.0.0.0:31015", 1, time.Second)
	require.ErrorContains(t, err, "must be a loopback IP")
	err = runIndexer(context.Background(), defaultRPC, home, defaultIndexerListen, 1, 0)
	require.ErrorContains(t, err, "--interval")
}
