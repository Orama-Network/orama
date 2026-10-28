package serverless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// FunctionInvoker is the minimal interface needed to invoke a function by
// name. It exists so packages downstream of `serverless` (notably
// `serverless/hostfunctions`) can hold a reference to the concrete
// `*Invoker` without creating an import cycle.
//
// Implemented by `*Invoker`.
type FunctionInvoker interface {
	Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error)
}

// Invoker handles function invocation with retry logic and DLQ support.
// It wraps the Engine to provide higher-level invocation semantics.
type Invoker struct {
	engine       *Engine
	registry     FunctionRegistry
	hostServices HostServices
	// servedNamespace is the one namespace whose functions this gateway runs:
	// its own client_namespace. See checkServed.
	servedNamespace string
	logger          *zap.Logger
}

// NewInvoker creates a new function invoker.
//
// servedNamespace is the namespace whose functions it runs — the gateway's own
// client_namespace. An invocation for any other namespace is refused.
func NewInvoker(engine *Engine, registry FunctionRegistry, hostServices HostServices, servedNamespace string, logger *zap.Logger) *Invoker {
	return &Invoker{
		engine:          engine,
		registry:        registry,
		hostServices:    hostServices,
		servedNamespace: strings.TrimSpace(servedNamespace),
		logger:          logger,
	}
}

// checkServed refuses an invocation of another namespace's function.
//
// Bugboard #427: the cluster gateway ran every namespace's functions, from
// function rows in the cluster registry. HTTP traffic for a tenant is now
// proxied to the tenant's gateway, but the cron scheduler, the pubsub
// dispatcher, the JWT claims provider and nested function_invoke all reach the
// invoker directly, and would have kept running tenant rows left in the
// registry. One gateway runs one namespace's functions, whichever path asks.
func (i *Invoker) checkServed(namespace string) error {
	if namespace == i.servedNamespace {
		return nil
	}
	return fmt.Errorf("%w: %w: this gateway runs the %q namespace's functions, and namespace %q's run on its own gateway",
		ErrFunctionNotFound, ErrNamespaceNotServed, i.servedNamespace, namespace)
}

// InvokeRequest contains the parameters for invoking a function.
type InvokeRequest struct {
	Namespace    string      `json:"namespace"`
	FunctionName string      `json:"function_name"`
	Version      int         `json:"version,omitempty"` // 0 = latest
	Input        []byte      `json:"input"`
	TriggerType  TriggerType `json:"trigger_type"`
	CallerWallet string      `json:"caller_wallet,omitempty"`
	// CallerIsAdmin is true when the caller holds the admin (control-plane)
	// scope. Only an admin (or a system trigger) may invoke a function marked
	// is_internal (bugboard #152). Set by the HTTP/WS handlers from the
	// request's resolved scope set.
	CallerIsAdmin bool `json:"caller_is_admin,omitempty"`
	// CallerHasInvoke is true when the caller holds the invoke grant (or admin).
	// API keys without invoke must not run private functions (bugboard #259).
	CallerHasInvoke bool `json:"caller_has_invoke,omitempty"`
	// CallerIP is the source IP of the request, used by the multi-tier
	// rate limiter as a fallback bucket for anonymous (no-wallet) callers.
	CallerIP string `json:"caller_ip,omitempty"`
	// CallerKey names the credential of a caller with no wallet: the
	// fingerprint of its API key, or of its key's or workload's token subject.
	// Empty for a wallet or an anonymous caller. The rate limiter buckets such
	// a caller by (key, IP), not by the namespace every key belongs to.
	CallerKey  string `json:"caller_key,omitempty"`
	WSClientID string `json:"ws_client_id,omitempty"`
	// CallerClaims holds custom JWT claims to expose via get_caller_claim.
	CallerClaims map[string]string `json:"caller_claims,omitempty"`
	// CallerJWTSubject carries the JWT `sub` claim explicitly so the
	// engine can populate InvocationContext.CallerJWTSubject — fixes the
	// bug-#215 case where API-key precedence buries the JWT identity.
	CallerJWTSubject string `json:"caller_jwt_subject,omitempty"`
	// CallerDeviceID is the device the caller's session is bound to — the
	// `did` of a token the gateway verified — or "" for a session bound to the
	// account alone, an API key, or no credential.
	CallerDeviceID string `json:"caller_device_id,omitempty"`
	// CallerAttestation is what the caller's device attestation proved, from
	// the verified token's `att` claim, or nil.
	CallerAttestation *CallerAttestation `json:"caller_attestation,omitempty"`
	// CallerCapability is what the capability the caller's socket was opened
	// with grants (feat-264), or nil. Only the gateway's WebSocket handler
	// sets it, after checking the token before the upgrade.
	CallerCapability *CapabilityGrant `json:"caller_capability,omitempty"`
	// TriggerDepth is the recursion-depth bucket at which this invocation
	// runs. 0 means top-level (HTTP/WS/cron source); each trigger-driven
	// invocation increments it. The dispatcher's host-fn wildcard path
	// (bugboard #93) uses this to bound local recursion that otherwise
	// would not round-trip through libp2p network latency.
	TriggerDepth int `json:"trigger_depth,omitempty"`

	// SystemOriginated marks an invocation the gateway itself started: a cron
	// row firing, a pubsub trigger matching, the JWT claims provider. Such an
	// invocation has no per-request caller to authorize — the authorization
	// happened when the trigger was registered — so it skips the caller check.
	//
	// This used to be inferred from TriggerType: a set of type values counted
	// as "system", and a nested call from a system-triggered parent was given
	// one of them, so the type carried the authority. A value that means
	// "skip authorization" should not be a label that travels with the work
	// and can be copied onto it; it is set here, by the gateway-internal
	// dispatcher that has the authority, and by nothing that reads a request.
	SystemOriginated bool `json:"-"`

	// ParentFunction is the function whose function_invoke or
	// function_invoke_async made this call, "" for a call from anything else.
	// A function that declares invocable_by runs only for a parent it names
	// (feat-971). Like SystemOriginated it is set by the host functions, from
	// the parent's own invocation context, and by nothing that reads a request.
	ParentFunction string `json:"-"`
}

// InvokeResponse contains the result of a function invocation.
type InvokeResponse struct {
	RequestID  string           `json:"request_id"`
	Output     []byte           `json:"output,omitempty"`
	Status     InvocationStatus `json:"status"`
	Error      string           `json:"error,omitempty"`
	DurationMS int64            `json:"duration_ms"`
	Retries    int              `json:"retries,omitempty"`

	// RawHTTP carries a verbatim HTTP response set by a RawHTTPResponse
	// function via set_http_response (bugboard #835). nil for normal
	// functions and for raw functions that never called set_http_response —
	// the HTTP handler falls back to the standard JSON/Ack path in that case.
	// Not serialized; consumed directly by the HTTP invoke handler.
	RawHTTP *RawHTTPResult `json:"-"`
}

// Invoke executes a function with automatic retry logic.
func (i *Invoker) Invoke(ctx context.Context, req *InvokeRequest) (*InvokeResponse, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Message: "cannot be nil"}
	}
	if req.FunctionName == "" {
		return nil, &ValidationError{Field: "function_name", Message: "cannot be empty"}
	}
	if req.Namespace == "" {
		return nil, &ValidationError{Field: "namespace", Message: "cannot be empty"}
	}

	requestID := uuid.New().String()
	startTime := time.Now()

	if err := i.checkServed(req.Namespace); err != nil {
		return &InvokeResponse{RequestID: requestID, Status: InvocationStatusError, Error: err.Error()}, err
	}

	// Get function from registry
	fn, err := i.registry.Get(ctx, req.Namespace, req.FunctionName, req.Version)
	if err != nil {
		return &InvokeResponse{
			RequestID:  requestID,
			Status:     InvocationStatusError,
			Error:      err.Error(),
			DurationMS: time.Since(startTime).Milliseconds(),
		}, err
	}

	// Check authorization — for everything except an invocation the gateway
	// itself started. A cron row firing, a pubsub trigger matching or the
	// claims provider running has no per-invocation caller identity to check:
	// the authorization happened when the trigger was registered (bugboard
	// #264 — gating those on CallerWallet blocked every fire for 19 hours).
	//
	// What says so is req.SystemOriginated, which only a gateway-internal
	// dispatcher sets. It used to be read off TriggerType, and a nested call
	// from a system-triggered parent was given a trigger type that counted as
	// system — so the authority to skip the check travelled with the work as
	// an ordinary field.
	//
	// A function that declares invocable_by is first refused to everything
	// but the parents it names (feat-971); a named parent's call is then
	// still the caller's, checked below like any other.
	if err := authorizeParent(fn, req); err != nil {
		return &InvokeResponse{
			RequestID:  requestID,
			Status:     InvocationStatusError,
			Error:      err.Error(),
			DurationMS: time.Since(startTime).Milliseconds(),
		}, err
	}
	if !req.SystemOriginated && !capabilityOpens(fn, req) &&
		!canInvokeFn(fn, req.CallerWallet, req.CallerIsAdmin, req.CallerHasInvoke) {
		// Authorization uses the function we already fetched above —
		// CanInvoke would re-`registry.Get` it, a redundant leader-routed
		// read on every op (bugboard #708).
		return &InvokeResponse{
			RequestID:  requestID,
			Status:     InvocationStatusError,
			Error:      "unauthorized",
			DurationMS: time.Since(startTime).Milliseconds(),
		}, ErrUnauthorized
	}

	// Get environment variables
	envVars, err := i.getEnvVars(ctx, fn.ID)
	if err != nil {
		i.logger.Warn("Failed to get env vars", zap.Error(err))
		envVars = make(map[string]string)
	}

	invCtx := newInvocationContext(req, fn, requestID, envVars)

	// Execute with retry logic
	output, retries, err := i.executeWithRetry(ctx, fn, req.Input, invCtx)

	response := &InvokeResponse{
		RequestID:  requestID,
		Output:     output,
		DurationMS: time.Since(startTime).Milliseconds(),
		Retries:    retries,
	}

	if err != nil {
		response.Status = InvocationStatusError
		response.Error = err.Error()

		// Check if it's a timeout
		if ctx.Err() == context.DeadlineExceeded {
			response.Status = InvocationStatusTimeout
		}

		return response, err
	}

	response.Status = InvocationStatusSuccess
	// Surface any verbatim HTTP response the function set (bugboard #835).
	response.RawHTTP = invCtx.RawHTTP
	return response, nil
}

// newInvocationContext is what the running function, and anything it invokes in
// turn, sees of the request that started it.
//
// It is a function of its own because what it carries is the authorization: the
// caller's identity and grants, and whether the gateway started this. A field
// dropped here is a grant lost at the first nested call, or an authority
// silently granted.
func newInvocationContext(req *InvokeRequest, fn *Function, requestID string, envVars map[string]string) *InvocationContext {
	return &InvocationContext{
		RequestID:        requestID,
		FunctionID:       fn.ID,
		FunctionName:     fn.Name,
		Namespace:        fn.Namespace,
		CallerWallet:     req.CallerWallet,
		CallerIP:         req.CallerIP,
		CallerKey:        req.CallerKey,
		CallerIsAdmin:    req.CallerIsAdmin,
		CallerHasInvoke:  req.CallerHasInvoke,
		SystemOriginated: req.SystemOriginated,
		TriggerType:      req.TriggerType,
		WSClientID:       req.WSClientID,
		EnvVars:          envVars,
		CallerClaims:     req.CallerClaims,
		CallerJWTSubject: req.CallerJWTSubject,
		CallerDeviceID:   req.CallerDeviceID,
		// A nested call is the same caller: its attestation goes with it.
		CallerAttestation: req.CallerAttestation,
		CallerCapability:  req.CallerCapability,
		TriggerDepth:      req.TriggerDepth,
	}
}

// InvalidateCache removes a compiled module from the engine's cache.
func (i *Invoker) InvalidateCache(wasmCID string) {
	i.engine.Invalidate(wasmCID)
}

// executeWithRetry executes a function with retry logic and DLQ.
func (i *Invoker) executeWithRetry(ctx context.Context, fn *Function, input []byte, invCtx *InvocationContext) ([]byte, int, error) {
	var lastErr error
	var output []byte

	maxAttempts := fn.RetryCount + 1 // Initial attempt + retries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Check if context is cancelled
		if ctx.Err() != nil {
			return nil, attempt, ctx.Err()
		}

		// Execute the function
		output, lastErr = i.engine.Execute(ctx, fn, input, invCtx)
		if lastErr == nil {
			return output, attempt, nil
		}

		i.logger.Warn("Function execution failed",
			zap.String("function", fn.Name),
			zap.String("request_id", invCtx.RequestID),
			zap.Int("attempt", attempt+1),
			zap.Int("max_attempts", maxAttempts),
			zap.Error(lastErr),
		)

		// Don't retry on certain errors
		if !i.isRetryable(lastErr) {
			break
		}

		// Don't wait after the last attempt
		if attempt < maxAttempts-1 {
			delay := i.calculateBackoff(fn.RetryDelaySeconds, attempt)
			select {
			case <-ctx.Done():
				return nil, attempt + 1, ctx.Err()
			case <-time.After(delay):
				// Continue to next attempt
			}
		}
	}

	// All retries exhausted - send to DLQ if configured
	if fn.DLQTopic != "" {
		i.sendToDLQ(ctx, fn, input, invCtx, lastErr)
	}

	return nil, maxAttempts - 1, lastErr
}

// isRetryable determines if an error should trigger a retry.
func (i *Invoker) isRetryable(err error) bool {
	// A WASM-fetch timeout is already retried inside GetWASMBytes (independent
	// 4s×3 budget) and is surfaced to the client as retryable
	// (FUNCTION_UNAVAILABLE). Re-running the whole invocation here would just
	// re-fetch and amplify IPFS load, so don't retry it at the invoker layer —
	// the client retries the request instead. (bugboard #137)
	if errors.Is(err, ErrWASMFetchTimeout) {
		return false
	}

	// Don't retry validation errors or not-found errors
	if IsNotFound(err) {
		return false
	}

	// Don't retry resource exhaustion (rate limits, memory)
	if IsResourceExhausted(err) {
		return false
	}

	// Don't re-run a function that ran out of time: it ran, so its effects may
	// have happened, and a second run holds another full timeout.
	if IsTimeout(err) {
		return false
	}

	// Retry service unavailable errors
	if IsServiceUnavailable(err) {
		return true
	}

	// Retry execution errors (could be transient)
	var execErr *ExecutionError
	if errors.As(err, &execErr) {
		return true
	}

	// Default to retryable for unknown errors
	return true
}

// calculateBackoff calculates the delay before the next retry attempt.
// Uses exponential backoff with jitter.
func (i *Invoker) calculateBackoff(baseDelaySeconds, attempt int) time.Duration {
	if baseDelaySeconds <= 0 {
		baseDelaySeconds = 5
	}

	// Exponential backoff: delay * 2^attempt
	delay := time.Duration(baseDelaySeconds) * time.Second
	for j := 0; j < attempt; j++ {
		delay *= 2
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
			break
		}
	}

	return delay
}

// sendToDLQ sends a failed invocation to the dead letter queue.
func (i *Invoker) sendToDLQ(ctx context.Context, fn *Function, input []byte, invCtx *InvocationContext, err error) {
	dlqMessage := DLQMessage{
		FunctionID:   fn.ID,
		FunctionName: fn.Name,
		Namespace:    fn.Namespace,
		RequestID:    invCtx.RequestID,
		Input:        input,
		Error:        err.Error(),
		FailedAt:     time.Now(),
		TriggerType:  invCtx.TriggerType,
		CallerWallet: invCtx.CallerWallet,
	}

	data, marshalErr := json.Marshal(dlqMessage)
	if marshalErr != nil {
		i.logger.Error("Failed to marshal DLQ message",
			zap.Error(marshalErr),
			zap.String("function", fn.Name),
		)
		return
	}

	// Publish to DLQ topic via host services
	if err := i.hostServices.PubSubPublish(ctx, fn.DLQTopic, data); err != nil {
		i.logger.Error("Failed to send to DLQ",
			zap.Error(err),
			zap.String("function", fn.Name),
			zap.String("dlq_topic", fn.DLQTopic),
		)
	} else {
		i.logger.Info("Sent failed invocation to DLQ",
			zap.String("function", fn.Name),
			zap.String("dlq_topic", fn.DLQTopic),
			zap.String("request_id", invCtx.RequestID),
		)
	}
}

// getEnvVars retrieves environment variables for a function.
func (i *Invoker) getEnvVars(ctx context.Context, functionID string) (map[string]string, error) {
	// Type assert to get extended registry methods
	if reg, ok := i.registry.(*Registry); ok {
		return reg.GetEnvVars(ctx, functionID)
	}
	return nil, nil
}

// DLQMessage represents a message sent to the dead letter queue.
type DLQMessage struct {
	FunctionID   string      `json:"function_id"`
	FunctionName string      `json:"function_name"`
	Namespace    string      `json:"namespace"`
	RequestID    string      `json:"request_id"`
	Input        []byte      `json:"input"`
	Error        string      `json:"error"`
	FailedAt     time.Time   `json:"failed_at"`
	TriggerType  TriggerType `json:"trigger_type"`
	CallerWallet string      `json:"caller_wallet,omitempty"`
}

// -----------------------------------------------------------------------------
// Batch Invocation (for future use)
// -----------------------------------------------------------------------------

// BatchInvokeRequest contains parameters for batch invocation.
type BatchInvokeRequest struct {
	Requests []*InvokeRequest `json:"requests"`
}

// BatchInvokeResponse contains results of batch invocation.
type BatchInvokeResponse struct {
	Responses []*InvokeResponse `json:"responses"`
	Duration  time.Duration     `json:"duration"`
}

// BatchInvoke executes multiple functions in parallel.
func (i *Invoker) BatchInvoke(ctx context.Context, req *BatchInvokeRequest) (*BatchInvokeResponse, error) {
	if req == nil || len(req.Requests) == 0 {
		return nil, &ValidationError{Field: "requests", Message: "cannot be empty"}
	}

	startTime := time.Now()
	responses := make([]*InvokeResponse, len(req.Requests))

	// For simplicity, execute sequentially for now
	// TODO: Implement parallel execution with goroutines and semaphore
	for idx, invReq := range req.Requests {
		resp, err := i.Invoke(ctx, invReq)
		if err != nil && resp == nil {
			responses[idx] = &InvokeResponse{
				RequestID: uuid.New().String(),
				Status:    InvocationStatusError,
				Error:     err.Error(),
			}
		} else {
			responses[idx] = resp
		}
	}

	return &BatchInvokeResponse{
		Responses: responses,
		Duration:  time.Since(startTime),
	}, nil
}

// CanInvokeFunction is the authorization decision for one function and one
// caller, for callers outside this package that have already fetched the
// function — the persistent-WebSocket upgrade, which builds its own invocation
// context and never reaches Invoke.
//
// It replaces an exported CanInvoke that re-read the function from the registry
// and passed the invoke grant as a hardcoded true, so a caller holding no
// invoke grant was reported as able to invoke a private function.
func CanInvokeFunction(fn *Function, callerWallet string, callerIsAdmin, callerHasInvoke bool) bool {
	return canInvokeFn(fn, callerWallet, callerIsAdmin, callerHasInvoke)
}

// canInvokeFn is the authorization decision for an already-fetched function.
//
//   - Public functions (`is_public: true`): anyone may invoke. The auth
//     middleware lets unauthenticated requests reach public paths.
//   - Private functions: an identified caller holding the invoke grant — an
//     API key with the `invoke` scope, an admin, or a SIWE wallet. A
//     storage-only key is refused (bugboard #259). HTTP `/invoke` is a public
//     path, so this is the choke point; scopeMiddleware never runs.
//   - Internal functions (`internal: true`, bugboard #152): an admin caller or
//     a gateway-started invocation, and nothing else. An ordinary app-runtime
//     key is refused even though its identity is valid.
//
// The HTTP handler reports SIWE wallets as hasInvoke; do not treat callerWallet
// as an API-key detector — getWalletFromRequest returns the namespace string
// for API keys, not an ak_ prefix.
//
// History (bug #215 follow-up): this was once
//
//	return callerWallet == namespace || fn.CreatedBy == callerWallet, nil
//
// which allowed only the namespace-name-as-wallet API-key fallback or the
// deploying wallet. Onboarding functions like `user-create`, where a brand-new
// wallet calls in to register, were refused with 401. They worked only as a
// side effect of JWT verification silently failing before #215 and callerWallet
// collapsing to the namespace string; fixing JWT verification surfaced the
// underlying flaw.
//
// Per-function ACLs (group membership, roles) are deferred until there is a
// concrete tenant requirement. Today "private" means "an authenticated
// in-namespace caller with the invoke grant".
func canInvokeFn(fn *Function, callerWallet string, callerIsAdmin, callerHasInvoke bool) bool {
	if fn.IsInternal && !callerIsAdmin {
		return false
	}
	if fn.IsPublic {
		return true
	}
	if callerIsAdmin {
		return true
	}
	if strings.TrimSpace(callerWallet) == "" {
		return false
	}
	return callerHasInvoke
}

// GetFunctionInfo returns basic info about a function for invocation.
func (i *Invoker) GetFunctionInfo(ctx context.Context, namespace, functionName string, version int) (*Function, error) {
	return i.registry.Get(ctx, namespace, functionName, version)
}

// ValidateInput performs basic input validation.
func (i *Invoker) ValidateInput(input []byte, maxSize int) error {
	if maxSize > 0 && len(input) > maxSize {
		return &ValidationError{
			Field:   "input",
			Message: fmt.Sprintf("exceeds maximum size of %d bytes", maxSize),
		}
	}
	return nil
}
