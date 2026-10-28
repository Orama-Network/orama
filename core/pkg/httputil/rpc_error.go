package httputil

// rpc_error.go defines the canonical RPC error envelope used by every
// gateway endpoint that serves typed RPC-style errors (function invoke,
// function deploy, push send, pubsub publish, etc.).
//
// Why this exists: prior to this file the gateway returned at least three
// different error shapes — `{error: "msg"}` from one path, an envelope
// with `request_id/duration_ms/error` from another, and an absent `error`
// field in a third. Generic clients couldn't write a single error parser.
//
// Canonical shape (always populated):
//
//	{
//	  "ok":    false,
//	  "error": {
//	    "code":       "VALIDATION_FAILED",      // typed enum, never empty
//	    "message":    "missing username",       // human-readable, never empty
//	    "retryable":  false,
//	    "request_id": "abc...",                 // optional
//	    "retry_after": 2.5                      // optional, seconds (float)
//	  }
//	}
//
// HTTP status code is set on the response and reflects the error class;
// the envelope's code is the typed enum a client switches on.

import (
	"net/http"
)

// HeaderTenantOrigin marks a response as the tenant's code's own answer, not
// the platform's: a function's status or the failure to load it, written by a
// namespace gateway, or the response of a deployed app, relayed by its home
// node to the node that forwarded the request. The circuit breaker of the
// gateway that forwarded does not count a 502, 503 or 504 that carries it,
// because what a tenant's code answers says nothing about the node or gateway
// that ran it. The forwarding gateway removes it before the client sees it.
const HeaderTenantOrigin = "X-Orama-Tenant-Origin"

// MarkTenantOrigin sets the tenant-origin marker on h. It removes any value
// first, so one the tenant's code put there (an empty one, say) cannot sit
// beside it.
func MarkTenantOrigin(h http.Header) {
	h.Del(HeaderTenantOrigin)
	h.Set(HeaderTenantOrigin, "1")
}

// RPCErrorCode is the typed error-code enum. New codes go here, alphabetic
// within their class. Codes are stable strings — clients pin to them.
type RPCErrorCode string

const (
	// 4xx — client error
	ErrCodeValidationFailed RPCErrorCode = "VALIDATION_FAILED"
	ErrCodeUnauthorized     RPCErrorCode = "UNAUTHORIZED"
	ErrCodeForbidden        RPCErrorCode = "FORBIDDEN"
	ErrCodeNotFound         RPCErrorCode = "NOT_FOUND"
	ErrCodeConflict         RPCErrorCode = "CONFLICT"
	ErrCodeRateLimited      RPCErrorCode = "RATE_LIMITED"
	ErrCodePayloadTooLarge  RPCErrorCode = "PAYLOAD_TOO_LARGE"
	// ErrCodeStorageQuotaExceeded — the namespace's configured storage budget
	// (max_storage_bytes) would be exceeded by this pin/upload, RF-aware
	// (bugboard #141). Non-retryable: the caller must free space or raise the
	// budget. Only enforced for namespaces that have a positive budget row.
	ErrCodeStorageQuotaExceeded RPCErrorCode = "STORAGE_QUOTA_EXCEEDED"
	// ErrCodeUnpinnedUploadRefused — an upload asked not to be pinned (pin
	// other than "true"). Every stored object is pinned with the namespace's
	// replication factor and counted against its references; an unpinned one
	// could not be downloaded and would be reclaimed (bugboard #432).
	ErrCodeUnpinnedUploadRefused RPCErrorCode = "UNPINNED_UPLOAD_REFUSED"

	// 5xx — server error
	ErrCodeInternal           RPCErrorCode = "INTERNAL"
	ErrCodeServiceUnavailable RPCErrorCode = "SERVICE_UNAVAILABLE"
	ErrCodeTimeout            RPCErrorCode = "TIMEOUT"
	// ErrCodeNamespaceGatewayUnavailable — none of the namespace's gateways
	// could be reached (every member refused the connection or has its circuit
	// open). Nothing was sent to any of them, so the exact request is safe to
	// retry; a WebSocket client should reconnect with backoff.
	ErrCodeNamespaceGatewayUnavailable RPCErrorCode = "NAMESPACE_GATEWAY_UNAVAILABLE"

	// Function-specific (5xx-mapped but distinct codes for client routing)
	ErrCodeFunctionExecution RPCErrorCode = "FUNCTION_EXECUTION_FAILED"
	ErrCodeFunctionDeploy    RPCErrorCode = "FUNCTION_DEPLOY_FAILED"
	ErrCodeSchemaMismatch    RPCErrorCode = "SCHEMA_MISMATCH"
	// ErrCodeFunctionUnavailable is a TRANSIENT infra failure loading a
	// function's code (e.g. its WASM blob isn't yet retrievable from IPFS on
	// this node). Distinct from FUNCTION_EXECUTION_FAILED (a real, non-retryable
	// error inside the function); this one is safe to retry the exact request.
	ErrCodeFunctionUnavailable RPCErrorCode = "FUNCTION_UNAVAILABLE"
	// ErrCodeFunctionTimeout is a function that ran and was stopped at its
	// timeout. It is not RATE_LIMITED, which is a request refused before the
	// function ran: whatever the function did before it was stopped (a write,
	// an upstream call, a broadcast) may have happened. Not retryable by
	// default — only a caller that knows the call is idempotent repeats it.
	ErrCodeFunctionTimeout RPCErrorCode = "FUNCTION_TIMEOUT"
	// ErrCodeFunctionVersionSuperseded is a request for a version of a
	// function that a later deploy or a rollback replaced (404). Only the
	// current version runs; a rollback makes an earlier one current again.
	ErrCodeFunctionVersionSuperseded RPCErrorCode = "FUNCTION_VERSION_SUPERSEDED"
	// ErrCodeFunctionNotDirectlyInvocable is a client naming a function that
	// declares `invocable_by` (403): it runs only when one of the functions it
	// names calls it with function_invoke or function_invoke_async. No
	// credential a client can hold changes that; call the parent instead.
	ErrCodeFunctionNotDirectlyInvocable RPCErrorCode = "FUNCTION_NOT_DIRECTLY_INVOCABLE"
)

// RPCErrorEnvelope is the canonical wire shape. Use WriteRPCError to emit;
// the struct is exported so SDK clients can decode/match against it.
type RPCErrorEnvelope struct {
	OK    bool            `json:"ok"`
	Error *RPCErrorDetail `json:"error"`
}

// RPCErrorDetail is the typed error body. `Code` and `Message` are
// always populated by WriteRPCError — clients can rely on that contract.
type RPCErrorDetail struct {
	Code      RPCErrorCode `json:"code"`
	Message   string       `json:"message"`
	Retryable bool         `json:"retryable"`
	// Optional metadata. omitempty so clients don't see noise on simple cases.
	RequestID  string  `json:"request_id,omitempty"`
	RetryAfter float64 `json:"retry_after,omitempty"`
}

// RPCErrorOption customizes the envelope (request id, retry-after, etc.).
// Callers build chains like WriteRPCError(w, 429, code, msg,
//
//	WithRetryAfter(2.5), WithRequestID(reqID)).
type RPCErrorOption func(*RPCErrorDetail)

// WithRequestID attaches the gateway request ID to the error detail.
// Useful for cross-referencing client errors with gateway logs.
func WithRequestID(id string) RPCErrorOption {
	return func(d *RPCErrorDetail) { d.RequestID = id }
}

// WithRetryAfter sets the retry-after hint (seconds, float). Sets
// Retryable=true automatically — anything with a retry hint is retryable.
func WithRetryAfter(seconds float64) RPCErrorOption {
	return func(d *RPCErrorDetail) {
		d.RetryAfter = seconds
		d.Retryable = true
	}
}

// WithRetryable marks the error as retryable without a specific delay.
// Only set this for transient errors (rate limiting, temporary upstream
// unavailability). Don't set it for validation errors — retrying with the
// same input won't help.
func WithRetryable() RPCErrorOption {
	return func(d *RPCErrorDetail) { d.Retryable = true }
}

// WriteRPCError writes the canonical envelope. Both code and message are
// REQUIRED — empty values are normalized so clients never see an empty
// message.
//
// Example:
//
//	httputil.WriteRPCError(w, http.StatusBadRequest,
//	    httputil.ErrCodeValidationFailed, "username required")
//
// With options:
//
//	httputil.WriteRPCError(w, http.StatusTooManyRequests,
//	    httputil.ErrCodeRateLimited, "wallet over per-minute cap",
//	    httputil.WithRetryAfter(1.2),
//	    httputil.WithRequestID(reqID))
func WriteRPCError(w http.ResponseWriter, status int, code RPCErrorCode, message string, opts ...RPCErrorOption) {
	if code == "" {
		code = ErrCodeInternal
	}
	if message == "" {
		message = defaultMessageFor(code)
	}
	detail := &RPCErrorDetail{
		Code:      code,
		Message:   message,
		Retryable: defaultRetryableFor(code),
	}
	for _, opt := range opts {
		opt(detail)
	}

	// On rate-limit responses with a retry hint, also surface the standard
	// HTTP Retry-After header so non-RPC-aware clients honor it.
	if detail.RetryAfter > 0 {
		w.Header().Set("Retry-After", formatFloat(detail.RetryAfter))
	}

	WriteJSON(w, status, RPCErrorEnvelope{OK: false, Error: detail})
}

// defaultMessageFor returns a sensible fallback when the caller passed
// an empty message string. We never leave the envelope's message empty —
// that was the bug.
func defaultMessageFor(code RPCErrorCode) string {
	switch code {
	case ErrCodeValidationFailed:
		return "request failed validation"
	case ErrCodeUnauthorized:
		return "authentication required"
	case ErrCodeForbidden:
		return "access denied"
	case ErrCodeNotFound:
		return "resource not found"
	case ErrCodeConflict:
		return "request conflicts with current state"
	case ErrCodeRateLimited:
		return "rate limit exceeded"
	case ErrCodePayloadTooLarge:
		return "request payload too large"
	case ErrCodeInternal:
		return "internal server error"
	case ErrCodeServiceUnavailable:
		return "service temporarily unavailable"
	case ErrCodeTimeout:
		return "request timed out"
	case ErrCodeNamespaceGatewayUnavailable:
		return "no namespace gateway could be reached, retry"
	case ErrCodeFunctionExecution:
		return "function execution failed"
	case ErrCodeFunctionUnavailable:
		return "function temporarily unavailable, retry"
	case ErrCodeFunctionTimeout:
		return "function ran past its timeout; its effects may have happened"
	case ErrCodeFunctionVersionSuperseded:
		return "function version superseded; only the current version can be invoked"
	case ErrCodeFunctionNotDirectlyInvocable:
		return "function is not directly invocable; it runs only when a function it names calls it"
	case ErrCodeFunctionDeploy:
		return "function deployment failed"
	case ErrCodeSchemaMismatch:
		return "gateway schema does not match required version"
	default:
		return "an error occurred"
	}
}

// defaultRetryableFor seeds the retryable bit based on the error class.
// Callers can override via WithRetryable() / WithRetryAfter().
func defaultRetryableFor(code RPCErrorCode) bool {
	switch code {
	case ErrCodeRateLimited, ErrCodeServiceUnavailable, ErrCodeTimeout, ErrCodeFunctionUnavailable, ErrCodeNamespaceGatewayUnavailable:
		return true
	default:
		return false
	}
}

// formatFloat renders a float seconds value compactly for the
// Retry-After HTTP header. We avoid stdlib strconv import here to keep
// this file's dependency set tight.
func formatFloat(v float64) string {
	if v <= 0 {
		return "0"
	}
	// Round to one decimal place; clients don't need sub-100ms precision.
	tenths := int64(v*10 + 0.5)
	whole := tenths / 10
	frac := tenths % 10
	if frac == 0 {
		return itoa(whole)
	}
	return itoa(whole) + "." + itoa(frac)
}

// itoa is a small unsigned-int formatter (Go's strconv would also work
// but this keeps the file small + dep-free).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
