package gateway

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/constants"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/secrets"
)

// A gateway's own state.
//
// Every gateway on a host runs as orama-namespace-gateway@<ns>, as the orama
// user under ProtectSystem=strict, and <oramaDir>/secrets is read-only to it.
// Its signing keys and its encryption-root cache used to be written there
// anyway — every write failed, the serverless init that builds the auth
// service failed with it, and the gateway served /health while every
// /v1/auth/* route was a 404. Every gateway on the host also resolved the SAME
// key files, so "each gateway has its own key" held for none of them.
//
// Each gateway writes only to its StateDir now,
// <oramaDir>/data/namespaces/<ns>/gateway (0700): jwt-signing-key.pem,
// jwt-eddsa-key.pem and the encryption-root cache files sit directly in it.
// A namespace gateway also keeps wazero's compiled function code in its
// compilationCacheDirName subdirectory (bug-963).

// compilationCacheDirName is the StateDir subdirectory of a namespace
// gateway's on-disk WASM compilation cache (serverless.Config
// CompilationCacheDir). It is private to the gateway because wazero maps what
// it reads there as executable code.
const compilationCacheDirName = "wasm-compiled"

// ensureStateDir creates the gateway's state directory, 0700 to the orama user.
func ensureStateDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("gateway has no state_dir; it has nowhere to keep its signing keys")
	}
	if err := os.MkdirAll(dir, constants.GatewayStateDirMode); err != nil {
		return fmt.Errorf("create the gateway state directory %s (it must be under the unit's ReadWritePaths): %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, and this one holds
	// private keys.
	if err := os.Chmod(dir, constants.GatewayStateDirMode); err != nil {
		return fmt.Errorf("restrict the gateway state directory %s: %w", dir, err)
	}
	return nil
}

// signingKeyNamespace is what a gateway's signing key is bound to.
//
// A namespace gateway's key signs only for its own tenant. The index gateway's
// is bound to nothing: it is the control plane, it mints the tokens the CLI
// signs in with for every namespace, and a compromise of it is not a tenant
// boundary problem. Its client_namespace is "index"; an empty one or the lobby
// namespace is the cluster gateway too.
func signingKeyNamespace(clientNamespace string) string {
	ns := strings.TrimSpace(clientNamespace)
	if ns == "" || ns == auth.LobbyNamespace || ns == constants.IndexNamespace {
		return ""
	}
	return ns
}

// servesNamedNamespace reports whether a gateway with this client namespace
// serves one tenant namespace. The cluster gateway serves none: its client
// namespace was "default" and is now "index", and checks written against
// "default" treated the renamed cluster gateway as a tenant's.
func servesNamedNamespace(clientNamespace string) bool {
	ns := strings.TrimSpace(clientNamespace)
	return ns != "" && ns != auth.LobbyNamespace && ns != constants.IndexNamespace
}

// bootstrapEncryptionRoot loads the encryption root every stored ciphertext is
// keyed from.
//
// The registry is the source of truth and the file in this gateway's state
// directory is its cache; the node's secrets/ copy seeds a gateway whose cache
// is empty (see secrets.LoadOrMaterialize). A failure is returned: it used to
// fall back to a copy of the cluster secret, which on a rotated cluster is the
// wrong key, and every secret this gateway then wrote was unreadable to the
// rest of the cluster.
func bootstrapEncryptionRoot(cfg *Config, deps *Dependencies) (*secrets.Holder, error) {
	if cfg == nil {
		return nil, fmt.Errorf("encryption root: no gateway config")
	}
	seedDir := ""
	if cfg.DataDir != "" {
		seedDir = secrets.SecretsDir(cfg.DataDir)
	}
	var store secrets.Store
	if deps != nil {
		if deps.GlobalORMClient != nil {
			store = deps.GlobalORMClient
		} else if deps.ORMClient != nil {
			store = deps.ORMClient
		}
	}
	r, err := secrets.LoadOrMaterialize(context.Background(), store, cfg.StateDir, seedDir, cfg.ClusterSecret)
	if err != nil {
		return nil, fmt.Errorf("load the encryption root (cache %s, seed %s): %w", cfg.StateDir, seedDir, err)
	}
	return secrets.NewHolder(r), nil
}
