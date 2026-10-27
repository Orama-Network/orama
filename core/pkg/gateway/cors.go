package gateway

import (
	"context"
	"net/http"

	"github.com/Orama-Network/orama/core/pkg/gateway/corsorigins"
	"github.com/Orama-Network/orama/core/pkg/rqlite"
	"go.uber.org/zap"
)

const (
	// corsAllowHeaders are the request headers a browser page may send: the
	// credential headers, and X-Orama-Revoke-Key, which the SDK's
	// revokeFetchCap sends on DELETE /v1/storage/fetch-caps/{id}.
	corsAllowHeaders = "Content-Type, Authorization, X-API-Key, X-Orama-Revoke-Key"
	// corsExposeHeaders are the response headers beyond the CORS-safelisted
	// ones a page may read, which a cross-origin page could not otherwise see:
	// Retry-After, which the SDK waits by on a 429 or 503, and Date, the
	// gateway's clock, which the SDK stamps its device proofs on.
	corsExposeHeaders = "Retry-After, Date"
)

// namespaceAllowsOrigin reports whether the namespace r's host names
// (ns-<name>.<base>) lists origin among its allowed origins. A preflight
// carries no credential, so the host is the only thing that says which
// namespace the page is calling.
func (g *Gateway) namespaceAllowsOrigin(r *http.Request, origin string) bool {
	if g.corsOrigins == nil {
		return false
	}
	ns := corsorigins.NamespaceFromHost(r.Host, g.cfg.BaseDomain)
	return ns != "" && g.corsOrigins.Allows(ns, origin)
}

// initCORSOrigins wires the namespaces' allowed origins on the index gateway,
// which answers every ns-<name> host (a namespace gateway's CORS headers are
// replaced by the index gateway's, copyProxiedHeaders). The lists live in the
// cluster registry.
func (g *Gateway) initCORSOrigins(registry rqlite.Client, logger *zap.Logger) {
	if registry == nil || isNamespaceGateway(g.cfg) {
		return
	}
	store := corsorigins.NewStore(registry)
	g.corsOrigins = corsorigins.NewCache(store, logger)
	g.corsOrigins.Start(context.Background())
	g.corsOriginsHandler = corsorigins.NewHandler(store, g.corsOrigins, logger)
}
