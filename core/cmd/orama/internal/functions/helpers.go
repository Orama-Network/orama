package functions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/Orama-Network/orama/core/cmd/orama/internal/clierr"
	"github.com/Orama-Network/orama/core/cmd/orama/internal/shared"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// FunctionConfig represents the function.yaml configuration.
type FunctionConfig struct {
	Name   string `yaml:"name"`
	Public bool   `yaml:"public"`
	// Internal marks a function invokable ONLY by a system trigger or an admin
	// caller (bugboard #152). A normal app-runtime key invoking it by name is
	// rejected. Default false → invokable as before.
	Internal bool              `yaml:"internal"`
	Memory   int               `yaml:"memory"`
	Timeout  int               `yaml:"timeout"`
	Retry    RetryConfig       `yaml:"retry"`
	Env      map[string]string `yaml:"env"`

	// Persistent WebSocket settings — when WSPersistent is true, the function
	// must export ws_open / ws_frame / ws_close instead of running per-frame
	// stateless. See core/plans/platform/06_PERSISTENT_WS_FUNCTIONS.md.
	WSPersistent         bool `yaml:"ws_persistent"`
	WSIdleTimeoutSec     int  `yaml:"ws_idle_timeout_sec"`
	WSMaxFrameBytes      int  `yaml:"ws_max_frame_bytes"`
	WSMaxInflightPerConn int  `yaml:"ws_max_inflight_per_conn"`

	// Reactor enables reactor-mode for a STATELESS function (bugboard #898):
	// the function exports `handle(ptr,len)->packed` and is built
	// `-buildmode=c-shared` (WASI reactor: `_initialize`, no `_start`). The
	// gateway pre-warms instances so the ~550ms TinyGo cold-start runs ahead of
	// the request instead of on it. The function's init() must be
	// identity-independent (warming has no caller in scope); do per-caller work
	// in handle(). Unset = command mode (back-compat).
	Reactor bool `yaml:"reactor"`

	// RawHTTPResponse enables raw-HTTP-response mode (bugboard #835) — the
	// function may call set_http_response to emit a verbatim HTTP response
	// (status/headers/body) instead of the JSON/Ack-wrapped output.
	RawHTTPResponse bool `yaml:"raw_http_response"`

	// WSAuth is how the function's WebSocket may be opened: empty for a
	// caller's credential, or "capability" to also accept a capability the
	// function minted (feat-264). The gateway validates it at deploy.
	WSAuth string `yaml:"ws_auth"`

	// RateLimit is the function's own invoke limits (bug-740, feat-938),
	// checked by the gateway on top of its own per-caller and per-namespace
	// buckets. The gateway validates the ranges at deploy.
	RateLimit *RateLimitConfig `yaml:"rate_limit"`

	// InvocableBy names the functions of the namespace that may invoke this
	// one with function_invoke or function_invoke_async (feat-971); a client
	// invoking it directly is refused 403 FUNCTION_NOT_DIRECTLY_INVOCABLE.
	// Empty = any caller the public/private rule admits. The gateway checks
	// at deploy that each name is a deployed function.
	InvocableBy []string `yaml:"invocable_by"`

	// OpenToPendingDevices lets a pending device's restricted session (the
	// namespace's function device policy, feat-973) invoke the function and
	// open its WebSocket: the functions that carry out the namespace's own
	// admission of a new device. False refuses one 403
	// DEVICE_PENDING_APPROVAL.
	OpenToPendingDevices bool `yaml:"open_to_pending_devices"`
}

// RateLimitConfig is function.yaml's rate_limit. Each rule allows `requests`
// calls per `window`: per_caller to each caller (a wallet; a key caller by key
// and address; an anonymous caller by address), per_ip to each client address
// whoever signed in.
type RateLimitConfig struct {
	PerCaller *RateLimitRule `yaml:"per_caller"`
	PerIP     *RateLimitRule `yaml:"per_ip"`
}

// RateLimitRule is one rule: requests per window, the window a duration such
// as 30s, 1m or 1h in whole seconds.
type RateLimitRule struct {
	Requests int    `yaml:"requests"`
	Window   string `yaml:"window"`
}

// rateLimitMetadata is rate_limit in the shape the deploy metadata carries it
// (window in seconds), or nil when the function declares none.
func rateLimitMetadata(rl *RateLimitConfig) (map[string]interface{}, error) {
	if rl == nil {
		return nil, nil
	}
	out := map[string]interface{}{}
	for field, rule := range map[string]*RateLimitRule{"per_caller": rl.PerCaller, "per_ip": rl.PerIP} {
		if rule == nil {
			continue
		}
		window, err := time.ParseDuration(rule.Window)
		if err != nil {
			return nil, fmt.Errorf("'rate_limit.%s.window' must be a duration such as 30s, 1m or 1h (got %q)", field, rule.Window)
		}
		if window%time.Second != 0 {
			return nil, fmt.Errorf("'rate_limit.%s.window' must be whole seconds (got %s)", field, window)
		}
		out[field] = map[string]int{"requests": rule.Requests, "window_seconds": int(window / time.Second)}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// RetryConfig holds retry settings.
type RetryConfig struct {
	Count int `yaml:"count"`
	Delay int `yaml:"delay"`
}

// wasmMagicBytes is the WASM binary magic number: \0asm
var wasmMagicBytes = []byte{0x00, 0x61, 0x73, 0x6d}

// validNameRegex validates function names (alphanumeric, hyphens, underscores).
var validNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// functionNameArgs is the Args of a command that takes exactly n positional
// arguments, the first of which names a deployed function. A name that is not
// a function name is refused as a usage error before any request: it is
// written into a request path, where "../" would reach another route.
func functionNameArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return err
		}
		if !validNameRegex.MatchString(args[0]) {
			return fmt.Errorf("invalid function name %q: must start with a letter and contain only letters, digits, hyphens, or underscores", args[0])
		}
		return nil
	}
}

// LoadConfig reads and parses a function.yaml from the given directory.
func LoadConfig(dir string) (*FunctionConfig, error) {
	path := filepath.Join(dir, "function.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read function.yaml: %w", err)
	}

	var cfg FunctionConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse function.yaml: %w", err)
	}

	// Apply defaults
	if cfg.Memory == 0 {
		cfg.Memory = 64
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30
	}
	if cfg.Retry.Delay == 0 {
		cfg.Retry.Delay = 5
	}

	// Validate
	if cfg.Name == "" {
		return nil, fmt.Errorf("function.yaml: 'name' is required")
	}
	if !validNameRegex.MatchString(cfg.Name) {
		return nil, fmt.Errorf("function.yaml: 'name' must start with a letter and contain only letters, digits, hyphens, or underscores")
	}
	if cfg.Memory < 1 || cfg.Memory > 256 {
		return nil, fmt.Errorf("function.yaml: 'memory' must be between 1 and 256 MB (got %d)", cfg.Memory)
	}
	if cfg.Timeout < 1 || cfg.Timeout > 300 {
		return nil, fmt.Errorf("function.yaml: 'timeout' must be between 1 and 300 seconds (got %d)", cfg.Timeout)
	}
	if _, err := rateLimitMetadata(cfg.RateLimit); err != nil {
		return nil, fmt.Errorf("function.yaml: %w", err)
	}
	for _, parent := range cfg.InvocableBy {
		if !validNameRegex.MatchString(parent) {
			return nil, fmt.Errorf("function.yaml: 'invocable_by' names %q, which is not a function name", parent)
		}
	}

	return &cfg, nil
}

// ValidateWASM checks that the given bytes are a valid WASM binary (magic number check).
func ValidateWASM(data []byte) error {
	if len(data) < 8 {
		return fmt.Errorf("file too small to be a valid WASM binary (%d bytes)", len(data))
	}
	if !bytes.HasPrefix(data, wasmMagicBytes) {
		return fmt.Errorf("file is not a valid WASM binary (bad magic bytes)")
	}
	return nil
}

// ValidateWASMFile checks that the file at the given path is a valid WASM binary.
func ValidateWASMFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open WASM file: %w", err)
	}
	defer f.Close()

	header := make([]byte, 8)
	n, err := f.Read(header)
	if err != nil {
		return fmt.Errorf("failed to read WASM file: %w", err)
	}
	return ValidateWASM(header[:n])
}

// apiRequest performs an authenticated HTTP request to the gateway API.
func apiRequest(method, endpoint string, body io.Reader, contentType string) (*http.Response, error) {
	apiURL, err := shared.GetAPIURL()
	if err != nil {
		return nil, err
	}
	url := apiURL + endpoint

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	token, err := shared.GetAuthToken()
	if err != nil {
		return nil, fmt.Errorf("authentication required: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, clierr.Unavailable("failed to reach the gateway at %s: %w", apiURL, err)
	}
	return resp, nil
}

// apiStatusError is the error for a gateway answer that is not a success, with
// the exit code that says what kind of answer it was: a credential the gateway
// refuses is an auth failure, a function or trigger that is not there is "not
// found", and a gateway that cannot serve right now is unavailable, which a
// script may retry. Any other refusal is the generic failure.
func apiStatusError(what string, status int, body []byte) error {
	err := fmt.Errorf("%s (%d): %s", what, status, string(body))
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return clierr.Wrap(clierr.CodeAuth, err)
	case http.StatusNotFound:
		return clierr.Wrap(clierr.CodeNotFound, err)
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return clierr.Wrap(clierr.CodeUnavailable, err)
	}
	return err
}

// apiGet performs an authenticated GET request and returns the parsed JSON response.
func apiGet(endpoint string) (map[string]interface{}, error) {
	resp, err := apiRequest("GET", endpoint, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, apiStatusError("API error", resp.StatusCode, respBody)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result, nil
}

// apiDelete performs an authenticated DELETE request and returns the parsed JSON response.
func apiDelete(endpoint string) (map[string]interface{}, error) {
	resp, err := apiRequest("DELETE", endpoint, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, apiStatusError("API error", resp.StatusCode, respBody)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result, nil
}

// uploadWASMFunction uploads a WASM file to the deploy endpoint via multipart/form-data.
func uploadWASMFunction(wasmPath string, cfg *FunctionConfig) (map[string]interface{}, error) {
	wasmFile, err := os.Open(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open WASM file: %w", err)
	}
	defer wasmFile.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add form fields
	writer.WriteField("name", cfg.Name)
	writer.WriteField("is_public", strconv.FormatBool(cfg.Public))
	writer.WriteField("is_internal", strconv.FormatBool(cfg.Internal))
	writer.WriteField("memory_limit_mb", strconv.Itoa(cfg.Memory))
	writer.WriteField("timeout_seconds", strconv.Itoa(cfg.Timeout))
	writer.WriteField("retry_count", strconv.Itoa(cfg.Retry.Count))
	writer.WriteField("retry_delay_seconds", strconv.Itoa(cfg.Retry.Delay))

	// Build metadata JSON. The deploy handler json.Unmarshal()s this into
	// FunctionDefinition first, then overlays the explicit form fields below.
	// Any field that has no explicit form-field equivalent (env vars, the
	// ws_* persistent settings) MUST live in this blob.
	metaObj := map[string]interface{}{}
	if len(cfg.Env) > 0 {
		metaObj["env_vars"] = cfg.Env
	}
	if cfg.WSPersistent {
		metaObj["ws_persistent"] = true
	}
	if cfg.WSIdleTimeoutSec > 0 {
		metaObj["ws_idle_timeout_sec"] = cfg.WSIdleTimeoutSec
	}
	if cfg.WSMaxFrameBytes > 0 {
		metaObj["ws_max_frame_bytes"] = cfg.WSMaxFrameBytes
	}
	if cfg.WSMaxInflightPerConn > 0 {
		metaObj["ws_max_inflight_per_conn"] = cfg.WSMaxInflightPerConn
	}
	if cfg.RawHTTPResponse {
		metaObj["raw_http_response"] = true
	}
	if cfg.WSAuth != "" {
		metaObj["ws_auth"] = cfg.WSAuth
	}
	rateLimit, err := rateLimitMetadata(cfg.RateLimit)
	if err != nil {
		return nil, fmt.Errorf("function.yaml: %w", err)
	}
	if rateLimit != nil {
		metaObj["rate_limit"] = rateLimit
	}
	if len(cfg.InvocableBy) > 0 {
		metaObj["invocable_by"] = cfg.InvocableBy
	}
	if cfg.OpenToPendingDevices {
		metaObj["open_to_pending_devices"] = true
	}
	if len(metaObj) > 0 {
		metadata, _ := json.Marshal(metaObj)
		writer.WriteField("metadata", string(metadata))
	}

	// Add WASM file
	part, err := writer.CreateFormFile("wasm", filepath.Base(wasmPath))
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, wasmFile); err != nil {
		return nil, fmt.Errorf("failed to write WASM data: %w", err)
	}
	writer.Close()

	resp, err := apiRequest("POST", "/v1/functions", body, writer.FormDataContentType())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, apiStatusError("deploy failed", resp.StatusCode, respBody)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return result, nil
}

// ResolveFunctionDir resolves and validates a function directory.
// If dir is empty, uses the current working directory.
func ResolveFunctionDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return "", fmt.Errorf("directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", absDir)
	}
	return absDir, nil
}
