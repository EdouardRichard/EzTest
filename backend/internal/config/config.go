// Package config parses and validates the EzTest service configuration from
// environment variables. It contains no business logic.
package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// LLMSeed is the optional first-boot LLM configuration, used to seed the
// database when no LLM configuration exists yet.
type LLMSeed struct {
	BaseURL string
	Key     string
	Model   string
}

// Config is the process configuration. It is resolved once at startup.
type Config struct {
	Env             string
	HTTPAddr        string
	WebOrigin       string
	DatabaseURL     string
	TestDatabaseURL string
	AdminUsername   string
	AdminPassword   string
	SecretKeys      map[int][]byte
	SecretKeyActive int
	StorageDir      string
	// VersionAttachmentDir is the root of the P3 version attachment file store
	// (design spec §4.1, D30). Docker maps it to /data/version-attachments.
	VersionAttachmentDir string
	// ExportDir is the root of the P5 mindmap export file store (design spec
	// §6.10/D30). Only the file metadata lives in the database; the generated
	// Markdown/Mermaid/OPML documents are written below this directory. Docker
	// maps it to /data/exports.
	ExportDir string
	// ExecutorURL is the base URL of the standalone Node/Playwright executor
	// (spec §5.6). ExecutorToken is the bearer token the API process sends on
	// every executor call; it is never logged.
	ExecutorURL   string
	ExecutorToken string
	// EvidenceDir is the root of the P6 evidence file store (spec §5.7/D30).
	EvidenceDir string
	// ReportDir is the root of the P7 delivery report Markdown store (spec
	// §6.5/D30). Report rows keep only the root-relative path, sha256 and size.
	ReportDir string
	// DBResultMaxBytes and DBSQLMaxBytes bound one execution.db_assert query
	// (spec §16.1): the returned result bytes and the accepted SQL length.
	DBResultMaxBytes int
	DBSQLMaxBytes    int
	// AutonomyBatchesPerTick is the autonomous worker's per-tick batch budget
	// (spec §16.1: 1–20, default 4).
	AutonomyBatchesPerTick int
	// ExecMaxConcurrentCases, ExecMaxToolCalls, ExecCaseTimeoutMS,
	// ExecMaxCasesPerBatch, ExecWorkerIntervalMS, ExecEvidenceMaxBytes and
	// ExecEvidencePerCase are the P6 execution engine limits (spec §5.3, §5.5,
	// §5.7).
	ExecMaxConcurrentCases int
	ExecMaxToolCalls       int
	ExecCaseTimeoutMS      int
	ExecMaxCasesPerBatch   int
	ExecWorkerIntervalMS   int
	ExecEvidenceMaxBytes   int
	ExecEvidencePerCase    int
	// VisualBaselineMaxBytes caps the approved visual baseline artifact (design
	// spec §9.3, EZTEST_VISUAL_BASELINE_MAX_BYTES, default 5 MiB).
	VisualBaselineMaxBytes int
	// SnowflakeNodeID is this process's 10-bit node ID for application-side
	// primary key generation (D29). It must be unique per running instance.
	SnowflakeNodeID int64
	// OpenAPIPath locates the hand-maintained OpenAPI document served by
	// GET /api/openapi.yaml. It is relative to the process working directory by
	// default so the deployed image only needs to ship the file at that path.
	OpenAPIPath string
	// AgentFileRoot is the read-only root of the file.list/file.read tools. It
	// is empty by default, which disables the file tools (design spec §4.1).
	AgentFileRoot string
	// AgentHTTPAllowlist lists the hosts http.get may reach. An entry may carry
	// an explicit :port.
	AgentHTTPAllowlist []string
	// AgentToolTimeoutMS bounds every single tool invocation.
	AgentToolTimeoutMS int
	// AgentMaxConcurrentRuns bounds the process-wide number of running Agent
	// runs.
	AgentMaxConcurrentRuns int
	// P4 codeintel settings (design spec §4.1). The prompt version participates
	// in the analysis cache key and in fact versioning.
	CodeIntelMaxChangedFiles       int
	CodeIntelMaxFileBytes          int
	CodeIntelMaxIndexFiles         int
	CodeIntelAnalysisTimeoutMS     int
	CodeIntelMaxConcurrentAnalyses int
	CodeIntelPromptVersion         string
	CodeIntelMaxExcerptLines       int
	CodeIntelMaxDiffBytes          int

	// P8 rate limiting (design spec §10.1/§11.4): a per-user (authenticated) or
	// per-IP (unauthenticated) token bucket plus a per-user SSE concurrency cap.
	// RateLimitRPS=0 disables the ordinary token bucket.
	RateLimitRPS    int
	RateLimitBurst  int
	RateLimitSSEMax int
	// P8 evidence GC (design spec §10.2/§11.4). GCEnabled only controls whether
	// a non-dry-run purge is allowed; a dry-run is always available to admins.
	GCEnabled       bool
	GCRetentionDays int
	GCBatchKeep     int
	// P8 multi-instance leader lock (design spec §10.5). InstanceID identifies
	// this API process in the startup/shutdown and lock diagnostics. It
	// defaults to hostname+pid and is capped at 64 characters. LeaderLockKey is
	// the PostgreSQL advisory lock key every API instance shares.
	InstanceID    string
	LeaderLockKey int64

	EnableTestEndpoints bool
	LogLevel            slog.Level
	LLMSeed             LLMSeed
}

// Agent runtime defaults and bounds from design spec §4.1.
const (
	defaultAgentToolTimeoutMS     = 10000
	minAgentToolTimeoutMS         = 1000
	maxAgentToolTimeoutMS         = 60000
	defaultAgentMaxConcurrentRuns = 4
	minAgentMaxConcurrentRuns     = 1
	maxAgentMaxConcurrentRuns     = 32
)

// Snowflake node defaults and bounds from design spec §4.1 (D29).
const (
	defaultSnowflakeNodeID = 1
	minSnowflakeNodeID     = 0
	maxSnowflakeNodeID     = 1023
)

// P4 codeintel defaults and bounds from design spec §4.1.
const (
	defaultCodeIntelMaxChangedFiles       = 20
	minCodeIntelMaxChangedFiles           = 1
	maxCodeIntelMaxChangedFiles           = 200
	defaultCodeIntelMaxFileBytes          = 65536
	minCodeIntelMaxFileBytes              = 1024
	maxCodeIntelMaxFileBytes              = 1048576
	defaultCodeIntelMaxIndexFiles         = 5000
	minCodeIntelMaxIndexFiles             = 1
	maxCodeIntelMaxIndexFiles             = 50000
	defaultCodeIntelAnalysisTimeoutMS     = 180000
	minCodeIntelAnalysisTimeoutMS         = 30000
	maxCodeIntelAnalysisTimeoutMS         = 600000
	defaultCodeIntelMaxConcurrentAnalyses = 1
	minCodeIntelMaxConcurrentAnalyses     = 1
	maxCodeIntelMaxConcurrentAnalyses     = 4
	defaultCodeIntelPromptVersion         = "p4.1"
	defaultCodeIntelMaxExcerptLines       = 40
	minCodeIntelMaxExcerptLines           = 1
	maxCodeIntelMaxExcerptLines           = 200
	defaultCodeIntelMaxDiffBytes          = 262144
	minCodeIntelMaxDiffBytes              = 4096
	maxCodeIntelMaxDiffBytes              = 1048576
)

// codeIntelPromptVersionPattern is the prompt version alphabet from design
// spec §4.1: 1–32 lowercase letters, digits, dots and hyphens.
var codeIntelPromptVersionPattern = regexp.MustCompile(`^[a-z0-9.-]{1,32}$`)

// P6 execution defaults and bounds from design spec §5.3, §5.5 and §5.7.
const (
	defaultExecutorURL          = "http://127.0.0.1:13336"
	defaultEvidenceDir          = ".data/evidence"
	defaultExecMaxConcurrent    = 3
	minExecMaxConcurrent        = 1
	maxExecMaxConcurrent        = 8
	defaultExecMaxToolCalls     = 80
	minExecMaxToolCalls         = 1
	maxExecMaxToolCalls         = 1000
	defaultExecCaseTimeoutMS    = 900000
	minExecCaseTimeoutMS        = 1000
	maxExecCaseTimeoutMS        = 3600000
	defaultExecMaxCasesPerBatch = 200
	minExecMaxCasesPerBatch     = 1
	maxExecMaxCasesPerBatch     = 1000
	defaultExecWorkerIntervalMS = 2000
	minExecWorkerIntervalMS     = 100
	maxExecWorkerIntervalMS     = 60000
	defaultExecEvidenceMaxBytes = 10 << 20
	minExecEvidenceMaxBytes     = 1024
	maxExecEvidenceMaxBytes     = 100 << 20
	defaultExecEvidencePerCase  = 100
	minExecEvidencePerCase      = 1
	maxExecEvidencePerCase      = 1000
)

// P8 hardening defaults and bounds from design spec §10.1/§10.2/§11.4.
const (
	defaultRateLimitRPS    = 50
	minRateLimitRPS        = 0
	maxRateLimitRPS        = 1000
	defaultRateLimitBurst  = 100
	minRateLimitBurst      = 1
	maxRateLimitBurst      = 5000
	defaultRateLimitSSEMax = 10
	minRateLimitSSEMax     = 1
	maxRateLimitSSEMax     = 100
	defaultGCRetentionDays = 30
	minGCRetentionDays     = 1
	maxGCRetentionDays     = 3650
	defaultGCBatchKeep     = 3
	minGCBatchKeep         = 1
	maxGCBatchKeep         = 50
	// defaultVisualBaselineMaxBytes and its bounds are the design spec §9.3
	// approved-baseline cap (EZTEST_VISUAL_BASELINE_MAX_BYTES).
	defaultVisualBaselineMaxBytes = 5 << 20
	minVisualBaselineMaxBytes     = 64 << 10
	maxVisualBaselineMaxBytes     = 20 << 20
	// defaultLeaderLockKey is the stable PostgreSQL advisory lock key of the
	// multi-instance leader election (design spec §10.5/§11.4).
	defaultLeaderLockKey int64 = 1163411512
	// maxInstanceIDLength bounds EZTEST_INSTANCE_ID, characters not bytes.
	maxInstanceIDLength = 64
)

// P7 delivery defaults and bounds from design spec §16.1/D30.
const (
	defaultReportDir              = ".data/reports"
	defaultDBResultMaxBytes       = 1048576
	minDBResultMaxBytes           = 64 << 10
	maxDBResultMaxBytes           = 16 << 20
	defaultDBSQLMaxBytes          = 16384
	minDBSQLMaxBytes              = 1024
	maxDBSQLMaxBytes              = 64 << 10
	defaultAutonomyBatchesPerTick = 4
	minAutonomyBatchesPerTick     = 1
	maxAutonomyBatchesPerTick     = 20
)

// defaultAgentHTTPAllowlist is the localhost-only HTTP allowlist applied when
// EZTEST_AGENT_HTTP_ALLOWLIST is unset.
var defaultAgentHTTPAllowlist = []string{"127.0.0.1", "localhost"}

// Load reads the configuration through getenv and validates it. It returns an
// error that names the offending environment variable instead of panicking.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Env:             valueOr(getenv, "EZTEST_ENV", "dev"),
		HTTPAddr:        valueOr(getenv, "EZTEST_HTTP_ADDR", ":13333"),
		WebOrigin:       valueOr(getenv, "EZTEST_WEB_ORIGIN", "http://localhost:6868"),
		DatabaseURL:     strings.TrimSpace(getenv("EZTEST_DATABASE_URL")),
		TestDatabaseURL: strings.TrimSpace(getenv("EZTEST_TEST_DATABASE_URL")),
		AdminUsername:   strings.TrimSpace(getenv("EZTEST_ADMIN_USERNAME")),
		AdminPassword:   getenv("EZTEST_ADMIN_PASSWORD"),
		StorageDir:      valueOr(getenv, "EZTEST_STORAGE_DIR", ".data/objects"),
		VersionAttachmentDir: valueOr(getenv, "EZTEST_VERSION_ATTACHMENT_DIR",
			".data/version-attachments"),
		ExportDir:     valueOr(getenv, "EZTEST_EXPORT_DIR", ".data/exports"),
		ExecutorURL:   valueOr(getenv, "EZTEST_EXECUTOR_URL", defaultExecutorURL),
		ExecutorToken: strings.TrimSpace(getenv("EZTEST_EXECUTOR_TOKEN")),
		EvidenceDir:   valueOr(getenv, "EZTEST_EVIDENCE_DIR", defaultEvidenceDir),
		ReportDir:     valueOr(getenv, "EZTEST_REPORT_DIR", defaultReportDir),
		OpenAPIPath:   valueOr(getenv, "EZTEST_OPENAPI_PATH", "docs/openapi/api.yaml"),
		AgentFileRoot: strings.TrimSpace(getenv("EZTEST_AGENT_FILE_ROOT")),
		LLMSeed: LLMSeed{
			BaseURL: strings.TrimSpace(getenv("llm_base_url")),
			Key:     strings.TrimSpace(getenv("llm_key")),
			Model:   strings.TrimSpace(getenv("llm_model_name")),
		},
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: EZTEST_DATABASE_URL is required")
	}

	allowlist, err := parseAgentHTTPAllowlist(getenv("EZTEST_AGENT_HTTP_ALLOWLIST"))
	if err != nil {
		return Config{}, err
	}
	cfg.AgentHTTPAllowlist = allowlist

	toolTimeout, err := parseBoundedInt(getenv, "EZTEST_AGENT_TOOL_TIMEOUT_MS",
		defaultAgentToolTimeoutMS, minAgentToolTimeoutMS, maxAgentToolTimeoutMS)
	if err != nil {
		return Config{}, err
	}
	cfg.AgentToolTimeoutMS = toolTimeout

	maxConcurrentRuns, err := parseBoundedInt(getenv, "EZTEST_AGENT_MAX_CONCURRENT_RUNS",
		defaultAgentMaxConcurrentRuns, minAgentMaxConcurrentRuns, maxAgentMaxConcurrentRuns)
	if err != nil {
		return Config{}, err
	}
	cfg.AgentMaxConcurrentRuns = maxConcurrentRuns

	nodeID, err := parseBoundedInt(getenv, "EZTEST_SNOWFLAKE_NODE_ID",
		defaultSnowflakeNodeID, minSnowflakeNodeID, maxSnowflakeNodeID)
	if err != nil {
		return Config{}, err
	}
	cfg.SnowflakeNodeID = int64(nodeID)

	maxChangedFiles, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_MAX_CHANGED_FILES",
		defaultCodeIntelMaxChangedFiles, minCodeIntelMaxChangedFiles, maxCodeIntelMaxChangedFiles)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelMaxChangedFiles = maxChangedFiles

	maxFileBytes, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_MAX_FILE_BYTES",
		defaultCodeIntelMaxFileBytes, minCodeIntelMaxFileBytes, maxCodeIntelMaxFileBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelMaxFileBytes = maxFileBytes

	maxIndexFiles, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_MAX_INDEX_FILES",
		defaultCodeIntelMaxIndexFiles, minCodeIntelMaxIndexFiles, maxCodeIntelMaxIndexFiles)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelMaxIndexFiles = maxIndexFiles

	analysisTimeout, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS",
		defaultCodeIntelAnalysisTimeoutMS, minCodeIntelAnalysisTimeoutMS, maxCodeIntelAnalysisTimeoutMS)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelAnalysisTimeoutMS = analysisTimeout

	maxConcurrentAnalyses, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_MAX_CONCURRENT_ANALYSES",
		defaultCodeIntelMaxConcurrentAnalyses, minCodeIntelMaxConcurrentAnalyses, maxCodeIntelMaxConcurrentAnalyses)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelMaxConcurrentAnalyses = maxConcurrentAnalyses

	cfg.CodeIntelPromptVersion = valueOr(getenv, "EZTEST_CODEINTEL_PROMPT_VERSION",
		defaultCodeIntelPromptVersion)
	if !codeIntelPromptVersionPattern.MatchString(cfg.CodeIntelPromptVersion) {
		return Config{}, fmt.Errorf(
			"config: EZTEST_CODEINTEL_PROMPT_VERSION must match %s, got %q",
			codeIntelPromptVersionPattern, cfg.CodeIntelPromptVersion)
	}

	maxExcerptLines, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_MAX_EXCERPT_LINES",
		defaultCodeIntelMaxExcerptLines, minCodeIntelMaxExcerptLines, maxCodeIntelMaxExcerptLines)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelMaxExcerptLines = maxExcerptLines

	maxDiffBytes, err := parseBoundedInt(getenv, "EZTEST_CODEINTEL_MAX_DIFF_BYTES",
		defaultCodeIntelMaxDiffBytes, minCodeIntelMaxDiffBytes, maxCodeIntelMaxDiffBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.CodeIntelMaxDiffBytes = maxDiffBytes

	execMaxConcurrent, err := parseBoundedInt(getenv, "EZTEST_EXEC_MAX_CONCURRENT_CASES",
		defaultExecMaxConcurrent, minExecMaxConcurrent, maxExecMaxConcurrent)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecMaxConcurrentCases = execMaxConcurrent

	execMaxToolCalls, err := parseBoundedInt(getenv, "EZTEST_EXEC_MAX_TOOL_CALLS",
		defaultExecMaxToolCalls, minExecMaxToolCalls, maxExecMaxToolCalls)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecMaxToolCalls = execMaxToolCalls

	execCaseTimeoutMS, err := parseBoundedInt(getenv, "EZTEST_EXEC_CASE_TIMEOUT_MS",
		defaultExecCaseTimeoutMS, minExecCaseTimeoutMS, maxExecCaseTimeoutMS)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecCaseTimeoutMS = execCaseTimeoutMS

	execMaxCasesPerBatch, err := parseBoundedInt(getenv, "EZTEST_EXEC_MAX_CASES_PER_BATCH",
		defaultExecMaxCasesPerBatch, minExecMaxCasesPerBatch, maxExecMaxCasesPerBatch)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecMaxCasesPerBatch = execMaxCasesPerBatch

	execWorkerIntervalMS, err := parseBoundedInt(getenv, "EZTEST_EXEC_WORKER_INTERVAL_MS",
		defaultExecWorkerIntervalMS, minExecWorkerIntervalMS, maxExecWorkerIntervalMS)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecWorkerIntervalMS = execWorkerIntervalMS

	execEvidenceMaxBytes, err := parseBoundedInt(getenv, "EZTEST_EXEC_EVIDENCE_MAX_BYTES",
		defaultExecEvidenceMaxBytes, minExecEvidenceMaxBytes, maxExecEvidenceMaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecEvidenceMaxBytes = execEvidenceMaxBytes

	execEvidencePerCase, err := parseBoundedInt(getenv, "EZTEST_EXEC_EVIDENCE_PER_CASE",
		defaultExecEvidencePerCase, minExecEvidencePerCase, maxExecEvidencePerCase)
	if err != nil {
		return Config{}, err
	}
	cfg.ExecEvidencePerCase = execEvidencePerCase

	visualBaselineMaxBytes, err := parseBoundedInt(getenv, "EZTEST_VISUAL_BASELINE_MAX_BYTES",
		defaultVisualBaselineMaxBytes, minVisualBaselineMaxBytes, maxVisualBaselineMaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.VisualBaselineMaxBytes = visualBaselineMaxBytes

	dbResultMaxBytes, err := parseBoundedInt(getenv, "EZTEST_DB_RESULT_MAX_BYTES",
		defaultDBResultMaxBytes, minDBResultMaxBytes, maxDBResultMaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.DBResultMaxBytes = dbResultMaxBytes

	dbSQLMaxBytes, err := parseBoundedInt(getenv, "EZTEST_DB_SQL_MAX_BYTES",
		defaultDBSQLMaxBytes, minDBSQLMaxBytes, maxDBSQLMaxBytes)
	if err != nil {
		return Config{}, err
	}
	cfg.DBSQLMaxBytes = dbSQLMaxBytes

	autonomyBatchesPerTick, err := parseBoundedInt(getenv, "EZTEST_AUTONOMY_BATCHES_PER_TICK",
		defaultAutonomyBatchesPerTick, minAutonomyBatchesPerTick, maxAutonomyBatchesPerTick)
	if err != nil {
		return Config{}, err
	}
	cfg.AutonomyBatchesPerTick = autonomyBatchesPerTick

	rateLimitRPS, err := parseBoundedInt(getenv, "EZTEST_RATE_LIMIT_RPS",
		defaultRateLimitRPS, minRateLimitRPS, maxRateLimitRPS)
	if err != nil {
		return Config{}, err
	}
	cfg.RateLimitRPS = rateLimitRPS

	rateLimitBurst, err := parseBoundedInt(getenv, "EZTEST_RATE_LIMIT_BURST",
		defaultRateLimitBurst, minRateLimitBurst, maxRateLimitBurst)
	if err != nil {
		return Config{}, err
	}
	cfg.RateLimitBurst = rateLimitBurst

	rateLimitSSEMax, err := parseBoundedInt(getenv, "EZTEST_RATE_LIMIT_SSE_MAX",
		defaultRateLimitSSEMax, minRateLimitSSEMax, maxRateLimitSSEMax)
	if err != nil {
		return Config{}, err
	}
	cfg.RateLimitSSEMax = rateLimitSSEMax

	rawGCEnabled := strings.TrimSpace(getenv("EZTEST_GC_ENABLED"))
	if rawGCEnabled != "" {
		enabled, err := strconv.ParseBool(rawGCEnabled)
		if err != nil {
			return Config{}, fmt.Errorf("config: EZTEST_GC_ENABLED must be a boolean, got %q", rawGCEnabled)
		}
		cfg.GCEnabled = enabled
	}

	gcRetentionDays, err := parseBoundedInt(getenv, "EZTEST_GC_RETENTION_DAYS",
		defaultGCRetentionDays, minGCRetentionDays, maxGCRetentionDays)
	if err != nil {
		return Config{}, err
	}
	cfg.GCRetentionDays = gcRetentionDays

	gcBatchKeep, err := parseBoundedInt(getenv, "EZTEST_GC_BATCH_KEEP",
		defaultGCBatchKeep, minGCBatchKeep, maxGCBatchKeep)
	if err != nil {
		return Config{}, err
	}
	cfg.GCBatchKeep = gcBatchKeep

	cfg.InstanceID = valueOr(getenv, "EZTEST_INSTANCE_ID", defaultInstanceID())
	if count := utf8.RuneCountInString(cfg.InstanceID); count > maxInstanceIDLength {
		return Config{}, fmt.Errorf(
			"config: EZTEST_INSTANCE_ID must be at most %d characters, got %d",
			maxInstanceIDLength, count)
	}
	leaderLockKey, err := parseLeaderLockKey(getenv("EZTEST_LEADER_LOCK_KEY"))
	if err != nil {
		return Config{}, err
	}
	cfg.LeaderLockKey = leaderLockKey

	keys, err := parseSecretKeys(getenv("EZTEST_SECRET_KEYS"))
	if err != nil {
		return Config{}, err
	}
	cfg.SecretKeys = keys

	activeRaw := strings.TrimSpace(getenv("EZTEST_SECRET_KEY_ACTIVE"))
	if activeRaw == "" {
		return Config{}, fmt.Errorf("config: EZTEST_SECRET_KEY_ACTIVE is required")
	}
	active, err := strconv.Atoi(activeRaw)
	if err != nil || active <= 0 {
		return Config{}, fmt.Errorf("config: EZTEST_SECRET_KEY_ACTIVE must be a positive integer, got %q", activeRaw)
	}
	if _, ok := keys[active]; !ok {
		return Config{}, fmt.Errorf("config: EZTEST_SECRET_KEY_ACTIVE %d is not present in EZTEST_SECRET_KEYS", active)
	}
	cfg.SecretKeyActive = active

	rawTestEndpoints := strings.TrimSpace(getenv("EZTEST_ENABLE_TEST_ENDPOINTS"))
	if rawTestEndpoints == "" {
		cfg.EnableTestEndpoints = cfg.Env != "prod"
	} else {
		enabled, err := strconv.ParseBool(rawTestEndpoints)
		if err != nil {
			return Config{}, fmt.Errorf("config: EZTEST_ENABLE_TEST_ENDPOINTS must be a boolean, got %q", rawTestEndpoints)
		}
		cfg.EnableTestEndpoints = enabled
	}

	level, err := parseLogLevel(getenv("EZTEST_LOG_LEVEL"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	return cfg, nil
}

// valueOr returns the trimmed value of key, or def when it is unset or empty.
func valueOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// defaultInstanceID returns the hostname+pid identity used when
// EZTEST_INSTANCE_ID is not configured (design spec §10.5).
func defaultInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	return instanceIDFor(strings.TrimSpace(hostname), os.Getpid())
}

// instanceIDFor builds a bounded hostname+pid identity. The pid suffix that
// distinguishes co-located instances is always preserved; an over-long
// hostname is truncated so the result never exceeds maxInstanceIDLength.
func instanceIDFor(hostname string, pid int) string {
	suffix := fmt.Sprintf("-%d", pid)
	maxHost := maxInstanceIDLength - utf8.RuneCountInString(suffix)
	if maxHost < 0 {
		maxHost = 0
	}
	runes := []rune(hostname)
	if len(runes) > maxHost {
		hostname = string(runes[:maxHost])
	}
	return hostname + suffix
}

// parseLeaderLockKey parses EZTEST_LEADER_LOCK_KEY as a signed 64-bit
// PostgreSQL advisory lock key. An unset value selects the default key.
func parseLeaderLockKey(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultLeaderLockKey, nil
	}
	key, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: EZTEST_LEADER_LOCK_KEY must be a 64-bit integer, got %q", raw)
	}
	return key, nil
}

// parseAgentHTTPAllowlist parses the comma-separated http.get host allowlist.
// An unset variable selects the localhost-only default; a value that contains
// no host at all is rejected so a typo cannot silently disable every host (the
// tool itself fails closed on an empty list).
func parseAgentHTTPAllowlist(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string(nil), defaultAgentHTTPAllowlist...), nil
	}
	hosts := make([]string, 0, strings.Count(raw, ",")+1)
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			hosts = append(hosts, entry)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("config: EZTEST_AGENT_HTTP_ALLOWLIST must contain at least one host, got %q", raw)
	}
	return hosts, nil
}

// parseBoundedInt parses one bounded integer setting. An empty value selects
// def; anything else must be a number inside [min, max]. The returned error
// names the offending environment variable.
func parseBoundedInt(getenv func(string) string, key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("config: %s must be an integer between %d and %d, got %q", key, min, max, raw)
	}
	return value, nil
}

// parseSecretKeys decodes EZTEST_SECRET_KEYS: a JSON object mapping key
// version to a base64-encoded 32-byte AES-256 key.
func parseSecretKeys(raw string) (map[int][]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS is required")
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS must be a JSON object mapping key version to base64 key: %w", err)
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS must contain at least one key")
	}
	keys := make(map[int][]byte, len(encoded))
	for version, value := range encoded {
		v, err := strconv.Atoi(strings.TrimSpace(version))
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS has invalid key version %q", version)
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS key %q is not valid base64: %w", version, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS key %q must decode to 32 bytes, got %d", version, len(key))
		}
		keys[v] = key
	}
	return keys, nil
}

// parseLogLevel maps EZTEST_LOG_LEVEL to a slog level. Empty means info.
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: EZTEST_LOG_LEVEL must be one of debug, info, warn, error, got %q", raw)
	}
}
