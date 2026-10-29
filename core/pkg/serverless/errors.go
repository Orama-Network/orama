package serverless

import (
	"errors"
	"fmt"
)

// Sentinel errors for common conditions.
var (
	// ErrFunctionNotFound is returned when a function does not exist.
	ErrFunctionNotFound = errors.New("function not found")

	// ErrFunctionExists is returned when attempting to create a function that already exists.
	ErrFunctionExists = errors.New("function already exists")

	// ErrVersionNotFound is returned when a specific function version does not exist.
	ErrVersionNotFound = errors.New("function version not found")

	// ErrVersionSuperseded is returned for a version a later deploy or a
	// rollback replaced: only the current version of a function is invocable.
	ErrVersionSuperseded = errors.New("function version superseded: only the current version of a function can be invoked; roll back to make this version current")

	// ErrSecretNotFound is returned when a secret does not exist.
	ErrSecretNotFound = errors.New("secret not found")

	// ErrJobNotFound is returned when a job does not exist.
	ErrJobNotFound = errors.New("job not found")

	// ErrTriggerNotFound is returned when a trigger does not exist.
	ErrTriggerNotFound = errors.New("trigger not found")

	// ErrTimerNotFound is returned when a timer does not exist.
	ErrTimerNotFound = errors.New("timer not found")

	// ErrUnauthorized is returned when the caller is not authorized.
	ErrUnauthorized = errors.New("unauthorized")

	// ErrFunctionNotDirectlyInvocable is returned when a function that declares
	// invocable_by is invoked by anything other than a function it names: a
	// client on any route, or another function of the namespace. It wraps
	// ErrUnauthorized, so every path that treats a refusal as permanent treats
	// this one so too.
	ErrFunctionNotDirectlyInvocable = fmt.Errorf("function not directly invocable: %w", ErrUnauthorized)

	// ErrRateLimited is returned when the rate limit is exceeded.
	ErrRateLimited = errors.New("rate limit exceeded")

	// ErrInvalidWASM is returned when the WASM module is invalid.
	ErrInvalidWASM = errors.New("invalid WASM module")

	// ErrCompilationFailed is returned when WASM compilation fails.
	ErrCompilationFailed = errors.New("WASM compilation failed")

	// ErrExecutionFailed is returned when function execution fails.
	ErrExecutionFailed = errors.New("function execution failed")

	// ErrTimeout is returned when function execution times out.
	ErrTimeout = errors.New("function execution timeout")

	// ErrMemoryExceeded is returned when the function exceeds memory limits.
	ErrMemoryExceeded = errors.New("memory limit exceeded")

	// ErrInvalidInput is returned when function input is invalid.
	ErrInvalidInput = errors.New("invalid input")

	// ErrWSNotAvailable is returned when WebSocket operations are used outside WS context.
	ErrWSNotAvailable = errors.New("websocket operations not available in this context")

	// ErrFunctionInvokeNotAvailable is returned when FunctionInvoke is called
	// but no invoker has been wired into the host-services bag (e.g. unit
	// tests, or before the gateway finishes bootstrap).
	ErrFunctionInvokeNotAvailable = errors.New("function_invoke not available in this context")

	// ErrWSClientNotFound is returned when a WebSocket client is not connected.
	ErrWSClientNotFound = errors.New("websocket client not found")

	// ErrInvalidCronExpression is returned when a cron expression is invalid.
	ErrInvalidCronExpression = errors.New("invalid cron expression")

	// ErrPayloadTooLarge is returned when a job payload exceeds the maximum size.
	ErrPayloadTooLarge = errors.New("payload too large")

	// ErrQueueFull is returned when the job queue is full.
	ErrQueueFull = errors.New("job queue is full")

	// ErrJobCancelled is returned when a job is cancelled.
	ErrJobCancelled = errors.New("job cancelled")

	// ErrStorageUnavailable is returned when IPFS storage is unavailable.
	ErrStorageUnavailable = errors.New("storage unavailable")

	// ErrWASMFetchTimeout marks a transient failure fetching a function's WASM
	// bytecode from IPFS (the block is cold/not-yet-replicated on this node).
	// It is an infra failure, not a function error, so callers must treat it as
	// retryable and surface a retryable error envelope to clients.
	ErrWASMFetchTimeout = errors.New("wasm fetch from IPFS timed out")

	// ErrWASMNotStored is returned when a function's WASM is not in the
	// cluster's pinset: nothing the cluster knows of stores it, so no retry
	// can fetch it, and the function cannot run until it is deployed again.
	ErrWASMNotStored = errors.New("function WASM is not stored in the cluster")

	// ErrDatabaseUnavailable is returned when the database is unavailable.
	ErrDatabaseUnavailable = errors.New("database unavailable")

	// ErrNamespaceNotServed is returned when a gateway is asked to run a
	// function of a namespace other than its own (bugboard #427).
	ErrNamespaceNotServed = errors.New("namespace not served by this gateway")

	// ErrDatabaseOfAnotherNamespace is returned when a database host call comes
	// from a function whose namespace does not own the gateway's database
	// (bugboard #427).
	ErrDatabaseOfAnotherNamespace = errors.New("this gateway's database belongs to another namespace")

	// ErrCacheUnavailable is returned when the cache is unavailable.
	ErrCacheUnavailable = errors.New("cache unavailable")

	// ErrCacheMiss is returned by CacheGet when the key is not in the cache.
	ErrCacheMiss = errors.New("cache miss")

	// ErrInvalidCacheTTL is returned when cache_set is given a ttl it refuses:
	// negative, or longer than olric.MaxEntryTTL. A guest mistake, not a
	// gateway fault.
	ErrInvalidCacheTTL = errors.New("invalid cache ttl")

	// ErrAsyncInvokeSaturated is returned when function_invoke_async is
	// refused because the gateway already runs its maximum of in-flight async
	// invocations. Nothing ran; the guest may retry or call synchronously.
	ErrAsyncInvokeSaturated = errors.New("too many in-flight async invocations")
)

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("config error: %s: %s", e.Field, e.Message)
}

// DeployError represents an error during function deployment.
type DeployError struct {
	FunctionName string
	Cause        error
}

func (e *DeployError) Error() string {
	return fmt.Sprintf("deploy error for function '%s': %v", e.FunctionName, e.Cause)
}

func (e *DeployError) Unwrap() error {
	return e.Cause
}

// ExecutionError represents an error during function execution.
type ExecutionError struct {
	FunctionName string
	RequestID    string
	Cause        error
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("execution error for function '%s' (request %s): %v",
		e.FunctionName, e.RequestID, e.Cause)
}

func (e *ExecutionError) Unwrap() error {
	return e.Cause
}

// HostFunctionError represents an error in a host function call.
type HostFunctionError struct {
	Function string
	Cause    error
}

func (e *HostFunctionError) Error() string {
	return fmt.Sprintf("host function '%s' error: %v", e.Function, e.Cause)
}

func (e *HostFunctionError) Unwrap() error {
	return e.Cause
}

// TriggerError represents an error in trigger execution.
type TriggerError struct {
	TriggerType string
	TriggerID   string
	FunctionID  string
	Cause       error
}

func (e *TriggerError) Error() string {
	return fmt.Sprintf("trigger error (%s/%s) for function '%s': %v",
		e.TriggerType, e.TriggerID, e.FunctionID, e.Cause)
}

func (e *TriggerError) Unwrap() error {
	return e.Cause
}

// ValidationError represents an input validation error.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error: %s: %s", e.Field, e.Message)
}

// RetryableError wraps an error that should be retried.
type RetryableError struct {
	Cause      error
	RetryAfter int // Suggested retry delay in seconds
	MaxRetries int // Maximum number of retries remaining
	CurrentTry int // Current attempt number
}

func (e *RetryableError) Error() string {
	return fmt.Sprintf("retryable error (attempt %d): %v", e.CurrentTry, e.Cause)
}

func (e *RetryableError) Unwrap() error {
	return e.Cause
}

// IsRetryable checks if an error should be retried.
func IsRetryable(err error) bool {
	var retryable *RetryableError
	return errors.As(err, &retryable)
}

// IsNotFound checks if an error indicates a resource was not found.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrFunctionNotFound) ||
		errors.Is(err, ErrVersionNotFound) ||
		errors.Is(err, ErrVersionSuperseded) ||
		errors.Is(err, ErrSecretNotFound) ||
		errors.Is(err, ErrJobNotFound) ||
		errors.Is(err, ErrTriggerNotFound) ||
		errors.Is(err, ErrTimerNotFound) ||
		errors.Is(err, ErrWSClientNotFound)
}

// IsUnauthorized checks if an error indicates a lack of authorization.
func IsUnauthorized(err error) bool {
	return errors.Is(err, ErrUnauthorized)
}

// IsResourceExhausted checks if an error indicates resource exhaustion: the
// request was refused before the function ran. A timeout is not one of them
// (see IsTimeout): that function ran, and what it did may have happened.
func IsResourceExhausted(err error) bool {
	return errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrMemoryExceeded) ||
		errors.Is(err, ErrPayloadTooLarge) ||
		errors.Is(err, ErrQueueFull)
}

// IsTimeout reports whether a function ran and was stopped at its deadline.
func IsTimeout(err error) bool {
	return errors.Is(err, ErrTimeout)
}

// IsServiceUnavailable checks if an error indicates a service is unavailable.
func IsServiceUnavailable(err error) bool {
	return errors.Is(err, ErrStorageUnavailable) ||
		errors.Is(err, ErrDatabaseUnavailable) ||
		errors.Is(err, ErrCacheUnavailable)
}
