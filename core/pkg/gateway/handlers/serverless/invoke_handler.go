package serverless

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/Orama-Network/orama/core/pkg/gateway/clientkey"
	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// classifyInvokeError maps a function-invoke error to the canonical
// (HTTP status, RPC code, retryable) triple. Shared by the HTTP and WS invoke
// paths so both transports report identical, machine-readable error semantics.
// A cold-WASM fetch timeout is a transient infra failure (retryable); a genuine
// error raised inside the function is FUNCTION_EXECUTION_FAILED (not retryable).
// A function stopped at its timeout is 504 FUNCTION_TIMEOUT, never
// RATE_LIMITED: a rate limit refuses the request before the function runs, and
// a timeout means it ran, so a client that read one as the other would repeat
// a call whose effects (a payment broadcast) already happened.
func classifyInvokeError(err error) (int, httputil.RPCErrorCode, bool) {
	switch {
	case errors.Is(err, serverless.ErrWASMFetchTimeout):
		return http.StatusServiceUnavailable, httputil.ErrCodeFunctionUnavailable, true
	case errors.Is(err, serverless.ErrInvokeLimitsUnavailable):
		return http.StatusServiceUnavailable, httputil.ErrCodeServiceUnavailable, true
	case serverless.IsTimeout(err):
		return http.StatusGatewayTimeout, httputil.ErrCodeFunctionTimeout, false
	case errors.Is(err, serverless.ErrVersionSuperseded):
		return http.StatusNotFound, httputil.ErrCodeFunctionVersionSuperseded, false
	case serverless.IsNotFound(err):
		return http.StatusNotFound, httputil.ErrCodeNotFound, false
	case serverless.IsResourceExhausted(err):
		return http.StatusTooManyRequests, httputil.ErrCodeRateLimited, true
	case errors.Is(err, serverless.ErrFunctionNotDirectlyInvocable):
		// Before IsUnauthorized, which it also answers: no credential the
		// caller could present changes it, so it is never a 401.
		return http.StatusForbidden, httputil.ErrCodeFunctionNotDirectlyInvocable, false
	case serverless.IsUnauthorized(err):
		return http.StatusUnauthorized, httputil.ErrCodeUnauthorized, false
	case serverless.IsDevicePendingApproval(err):
		return http.StatusForbidden, httputil.ErrCodeDevicePendingApproval, false
	default:
		return http.StatusInternalServerError, httputil.ErrCodeFunctionExecution, false
	}
}

// extractRemoteIP returns the source IP of the request: the peer, or the last X-Forwarded-For entry
// when the peer is the local reverse proxy or a gateway on the mesh (clientkey.Attribute). X-Real-IP
// and the first, caller-written, entry are never used, and a private peer off the mesh is the client.
func extractRemoteIP(r *http.Request) string { return clientkey.Attribute(r) }

// InvokeFunction handles POST /v1/functions/{name}/invoke
// Invokes a function with the provided input.
func (h *ServerlessHandlers) InvokeFunction(w http.ResponseWriter, r *http.Request, nameWithNS string, version int) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse namespace and name
	var namespace, name string
	if idx := strings.Index(nameWithNS, "/"); idx > 0 {
		namespace = nameWithNS[:idx]
		name = nameWithNS[idx+1:]
	} else {
		name = nameWithNS
		namespace = h.invokeNamespace(r)
	}

	if namespace == "" {
		writeError(w, http.StatusBadRequest,
			"name the namespace whose function to invoke: POST /v1/invoke/<namespace>/<function>")
		return
	}

	// A grant may be narrowed to one function — `fn:name=checkout`. A
	// credential with no selector is unaffected: whether it may invoke at all
	// is the invoker's decision (public, private, the invoke grant), and this
	// only ever takes access away.
	if err := auth.AuthorizeResource(r.Context(), auth.Resource{
		Domain: auth.SelectorFn,
		Name:   name,
	}); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	// Read input body
	input, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB max
	if err != nil {
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	// Get caller wallet from JWT
	callerWallet := h.getWalletFromRequest(r)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	req := &serverless.InvokeRequest{
		Namespace:         namespace,
		FunctionName:      name,
		Version:           version,
		Input:             input,
		TriggerType:       serverless.TriggerTypeHTTP,
		CallerWallet:      callerWallet,
		CallerIsAdmin:     h.getCallerIsAdminFromRequest(r),
		CallerHasInvoke:   h.getCallerHasInvokeFromRequest(r),
		CallerIP:          extractRemoteIP(r),
		CallerKey:         h.getCallerKeyFromRequest(r),
		CallerClaims:      h.getCallerClaimsFromRequest(r),
		CallerJWTSubject:  h.getJWTSubjectFromRequest(r),
		CallerDeviceID:    h.getDeviceIDFromRequest(r),
		CallerDeviceState: h.getDeviceStateFromRequest(r),
		CallerAttestation: h.getAttestationFromRequest(r),
	}

	// From here the response is the function's: its output, its own status, or
	// the failure to run it. The proxy in front does not hold a 502, 503 or 504
	// that carries this against the gateway.
	httputil.MarkTenantOrigin(w.Header())
	resp, err := h.invoker.Invoke(ctx, req)
	if err != nil {
		// Bug #212: every error path here emits the canonical RPC
		// envelope. error.message is always populated (falls back to
		// err.Error() then to a default per code).

		// Rate-limit errors carry a retry hint we surface as both the
		// HTTP Retry-After header and the envelope field.
		var rle *serverless.RateLimitedError
		if errors.As(err, &rle) {
			opts := []httputil.RPCErrorOption{}
			if rle.RetryAfter > 0 {
				opts = append(opts, httputil.WithRetryAfter(rle.RetryAfter.Seconds()))
			}
			if resp != nil && resp.RequestID != "" {
				opts = append(opts, httputil.WithRequestID(resp.RequestID))
			}
			writeRPCError(w, http.StatusTooManyRequests,
				httputil.ErrCodeRateLimited, err.Error(), opts...)
			return
		}

		// Map domain-typed errors to (status, RPC code). WriteRPCError seeds the
		// retryable bit from defaultRetryableFor(code), so a cold-WASM
		// FUNCTION_UNAVAILABLE comes back retryable automatically.
		statusCode, errCode, _ := classifyInvokeError(err)
		// A caller the gateway knows and the invoker refused has been
		// authenticated: signing in again would change nothing, so it is told
		// it may not (403), and 401 stays for a caller with no identity.
		// getWalletFromRequest answers a verified wallet, else the namespace
		// the credential belongs to (an API key, a workload), and "" only when
		// the request carries no credential at all.
		identified := callerWallet != ""
		if errCode == httputil.ErrCodeUnauthorized && identified {
			statusCode, errCode = http.StatusForbidden, httputil.ErrCodeForbidden
		}

		// Pick the most informative message: function-side resp.Error
		// (if set) is more actionable than the wrapping err.Error().
		msg := err.Error()
		if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		opts := []httputil.RPCErrorOption{}
		if resp != nil && resp.RequestID != "" {
			opts = append(opts, httputil.WithRequestID(resp.RequestID))
		}
		writeRPCError(w, statusCode, errCode, msg, opts...)
		return
	}

	writeInvocationOutput(w, resp)
}

// writeInvocationOutput writes a successful invocation: the function's raw
// HTTP response when it set one, its output as it is when that is JSON, and
// otherwise the output wrapped with the request id, status and duration.
func writeInvocationOutput(w http.ResponseWriter, resp *serverless.InvokeResponse) {
	w.Header().Set("X-Request-ID", resp.RequestID)
	w.Header().Set("X-Duration-Ms", strconv.FormatInt(resp.DurationMS, 10))

	// Raw-HTTP-response mode (bugboard #835): when a function deployed with
	// raw_http_response actually set a response via set_http_response, replay
	// it verbatim (status + headers + body) and skip the sniff/wrap path. If
	// the function set nothing, RawHTTP is nil and we fall through to the
	// normal behavior unchanged.
	if resp.RawHTTP != nil {
		for k, v := range resp.RawHTTP.Headers {
			// A tenant function must not overwrite gateway-owned trace/auth
			// headers or framing-control (hop-by-hop) headers via its raw
			// response — that would let it forge request IDs, leak/spoof
			// internal-auth headers, or corrupt response framing.
			if isReservedResponseHeader(k) {
				continue
			}
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.RawHTTP.Status)
		w.Write(resp.RawHTTP.Body)
		return
	}

	// Output that is JSON is the function's answer and is returned as it is.
	// The test used to be its first byte, '{' or '[', so any other JSON value —
	// null, a number, a string, true — came back wrapped in the envelope: a
	// function that returned null (a refused database call) read as an object.
	if json.Valid(resp.Output) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(resp.Output)
	} else {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"request_id":  resp.RequestID,
			"output":      string(resp.Output),
			"status":      resp.Status,
			"duration_ms": resp.DurationMS,
		})
	}
}

// HandleInvoke handles POST /v1/invoke/{namespace}/{name}[@version]
// Direct invocation endpoint with namespace in path.
func (h *ServerlessHandlers) HandleInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse path: /v1/invoke/{namespace}/{name}[@version]
	path := strings.TrimPrefix(r.URL.Path, "/v1/invoke/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) < 2 {
		http.Error(w, "Path must be /v1/invoke/{namespace}/{name}", http.StatusBadRequest)
		return
	}

	namespace := parts[0]
	name := parts[1]

	// Parse version if present
	version := 0
	if idx := strings.Index(name, "@"); idx > 0 {
		vStr := name[idx+1:]
		name = name[:idx]
		if v, err := strconv.Atoi(vStr); err == nil {
			version = v
		}
	}

	h.InvokeFunction(w, r, namespace+"/"+name, version)
}

// GetFunctionInfo handles GET /v1/functions/{name}
// Returns detailed information about a specific function.
func (h *ServerlessHandlers) GetFunctionInfo(w http.ResponseWriter, r *http.Request, name string, version int) {
	namespace, ok := managedNamespace(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	fn, err := h.registry.Get(ctx, namespace, name, version)
	if err != nil {
		if serverless.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "Function not found")
		} else {
			writeStoreError(w, "Failed to get function", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, fn)
}

// ListVersions handles GET /v1/functions/{name}/versions
// Lists all versions of a specific function.
func (h *ServerlessHandlers) ListVersions(w http.ResponseWriter, r *http.Request, name string) {
	namespace, ok := managedNamespace(w, r)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Get registry with extended methods
	reg, ok := h.registry.(*serverless.Registry)
	if !ok {
		writeError(w, http.StatusNotImplemented, "Version listing not supported")
		return
	}

	versions, err := reg.ListVersions(ctx, namespace, name)
	if err != nil {
		writeStoreError(w, "Failed to list versions", err)
		return
	}

	writeVersions(w, versions)
}

// writeVersions answers a version listing. A function that exists has at least
// one version, and deleting a function deletes its versions, so an empty list
// means there is no such function: a 404, not a 200 with nothing in it.
func writeVersions(w http.ResponseWriter, versions []*serverless.Function) {
	if len(versions) == 0 {
		writeError(w, http.StatusNotFound, "Function not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"versions": versions,
		"count":    len(versions),
	})
}

// reservedResponseHeaders are response headers a raw-HTTP-response tenant
// function (bugboard #835) must not be able to set or overwrite: gateway-owned
// trace/auth headers and hop-by-hop / framing-control headers. Compared
// case-insensitively; the X-Internal- and X-Orama- prefixes (the latter holds
// the tenant-origin marker the proxy in front reads) are matched separately.
var reservedResponseHeaders = map[string]struct{}{
	"x-request-id":        {},
	"x-duration-ms":       {},
	"content-length":      {},
	"transfer-encoding":   {},
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"upgrade":             {},
}

// isReservedResponseHeader reports whether a tenant-supplied response header key
// is reserved for the gateway and must be ignored in raw-HTTP-response mode.
func isReservedResponseHeader(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if _, ok := reservedResponseHeaders[k]; ok {
		return true
	}
	// Any internal-auth header the gateway uses for inter-service trust, and
	// any header the platform's own components exchange (x-orama-): a function
	// that blanked the tenant-origin marker would make its own 503 count
	// against its namespace's circuit breaker.
	return strings.HasPrefix(k, "x-internal-") || strings.HasPrefix(k, "x-orama-")
}
