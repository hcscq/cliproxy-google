// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// Config represents the application's configuration, loaded from a YAML file.
type Config struct {
	// Models selects optional catalog sources independently for each catalog.
	Models ModelCatalogs `yaml:"models" json:"models"`

	SDKConfig `yaml:",inline"`
	// Host is the network host/interface on which the API server will bind.
	// Default is empty ("") to bind all interfaces (IPv4 + IPv6). Use "127.0.0.1" or "localhost" for local-only access.
	Host string `yaml:"host" json:"-"`
	// Port is the network port on which the API server will listen.
	Port int `yaml:"port" json:"-"`

	// GitHubToken is the global token for GitHub requests, taking precedence over GITHUB_TOKEN.
	GitHubToken string `yaml:"github-token" json:"-"`

	// TrustedProxies lists the IPs or CIDRs allowed to provide forwarded client IP headers.
	// The server applies this list at startup; changing it requires a restart.
	TrustedProxies []string `yaml:"trusted-proxies" json:"trusted-proxies"`

	// TLS config controls HTTPS server settings.
	TLS TLSConfig `yaml:"tls" json:"tls"`

	// Home config is runtime-only and is populated from -home-jwt.
	Home HomeConfig `yaml:"-" json:"-"`

	// CredentialConcurrency contains Home-authoritative credential lifecycle settings.
	CredentialConcurrency CredentialConcurrencyConfig `yaml:"credential-concurrency" json:"credential-concurrency"`

	// CredentialInFlight configures credential observation snapshots.
	CredentialInFlight CredentialInFlightConfig `yaml:"credential-in-flight" json:"credential-in-flight"`

	// RemoteManagement nests management-related options under 'remote-management'.
	RemoteManagement RemoteManagement `yaml:"remote-management" json:"-"`

	// Plugins configures dynamic plugin discovery and per-plugin settings.
	Plugins PluginsConfig `yaml:"plugins" json:"plugins"`

	// AuthDir is the directory where authentication token files are stored.
	AuthDir string `yaml:"auth-dir" json:"-"`

	// Debug enables or disables debug-level logging and other debug features.
	Debug bool `yaml:"debug" json:"debug"`

	// Pprof config controls the optional pprof HTTP debug server.
	Pprof PprofConfig `yaml:"pprof" json:"pprof"`

	// Discovery configures local network mDNS / DNS-SD service advertising.
	Discovery DiscoveryConfig `yaml:"discovery" json:"discovery"`

	// CommercialMode disables high-overhead request logging and HTTP middleware features to minimize per-request memory usage.
	CommercialMode bool `yaml:"commercial-mode" json:"commercial-mode"`

	// LoggingToFile controls whether application logs are written to rotating files or stdout.
	LoggingToFile bool `yaml:"logging-to-file" json:"logging-to-file"`

	// LogsMaxTotalSizeMB limits the total size (in MB) of log files under the logs directory.
	// When exceeded, the oldest log files are deleted until within the limit. Set to 0 to disable.
	LogsMaxTotalSizeMB int `yaml:"logs-max-total-size-mb" json:"logs-max-total-size-mb"`

	// ErrorLogsMaxFiles limits the number of error log files retained when request logging is disabled.
	// When exceeded, the oldest error log files are deleted. Default is 10. Set to 0 to disable cleanup.
	ErrorLogsMaxFiles int `yaml:"error-logs-max-files" json:"error-logs-max-files"`

	// UsageStatisticsEnabled toggles in-memory usage aggregation; when false, usage data is discarded.
	UsageStatisticsEnabled bool `yaml:"usage-statistics-enabled" json:"usage-statistics-enabled"`

	// RedisUsageQueueRetentionSeconds controls how long usage queue items are retained
	// in memory for Management API consumers.
	// Default: 60. Max: 3600.
	RedisUsageQueueRetentionSeconds int `yaml:"redis-usage-queue-retention-seconds" json:"redis-usage-queue-retention-seconds"`

	// DisableCooling disables auth/model cooldown scheduling when true unless a credential or provider overrides it.
	DisableCooling bool `yaml:"disable-cooling" json:"disable-cooling"`

	// SaveCooldownStatus persists runtime cooldown status next to auth files when true.
	SaveCooldownStatus bool `yaml:"save-cooldown-status" json:"save-cooldown-status"`

	// TransientErrorCooldownSeconds controls cooldowns for transient upstream errors (408/500/502/503/504/520-526).
	// 0 keeps the legacy default cooldown. Negative values disable these cooldowns.
	TransientErrorCooldownSeconds int `yaml:"transient-error-cooldown-seconds" json:"transient-error-cooldown-seconds"`

	// AuthAutoRefreshWorkers overrides the size of the core auth auto-refresh and manual refresh-all worker pool.
	// When <= 0, the default worker count is used.
	AuthAutoRefreshWorkers int `yaml:"auth-auto-refresh-workers" json:"auth-auto-refresh-workers"`

	// RequestRetry defines the number of additional credential retry rounds after
	// the first round has exhausted its eligible credentials.
	RequestRetry int `yaml:"request-retry" json:"request-retry"`
	// MaxRetryCredentials defines the maximum number of different credentials to
	// try in each credential retry round.
	// Set to 0 or a negative value to keep trying all available credentials (legacy behavior).
	MaxRetryCredentials int `yaml:"max-retry-credentials" json:"max-retry-credentials"`
	// MaxRequestCredentials caps the distinct credentials one request switches
	// through (same model, all retry rounds); the last upstream error is then
	// returned. 0 = default (5); negative = unlimited.
	MaxRequestCredentials int `yaml:"max-request-credentials" json:"max-request-credentials"`
	// MaxRetryDuration bounds, in seconds, how long one request keeps switching
	// credentials or waiting between retry rounds before the last upstream error
	// is returned (keeps streaming clients under ~100s to first byte).
	// 0 = default (90); negative = unlimited.
	MaxRetryDuration int `yaml:"max-retry-duration" json:"max-retry-duration"`
	// FirstByteDeadline bounds, in seconds, how long a model request may run
	// before the client gets a first byte; past it the proxy answers a
	// retryable 503 so the gateway fails over. 0 = default (120); <0 = off.
	FirstByteDeadline int `yaml:"first-byte-deadline,omitempty" json:"first-byte-deadline,omitempty"`
	// FirstByteDeadlineNonStream is the same for non-streaming requests
	// (first byte only after full generation). 0 = default (240); <0 = off.
	FirstByteDeadlineNonStream int `yaml:"first-byte-deadline-nonstream,omitempty" json:"first-byte-deadline-nonstream,omitempty"`
	// HomeKVTimeoutMs bounds best-effort Home KV calls on the request path
	// (reasoning replay read/write); slow or failing calls are skipped.
	// 0 = default (500); <0 = use the connection timeout.
	HomeKVTimeoutMs int `yaml:"home-kv-timeout-ms,omitempty" json:"home-kv-timeout-ms,omitempty"`
	// MaxRetryInterval defines the maximum positive cooldown wait, in seconds,
	// allowed before starting another credential retry round. A non-positive value
	// forbids positive cooldown waits; it does not disable same-round credential
	// failover or immediate additional rounds allowed by RequestRetry.
	MaxRetryInterval int `yaml:"max-retry-interval" json:"max-retry-interval"`

	// CapacityRetry retries requests that failed because the upstream had no
	// capacity for the requested model (503 UNAVAILABLE / MODEL_CAPACITY_EXHAUSTED,
	// capacity-flavoured 429). The requested model is never changed.
	CapacityRetry CapacityRetryConfig `yaml:"capacity-retry,omitempty" json:"capacity-retry,omitempty"`

	// HonorMaxTokens passes client output limits (OpenAI max_tokens /
	// max_completion_tokens, Claude max_tokens, Gemini maxOutputTokens) to Gemini
	// models on Antigravity and AI Studio instead of stripping them. nil = true.
	// When thinking is enabled with a fixed budget >= the limit, the budget is
	// lowered so some of the limit remains for the answer.
	HonorMaxTokens *bool `yaml:"honor-max-tokens,omitempty" json:"honor-max-tokens,omitempty"`

	// StreamThinkingEmulation makes streaming requests that ask for thinking
	// on Antigravity Gemini models call the non-stream upstream endpoint and
	// replay the full response as a single SSE chunk. Upstream gemini-3.x
	// streams often omit thought summaries (they are always present in the
	// non-stream response), so this trades time-to-first-token for complete
	// reasoning text. Default off.
	StreamThinkingEmulation bool `yaml:"stream-thinking-emulation,omitempty" json:"stream-thinking-emulation,omitempty"`

	// StickySessions pins repeated / continued requests to the credential that
	// served them before, so per-credential implicit prompt caching hits.
	StickySessions StickySessionsConfig `yaml:"sticky-sessions,omitempty" json:"sticky-sessions,omitempty"`

	// AntigravityCatalogRefresh is how often Home re-fetches each Antigravity
	// credential's upstream model catalog (Go duration, default 15m; "0" or
	// "off" disables the timer, event-driven and manual refreshes still work).
	// Only the Home leader fetches; the result is shared through the database.
	AntigravityCatalogRefresh string `yaml:"antigravity-catalog-refresh,omitempty" json:"antigravity-catalog-refresh,omitempty"`

	// QuotaExceeded defines the behavior when a quota is exceeded.
	QuotaExceeded QuotaExceeded `yaml:"quota-exceeded" json:"quota-exceeded"`

	// Routing controls credential selection behavior.
	Routing RoutingConfig `yaml:"routing" json:"routing"`

	// WebsocketAuth enables or disables authentication for the WebSocket API.
	WebsocketAuth bool `yaml:"ws-auth" json:"ws-auth"`

	// AntigravitySignatureCacheEnabled controls whether signature cache validation is enabled for thinking blocks.
	// When true (default), cached signatures are preferred and validated.
	// When false, client signatures are used directly after normalization (bypass mode).
	AntigravitySignatureCacheEnabled *bool `yaml:"antigravity-signature-cache-enabled,omitempty" json:"antigravity-signature-cache-enabled,omitempty"`

	AntigravitySignatureBypassStrict *bool `yaml:"antigravity-signature-bypass-strict,omitempty" json:"antigravity-signature-bypass-strict,omitempty"`

	// Antigravity configures provider-wide Antigravity request behavior.
	Antigravity AntigravityConfig `yaml:"antigravity" json:"antigravity"`

	// Devin configures provider-wide Devin request behavior.
	Devin DevinConfig `yaml:"devin" json:"devin"`

	// GeminiKey defines Gemini API key configurations with optional routing overrides.
	GeminiKey []GeminiKey `yaml:"gemini-api-key" json:"gemini-api-key"`

	// InteractionsKey defines native Google Interactions API key configurations.
	InteractionsKey []GeminiKey `yaml:"interactions-api-key" json:"interactions-api-key"`

	// Codex defines a list of Codex API key configurations as specified in the YAML configuration file.
	CodexKey []CodexKey `yaml:"codex-api-key" json:"codex-api-key"`

	// XAIKey defines xAI API key configurations using the same structure as Codex API keys.
	XAIKey []XAIKey `yaml:"xai-api-key" json:"xai-api-key"`

	// MetaKey defines Meta API key configurations using the same structure as Codex API keys.
	MetaKey []MetaKey `yaml:"meta-api-key" json:"meta-api-key"`

	// XAI configures provider-wide xAI request behavior.
	XAI XAIConfig `yaml:"xai" json:"xai"`

	// Codex configures provider-wide Codex request behavior.
	Codex CodexConfig `yaml:"codex" json:"codex"`

	// CodexHeaderDefaults configures fallback headers for Codex OAuth model requests.
	// These are used only when the client does not send its own headers.
	CodexHeaderDefaults CodexHeaderDefaults `yaml:"codex-header-defaults" json:"codex-header-defaults"`

	// Claude configures provider-wide Claude request behavior.
	Claude ClaudeConfig `yaml:"claude" json:"claude"`

	// ClaudeKey defines a list of Claude API key configurations as specified in the YAML configuration file.
	ClaudeKey []ClaudeKey `yaml:"claude-api-key" json:"claude-api-key"`

	// ClaudeHeaderDefaults configures default header values for Claude API requests.
	// These are used as fallbacks when the client does not send its own headers.
	ClaudeHeaderDefaults ClaudeHeaderDefaults `yaml:"claude-header-defaults" json:"claude-header-defaults"`

	// DisableClaudeCloakMode globally disables Claude request cloaking when true.
	// Cloaking disguises requests as the official Claude Code CLI and replaces the
	// system prompt. When true, every Claude credential defaults to no cloaking
	// ("never"); a specific credential can still re-enable or override it via its own
	// cloak settings (the per claude-api-key "cloak" block, or a "cloak_mode" value in
	// the auth/OAuth token file). Default false preserves the per-client "auto" behavior.
	DisableClaudeCloakMode bool `yaml:"disable-claude-cloak-mode" json:"disable-claude-cloak-mode"`

	// OpenAICompatibility defines OpenAI API compatibility configurations for external providers.
	OpenAICompatibility []OpenAICompatibility `yaml:"openai-compatibility" json:"openai-compatibility"`

	// VertexCompatAPIKey defines Vertex AI-compatible API key configurations for third-party providers.
	// Used for services that use Vertex AI-style paths but with simple API key authentication.
	VertexCompatAPIKey []VertexCompatKey `yaml:"vertex-api-key" json:"vertex-api-key"`

	// OAuthExcludedModels defines per-provider global model exclusions applied to OAuth/file-backed auth entries.
	OAuthExcludedModels map[string][]string `yaml:"oauth-excluded-models,omitempty" json:"oauth-excluded-models,omitempty"`

	// OAuthModelAlias defines global model name aliases for OAuth/file-backed auth channels.
	// These aliases affect both model listing and model routing for supported channels:
	// vertex, aistudio, antigravity, claude, codex, kimi, xai, meta.
	//
	// NOTE: This does not apply to existing per-credential model alias features under:
	// gemini-api-key, interactions-api-key, codex-api-key, xai-api-key, meta-api-key, claude-api-key, openai-compatibility, and vertex-api-key.
	OAuthModelAlias map[string][]OAuthModelAlias `yaml:"oauth-model-alias,omitempty" json:"oauth-model-alias,omitempty"`

	// OAuthRequestScopedErrors defines per-provider request-scoped error rules applied to OAuth/file-backed auth entries.
	// Supported channels include: vertex, aistudio, antigravity, claude, codex, kimi, xai, meta, and OAuth plugin provider keys.
	//
	// NOTE: This applies only to OAuth credentials and does not affect per-credential request-scoped-errors under *-api-key.
	OAuthRequestScopedErrors map[string][]RequestScopedErrorRule `yaml:"oauth-request-scoped-errors,omitempty" json:"oauth-request-scoped-errors,omitempty"`

	// OAuthSettings defines per-channel model settings (such as max-context-length) applied to OAuth/file-backed auth entries.
	OAuthSettings map[string][]OAuthModelSetting `yaml:"oauth-settings,omitempty" json:"oauth-settings,omitempty"`

	// Payload defines default and override rules for provider payload parameters.
	Payload PayloadConfig `yaml:"payload" json:"payload"`
}

// CapacityRetryConfig configures same-model retries on upstream capacity errors.
// Every retry first tries the other available credentials (a normal credential
// round), then backs off with jitter before the next round.
type CapacityRetryConfig struct {
	// Enabled toggles capacity retries; nil means enabled.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// MaxAttempts is the number of extra rounds after the first failure (default 3).
	MaxAttempts int `yaml:"max-attempts,omitempty" json:"max-attempts,omitempty"`
	// BaseDelay is the first backoff delay (Go duration, default 1s); it doubles per attempt.
	BaseDelay string `yaml:"base-delay,omitempty" json:"base-delay,omitempty"`
	// MaxDelay caps a single backoff delay (default 8s). An upstream RetryInfo
	// retryDelay is honoured up to this cap.
	MaxDelay string `yaml:"max-delay,omitempty" json:"max-delay,omitempty"`
}
