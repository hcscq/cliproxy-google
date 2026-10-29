package dailybilling

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigEnvOverridesFileWithoutLeakingPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.yaml")
	content := []byte(`
pgsql:
  host: "127.0.0.1"
  user: "cliproxy"
  password: "home-secret"
  database: "cliproxy_home"
new-api:
  postgres-uri: "postgresql://fileuser:file-secret@files.example:5432/filedb?sslmode=disable"
  quota-per-unit: 500000
  group-ratio: 0.3
  channel-id: 59
`)
	if errWrite := os.WriteFile(path, content, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	t.Setenv("NEW_API_DATABASE_DSN", "")
	t.Setenv("NEW_API_DB_HOST", "env-host")
	t.Setenv("NEW_API_DB_PORT", "5433")
	t.Setenv("NEW_API_DB_USER", "env-user")
	t.Setenv("NEW_API_DB_PASSWORD", "env-secret")
	t.Setenv("NEW_API_DB_NAME", "envdb")
	t.Setenv("NEW_API_DB_SSLMODE", "require")
	t.Setenv("NEW_API_QUOTA_PER_UNIT", "250000")
	t.Setenv("NEW_API_GROUP_RATIO", "0.5")
	t.Setenv("NEW_API_CHANNEL_ID", "7")

	cfg := LoadConfig(path)
	if cfg.PostgresURI != "" {
		t.Fatal("host env should replace the file URI")
	}
	if cfg.Host != "env-host" || cfg.Port != 5433 || cfg.User != "env-user" || cfg.Database != "envdb" || cfg.SSLMode != "require" {
		t.Fatalf("connection = %+v", cfg.SafeString())
	}
	if cfg.Password != "env-secret" {
		t.Fatal("password was not taken from the environment")
	}
	if cfg.QuotaPerUnit != 250000 || cfg.GroupRatio != 0.5 || cfg.ChannelID != 7 {
		t.Fatalf("constants = unit %v ratio %v channel %d", cfg.QuotaPerUnit, cfg.GroupRatio, cfg.ChannelID)
	}
	safe := cfg.SafeString()
	if strings.Contains(safe, "env-secret") || strings.Contains(safe, "file-secret") || strings.Contains(safe, "home-secret") {
		t.Fatalf("safe string leaked a password: %s", safe)
	}
	dsn, errDSN := cfg.connectionString()
	if errDSN != nil {
		t.Fatal(errDSN)
	}
	if !strings.Contains(dsn, "default_transaction_read_only=on") {
		t.Fatalf("dsn missing read-only session: %s", redactSecrets(dsn, cfg.Password, ""))
	}
	leaky := "dial " + dsn + " password authentication failed for user env-secret"
	redacted := redactSecrets(leaky, cfg.Password, dsn)
	if strings.Contains(redacted, "env-secret") || strings.Contains(redacted, "env-host") {
		t.Fatalf("redaction failed: %s", redacted)
	}
}

func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	clearNewAPIEnv(t)
	cfg := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
	if cfg.Configured() {
		t.Fatalf("missing file configured: %s", cfg.SafeString())
	}
	if cfg.QuotaPerUnit != DefaultQuotaPerUnit || cfg.GroupRatio != DefaultGroupRatio || cfg.ChannelID != DefaultChannelID {
		t.Fatalf("defaults = unit %v ratio %v channel %d", cfg.QuotaPerUnit, cfg.GroupRatio, cfg.ChannelID)
	}
	if cfg.ModelPrefix != DefaultModelPrefix || cfg.ProTier != DefaultProTier || cfg.UltraTier != DefaultUltraTier {
		t.Fatalf("model/tier defaults = %s %s %s", cfg.ModelPrefix, cfg.ProTier, cfg.UltraTier)
	}
	if cfg.CreatedAt != DefaultCreatedAt || cfg.SuccessType != DefaultSuccessType {
		t.Fatalf("created-at %s type %d", cfg.CreatedAt, cfg.SuccessType)
	}
}

func TestURIKeepsReadOnlyOptionWithoutEmbeddingPasswordInSafeString(t *testing.T) {
	t.Parallel()
	cfg := Config{PostgresURI: "postgresql://reader:s3cret@db.internal:5432/newapi?sslmode=require"}.normalized()
	if !cfg.Configured() {
		t.Fatal("uri should be configured")
	}
	if strings.Contains(cfg.SafeString(), "s3cret") || strings.Contains(cfg.SafeString(), "reader") {
		t.Fatalf("safe string = %s", cfg.SafeString())
	}
	if !strings.Contains(cfg.SafeString(), "db.internal") {
		t.Fatalf("safe string = %s", cfg.SafeString())
	}
	dsn, errDSN := cfg.connectionString()
	if errDSN != nil {
		t.Fatal(errDSN)
	}
	if strings.Contains(dsn, "s3cret") == false || strings.Contains(dsn, "default_transaction_read_only") == false {
		t.Fatal("read-only uri was not built")
	}
}

func clearNewAPIEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"NEW_API_DATABASE_DSN",
		"NEW_API_DB_HOST",
		"NEW_API_DB_PORT",
		"NEW_API_DB_USER",
		"NEW_API_DB_PASSWORD",
		"NEW_API_DB_NAME",
		"NEW_API_DB_SSLMODE",
		"NEW_API_QUOTA_PER_UNIT",
		"NEW_API_GROUP_RATIO",
		"NEW_API_CHANNEL_ID",
		"NEW_API_BILLING_TIMEZONE",
		"NEW_API_MODEL_PREFIX",
		"NEW_API_MODELS",
		"NEW_API_MODEL_EXCLUDE",
		"NEW_API_CREATED_AT",
		"NEW_API_PRO_TIER",
		"NEW_API_ULTRA_TIER",
		"NEW_API_SUCCESS_TYPE",
	} {
		t.Setenv(key, "")
	}
}
