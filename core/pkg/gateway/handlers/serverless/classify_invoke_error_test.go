package serverless

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Orama-Network/orama/core/pkg/httputil"
	"github.com/Orama-Network/orama/core/pkg/serverless"
)

// TestClassifyInvokeError verifies the invoke-error → (status, code, retryable)
// mapping shared by the HTTP and WS paths. The key behavior (bugboard #137): a
// cold-WASM fetch timeout is a TRANSIENT infra failure (FUNCTION_UNAVAILABLE,
// retryable) — distinct from a genuine function error (not retryable).
func TestClassifyInvokeError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantStatus    int
		wantCode      httputil.RPCErrorCode
		wantRetryable bool
	}{
		{
			name:          "cold WASM fetch timeout is retryable FUNCTION_UNAVAILABLE",
			err:           serverless.ErrWASMFetchTimeout,
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      httputil.ErrCodeFunctionUnavailable,
			wantRetryable: true,
		},
		{
			name:          "wrapped cold WASM fetch timeout still classifies as transient",
			err:           fmt.Errorf("failed to fetch WASM: %w", serverless.ErrWASMFetchTimeout),
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      httputil.ErrCodeFunctionUnavailable,
			wantRetryable: true,
		},
		{
			// Mirrors the real production chain: engine.go wraps the fetch error
			// in *ExecutionError{Cause: ...}. errors.Is must still see through it.
			name:          "ExecutionError wrapping cold-WASM timeout classifies transient",
			err:           &serverless.ExecutionError{FunctionName: "fn", Cause: fmt.Errorf("failed to fetch WASM: %w", serverless.ErrWASMFetchTimeout)},
			wantStatus:    http.StatusServiceUnavailable,
			wantCode:      httputil.ErrCodeFunctionUnavailable,
			wantRetryable: true,
		},
		{
			// bug-936: the engine's timeout, wrapped as it is in production,
			// is its own code and status, never a rate limit.
			name:          "a function past its timeout is 504 FUNCTION_TIMEOUT, not retryable",
			err:           &serverless.ExecutionError{FunctionName: "fn", Cause: serverless.ErrTimeout},
			wantStatus:    http.StatusGatewayTimeout,
			wantCode:      httputil.ErrCodeFunctionTimeout,
			wantRetryable: false,
		},
		{
			// A tiered refusal reaches the WebSocket path through this
			// classifier; it was FUNCTION_EXECUTION_FAILED there.
			name:          "a tiered rate-limit refusal is 429 RATE_LIMITED, retryable",
			err:           &serverless.RateLimitedError{Scope: "per_wallet", RetryAfter: time.Second},
			wantStatus:    http.StatusTooManyRequests,
			wantCode:      httputil.ErrCodeRateLimited,
			wantRetryable: true,
		},
		{
			// bug-939/bug-940: name@N of a replaced version names why it is
			// refused, and an absent version stays plain NOT_FOUND.
			name:          "a superseded version is 404 FUNCTION_VERSION_SUPERSEDED",
			err:           serverless.ErrVersionSuperseded,
			wantStatus:    http.StatusNotFound,
			wantCode:      httputil.ErrCodeFunctionVersionSuperseded,
			wantRetryable: false,
		},
		{
			name:          "a version that does not exist is 404 NOT_FOUND",
			err:           serverless.ErrVersionNotFound,
			wantStatus:    http.StatusNotFound,
			wantCode:      httputil.ErrCodeNotFound,
			wantRetryable: false,
		},
		{
			// feat-971: a refusal no credential changes is 403 by name,
			// never the 401 every other unauthorized answers.
			name:          "a function only its parents may invoke is 403 FUNCTION_NOT_DIRECTLY_INVOCABLE",
			err:           serverless.RefuseClientInvocation(&serverless.Function{Name: "send-message", InvocableBy: []string{"rpc-router"}}, false),
			wantStatus:    http.StatusForbidden,
			wantCode:      httputil.ErrCodeFunctionNotDirectlyInvocable,
			wantRetryable: false,
		},
		{
			name:          "generic function error is non-retryable FUNCTION_EXECUTION_FAILED",
			err:           errors.New("panic: index out of range"),
			wantStatus:    http.StatusInternalServerError,
			wantCode:      httputil.ErrCodeFunctionExecution,
			wantRetryable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code, retryable := classifyInvokeError(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if retryable != tt.wantRetryable {
				t.Errorf("retryable = %v, want %v", retryable, tt.wantRetryable)
			}
		})
	}
}
