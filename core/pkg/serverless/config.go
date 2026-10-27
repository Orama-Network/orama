package serverless

import (
	"fmt"
	"time"
)

// Config holds configuration for the serverless engine.
type Config struct {
	// Memory limits
	DefaultMemoryLimitMB int `yaml:"default_memory_limit_mb"`
	MaxMemoryLimitMB     int `yaml:"max_memory_limit_mb"`

	// Execution limits
	DefaultTimeoutSeconds int `yaml:"default_timeout_seconds"`
	MaxTimeoutSeconds     int `yaml:"max_timeout_seconds"`

	// Retry configuration
	DefaultRetryCount        int `yaml:"default_retry_count"`
	MaxRetryCount            int `yaml:"max_retry_count"`
	DefaultRetryDelaySeconds int `yaml:"default_retry_delay_seconds"`

	// Rate limiting (global)
	GlobalRateLimitPerMinute int `yaml:"global_rate_limit_per_minute"`

	// Background job configuration
	JobWorkers        int           `yaml:"job_workers"`
	JobPollInterval   time.Duration `yaml:"job_poll_interval"`
	JobMaxQueueSize   int           `yaml:"job_max_queue_size"`
	JobMaxPayloadSize int           `yaml:"job_max_payload_size"` // bytes

	// Scheduler configuration.
	//
	// CronPollInterval is the cadence at which the cron scheduler scans
	// `function_cron_triggers` for due rows. Lower = finer dispatch
	// granularity (useful for sub-second cron expressions like
	// `*/1 * * * * *` — the 6-field grammar accepted by ParseCron),
	// higher = less rqlite/CPU spend.
	//
	// Hard floor: MinCronPollInterval (rejected at Validate). Below the
	// floor the scheduler can't keep up — each tick costs ~1 rqlite
	// ListDue + N MarkRun writes, ~340-450ms per call on a
	// cross-region anchat-test-style cluster. Polling faster than the
	// per-tick cost queues ticks indefinitely and starves the namespace.
	//
	// Default: 1 minute. Set to 1s for typing/presence-style ephemeral
	// state prune workloads (bugboard #109).
	CronPollInterval  time.Duration `yaml:"cron_poll_interval"`
	TimerPollInterval time.Duration `yaml:"timer_poll_interval"`
	DBPollInterval    time.Duration `yaml:"db_poll_interval"`

	// WASM execution limits
	MaxConcurrentExecutions int `yaml:"max_concurrent_executions"` // Max concurrent WASM module instantiations

	// MaxConcurrentPerNamespace caps how many of MaxConcurrentExecutions one
	// namespace may hold at once. 0 means half of MaxConcurrentExecutions,
	// which keeps one tenant from starving the others on a gateway that runs
	// several namespaces. A namespace gateway runs one namespace, so the
	// gateway sets it to MaxConcurrentExecutions there: the half it would
	// otherwise reserve has no other tenant to protect (bug-963).
	MaxConcurrentPerNamespace int `yaml:"max_concurrent_per_namespace"`

	// WASM compilation cache
	ModuleCacheSize int  `yaml:"module_cache_size"` // Number of compiled modules to cache
	EnablePrewarm   bool `yaml:"enable_prewarm"`    // Compile the served namespace's functions ahead of their first call (prewarm.go)

	// CompilationCacheDir, when set, keeps wazero's compiled machine code on
	// disk under this directory. A module compiled once is then read back in
	// milliseconds instead of compiled again in seconds: after a restart, and
	// after the in-memory module cache evicted it. Empty keeps compiled code in
	// memory only. The directory must be private to the gateway: wazero maps
	// what it reads there as executable code.
	CompilationCacheDir string `yaml:"compilation_cache_dir"`

	// SlowInvokeThresholdMs is the wall-clock (ms) above which Execute emits the
	// per-phase "slow invocation" diagnostic (bugboard #24/#27). Default 5000.
	// Lower it (e.g. 750) to surface the sub-second cold-start floor that the
	// 5s default hides — async-dispatched stateless handlers pay a fresh
	// instantiate + TinyGo _start per call, which a count=0 read makes visible
	// as ~1s of execute time with ~0 module-load (compile is cached). See #27.
	SlowInvokeThresholdMs int `yaml:"slow_invoke_threshold_ms"`

	// Secrets encryption
	SecretsEncryptionKey string `yaml:"secrets_encryption_key"` // AES-256 key (32 bytes, hex-encoded)

	// NodeID names the node this engine runs on (its peer ID) in every
	// invocation record, so a record says which gateway ran it (bug-963).
	NodeID string `yaml:"-"`

	// Logging
	LogInvocations bool `yaml:"log_invocations"` // Log all invocations to database
	LogRetention   int  `yaml:"log_retention"`   // Days to retain logs
}

// MinCronPollInterval is the hard floor on CronPollInterval. Below
// this the cron scheduler can't keep up with itself — each tick costs
// at minimum one rqlite ListDue (a network round-trip + query), so
// polling much faster than the per-tick cost would queue ticks
// indefinitely and starve the namespace gateway. 100ms is generous
// (it allows ~10 ticks/sec) while still preventing the runaway
// configuration that would cripple the gateway.
//
// Operators wanting sub-second cron dispatch (e.g. typing/presence
// ephemeral state prune jobs per bugboard #109) should set 1s — this
// gives comfortable headroom over per-tick rqlite latency even on
// cross-region clusters and allows 6-field cron expressions like
// `*/1 * * * * *` to fire on every-second cadence.
const MinCronPollInterval = 100 * time.Millisecond

// defaultSlowInvokeThresholdMs is the default wall-clock (ms) above which the
// per-phase slow-invocation diagnostic fires. 5s keeps normal traffic quiet
// while still firing before the 30s WS ceiling; lower it on a cluster under
// investigation to surface sub-second cold-start floors.
const defaultSlowInvokeThresholdMs = 5000

// DefaultConfig returns a configuration with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		// Memory limits
		DefaultMemoryLimitMB: 64,
		MaxMemoryLimitMB:     256,

		// Execution limits
		DefaultTimeoutSeconds: 30,
		MaxTimeoutSeconds:     300, // 5 minutes max

		// Retry configuration
		DefaultRetryCount:        0,
		MaxRetryCount:            5,
		DefaultRetryDelaySeconds: 5,

		// Rate limiting
		GlobalRateLimitPerMinute: 250000, // 250k requests/minute globally

		// Background jobs
		JobWorkers:        4,
		JobPollInterval:   time.Second,
		JobMaxQueueSize:   10000,
		JobMaxPayloadSize: 1024 * 1024, // 1MB

		// Scheduler
		CronPollInterval:  time.Minute,
		TimerPollInterval: time.Second,
		DBPollInterval:    time.Second * 5,

		// WASM execution
		MaxConcurrentExecutions: 10,

		// WASM cache
		ModuleCacheSize:       100,
		EnablePrewarm:         true,
		SlowInvokeThresholdMs: defaultSlowInvokeThresholdMs,

		// Logging
		LogInvocations: true,
		LogRetention:   7, // 7 days
	}
}

// Validate checks the configuration for errors.
func (c *Config) Validate() []error {
	var errs []error

	if c.DefaultMemoryLimitMB <= 0 {
		errs = append(errs, &ConfigError{Field: "DefaultMemoryLimitMB", Message: "must be positive"})
	}
	if c.MaxMemoryLimitMB < c.DefaultMemoryLimitMB {
		errs = append(errs, &ConfigError{Field: "MaxMemoryLimitMB", Message: "must be >= DefaultMemoryLimitMB"})
	}
	if c.DefaultTimeoutSeconds <= 0 {
		errs = append(errs, &ConfigError{Field: "DefaultTimeoutSeconds", Message: "must be positive"})
	}
	if c.MaxTimeoutSeconds < c.DefaultTimeoutSeconds {
		errs = append(errs, &ConfigError{Field: "MaxTimeoutSeconds", Message: "must be >= DefaultTimeoutSeconds"})
	}
	if c.GlobalRateLimitPerMinute <= 0 {
		errs = append(errs, &ConfigError{Field: "GlobalRateLimitPerMinute", Message: "must be positive"})
	}
	if c.JobWorkers <= 0 {
		errs = append(errs, &ConfigError{Field: "JobWorkers", Message: "must be positive"})
	}
	if c.ModuleCacheSize <= 0 {
		errs = append(errs, &ConfigError{Field: "ModuleCacheSize", Message: "must be positive"})
	}
	if c.MaxConcurrentPerNamespace < 0 || (c.MaxConcurrentExecutions > 0 && c.MaxConcurrentPerNamespace > c.MaxConcurrentExecutions) {
		errs = append(errs, &ConfigError{Field: "MaxConcurrentPerNamespace", Message: "must be between 0 and MaxConcurrentExecutions"})
	}
	// CronPollInterval floor — see MinCronPollInterval doc. Zero means
	// "use the default" (ApplyDefaults handles it); a non-zero value
	// below the floor would silently let the operator paint themselves
	// into a runaway-scheduler corner.
	if c.CronPollInterval != 0 && c.CronPollInterval < MinCronPollInterval {
		errs = append(errs, &ConfigError{
			Field: "CronPollInterval",
			Message: fmt.Sprintf("must be >= %s (current=%s); see bugboard #109 — below this the scheduler can't keep up with per-tick rqlite cost and queues ticks indefinitely",
				MinCronPollInterval, c.CronPollInterval),
		})
	}

	return errs
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()

	if c.DefaultMemoryLimitMB == 0 {
		c.DefaultMemoryLimitMB = defaults.DefaultMemoryLimitMB
	}
	if c.MaxMemoryLimitMB == 0 {
		c.MaxMemoryLimitMB = defaults.MaxMemoryLimitMB
	}
	if c.DefaultTimeoutSeconds == 0 {
		c.DefaultTimeoutSeconds = defaults.DefaultTimeoutSeconds
	}
	if c.MaxTimeoutSeconds == 0 {
		c.MaxTimeoutSeconds = defaults.MaxTimeoutSeconds
	}
	if c.GlobalRateLimitPerMinute == 0 {
		c.GlobalRateLimitPerMinute = defaults.GlobalRateLimitPerMinute
	}
	if c.JobWorkers == 0 {
		c.JobWorkers = defaults.JobWorkers
	}
	if c.JobPollInterval == 0 {
		c.JobPollInterval = defaults.JobPollInterval
	}
	if c.JobMaxQueueSize == 0 {
		c.JobMaxQueueSize = defaults.JobMaxQueueSize
	}
	if c.JobMaxPayloadSize == 0 {
		c.JobMaxPayloadSize = defaults.JobMaxPayloadSize
	}
	if c.CronPollInterval == 0 {
		c.CronPollInterval = defaults.CronPollInterval
	}
	if c.TimerPollInterval == 0 {
		c.TimerPollInterval = defaults.TimerPollInterval
	}
	if c.DBPollInterval == 0 {
		c.DBPollInterval = defaults.DBPollInterval
	}
	if c.MaxConcurrentExecutions == 0 {
		c.MaxConcurrentExecutions = defaults.MaxConcurrentExecutions
	}
	if c.ModuleCacheSize == 0 {
		c.ModuleCacheSize = defaults.ModuleCacheSize
	}
	if c.SlowInvokeThresholdMs == 0 {
		c.SlowInvokeThresholdMs = defaults.SlowInvokeThresholdMs
	}
	if c.LogRetention == 0 {
		c.LogRetention = defaults.LogRetention
	}
}

// WithMemoryLimit returns a copy with the memory limit set.
func (c *Config) WithMemoryLimit(defaultMB, maxMB int) *Config {
	copy := *c
	copy.DefaultMemoryLimitMB = defaultMB
	copy.MaxMemoryLimitMB = maxMB
	return &copy
}

// WithTimeout returns a copy with the timeout set.
func (c *Config) WithTimeout(defaultSec, maxSec int) *Config {
	copy := *c
	copy.DefaultTimeoutSeconds = defaultSec
	copy.MaxTimeoutSeconds = maxSec
	return &copy
}

// WithRateLimit returns a copy with the rate limit set.
func (c *Config) WithRateLimit(perMinute int) *Config {
	copy := *c
	copy.GlobalRateLimitPerMinute = perMinute
	return &copy
}
