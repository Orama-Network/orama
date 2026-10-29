package gateway

import (
	"net/http"
	"strings"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	serverlesshandlers "github.com/Orama-Network/orama/core/pkg/gateway/handlers/serverless"
	"github.com/Orama-Network/orama/core/pkg/httputil"
)

// The cluster gateway does not run tenant functions (bugboard #427).
//
// It runs with client_namespace "default", so its database is the cluster
// registry, and the registry's namespace-placed tables — namespaces,
// deployments, deployment_domains, functions, triggers — hold every tenant's
// rows side by side. A tenant function run there reached them all through its
// database host calls, and the SQL guard, a denylist of platform tables,
// cannot tell one tenant's row from another's.
//
// So on the cluster gateway every serverless request for a namespace other
// than "default" is proxied to that namespace's own gateway, through the same
// path an ns-<namespace> host takes: the credential is validated against the
// registry here, a credential of another namespace is refused, and the
// identity crosses the hop in signed internal-auth headers. Which namespace:
//
//   - the one the request names: POST /v1/invoke/<namespace>/<function>, or
//     ?namespace= on the other routes;
//   - else the credential's;
//   - else nothing, and the request is refused. An anonymous caller may invoke
//     a public function, but has to say whose.
//
// The database host functions refuse a foreign namespace on their own as well
// (hostfunctions.checkDatabaseAccess), so a tenant function row that is
// already in the registry and fired by a trigger gets no database either.

// directInvokePrefix is the invoke route that names its namespace in the path.
const directInvokePrefix = "/v1/invoke/"

// clusterServerlessRoutingMiddleware sends tenant serverless traffic on the
// cluster gateway to the tenant's own gateway. On a namespace gateway it does
// nothing: that gateway's functions are its namespace's.
func (g *Gateway) clusterServerlessRoutingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || !g.servesCoreRegistry() || !isServerlessRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		named := namespaceNamedByServerlessRequest(r)
		switch named {
		case auth.LobbyNamespace:
			next.ServeHTTP(w, r)
		case "":
			g.routeServerlessByCredential(w, r, next)
		default:
			g.handleNamespaceGatewayRequest(w, r, named)
		}
	})
}

// routeServerlessByCredential routes a serverless request that names no
// namespace by the namespace its credential belongs to.
//
// A request that stays here is authenticated again by authMiddleware; only
// the "default" namespace's own traffic takes that path.
func (g *Gateway) routeServerlessByCredential(w http.ResponseWriter, r *http.Request, next http.Handler) {
	markPhase(r, "routing")
	a := g.namespaceProxyAuthFor(r)
	markPhase(r, "auth")
	switch {
	case a.errMsg != "":
		g.refuseProxyCredential(w, r, a.errMsg, a.claims)
	case a.namespace == auth.LobbyNamespace:
		next.ServeHTTP(w, r)
	case a.namespace != "":
		g.proxyToNamespaceGateway(w, r, a.namespace, a)
	case g.policyFor(r).Access.Anonymous():
		httputil.WriteRPCError(w, http.StatusBadRequest, httputil.ErrCodeValidationFailed,
			"this gateway runs only the "+auth.LobbyNamespace+" namespace's functions and the request "+
				"names no namespace; invoke POST /v1/invoke/<namespace>/<function>, or call the "+
				"namespace's own gateway, "+g.namespaceGatewayURLPattern())
	default:
		unauthorized(w, CodeAuthMissing, "no credential was presented", nil)
	}
}

// isServerlessRoute reports whether path is one the serverless handlers serve,
// read from the same list they register, so the two cannot disagree.
func isServerlessRoute(path string) bool {
	for _, pattern := range serverlesshandlers.Routes() {
		if strings.HasSuffix(pattern, "/") {
			if strings.HasPrefix(path, pattern) {
				return true
			}
		} else if path == pattern {
			return true
		}
	}
	return false
}

// namespaceNamedByServerlessRequest is the namespace a serverless request
// names for itself, or "" when it names none. The direct-invoke route names it
// in the path and nowhere else; every other route may name it with
// ?namespace=.
func namespaceNamedByServerlessRequest(r *http.Request) string {
	if rest, ok := strings.CutPrefix(r.URL.Path, directInvokePrefix); ok {
		ns, _, _ := strings.Cut(rest, "/")
		return strings.TrimSpace(ns)
	}
	return strings.TrimSpace(r.URL.Query().Get("namespace"))
}

// namespaceGatewayURLPattern is where a namespace's gateway lives, for a
// refusal to point at.
func (g *Gateway) namespaceGatewayURLPattern() string {
	base := "<base domain>"
	if g.cfg != nil && strings.TrimSpace(g.cfg.BaseDomain) != "" {
		base = strings.TrimSpace(g.cfg.BaseDomain)
	}
	return "https://ns-<namespace>." + base
}
