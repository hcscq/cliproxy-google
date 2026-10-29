package dailybilling

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the read-only new-api Postgres connection and the billing constants.
// Password is never included in SafeString.
type Config struct {
	PostgresURI string
	Host        string
	Port        int
	User        string
	Password    string
	Database    string
	SSLMode     string

	QuotaPerUnit float64
	GroupRatio   float64
	ChannelID    int
	Timezone     string
	ModelPrefix  string
	Models       []string
	ModelExclude string
	CreatedAt    string
	ProTier      string
	UltraTier    string
	SuccessType  int

	channelExplicit bool
	groupExplicit   bool
	successExplicit bool
}

type fileRoot struct {
	NewAPI rawSection `yaml:"new-api"`
}

type rawSection struct {
	PostgresURI  *string  `yaml:"postgres-uri"`
	PostgresURI2 *string  `yaml:"postgres_uri"`
	DSN          *string  `yaml:"dsn"`
	Host         *string  `yaml:"host"`
	Port         *int     `yaml:"port"`
	User         *string  `yaml:"user"`
	Password     *string  `yaml:"password"`
	Database     *string  `yaml:"database"`
	SSLMode      *string  `yaml:"sslmode"`
	QuotaPerUnit *float64 `yaml:"quota-per-unit"`
	GroupRatio   *float64 `yaml:"group-ratio"`
	ChannelID    *int     `yaml:"channel-id"`
	Timezone     *string  `yaml:"timezone"`
	ModelPrefix  *string  `yaml:"model-prefix"`
	Models       []string `yaml:"models"`
	ModelExclude *string  `yaml:"model-exclude"`
	CreatedAt    *string  `yaml:"created-at"`
	ProTier      *string  `yaml:"pro-tier"`
	UltraTier    *string  `yaml:"ultra-tier"`
	SuccessType  *int     `yaml:"success-type"`
}

// LoadConfig reads the optional new-api section of cluster.yaml, then applies
// environment overrides. A missing file is not an error.
func LoadConfig(path string) Config {
	cfg := Config{}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "cluster.yaml"
	}
	content, errRead := os.ReadFile(path)
	if errRead == nil {
		var root fileRoot
		if errYAML := yaml.Unmarshal(content, &root); errYAML == nil {
			cfg = root.NewAPI.config()
		}
	}
	cfg.applyEnv()
	return cfg.normalized()
}

func (raw rawSection) config() Config {
	cfg := Config{}
	switch {
	case raw.DSN != nil && strings.TrimSpace(*raw.DSN) != "":
		cfg.PostgresURI = strings.TrimSpace(*raw.DSN)
	case raw.PostgresURI != nil && strings.TrimSpace(*raw.PostgresURI) != "":
		cfg.PostgresURI = strings.TrimSpace(*raw.PostgresURI)
	case raw.PostgresURI2 != nil && strings.TrimSpace(*raw.PostgresURI2) != "":
		cfg.PostgresURI = strings.TrimSpace(*raw.PostgresURI2)
	}
	if raw.Host != nil {
		cfg.Host = strings.TrimSpace(*raw.Host)
	}
	if raw.Port != nil {
		cfg.Port = *raw.Port
	}
	if raw.User != nil {
		cfg.User = strings.TrimSpace(*raw.User)
	}
	if raw.Password != nil {
		cfg.Password = *raw.Password
	}
	if raw.Database != nil {
		cfg.Database = strings.TrimSpace(*raw.Database)
	}
	if raw.SSLMode != nil {
		cfg.SSLMode = strings.TrimSpace(*raw.SSLMode)
	}
	if raw.QuotaPerUnit != nil {
		cfg.QuotaPerUnit = *raw.QuotaPerUnit
	}
	if raw.GroupRatio != nil {
		cfg.GroupRatio = *raw.GroupRatio
		cfg.groupExplicit = true
	}
	if raw.ChannelID != nil {
		cfg.ChannelID = *raw.ChannelID
		cfg.channelExplicit = true
	}
	if raw.Timezone != nil {
		cfg.Timezone = strings.TrimSpace(*raw.Timezone)
	}
	if raw.ModelPrefix != nil {
		cfg.ModelPrefix = strings.TrimSpace(*raw.ModelPrefix)
	}
	cfg.Models = cleanModels(raw.Models)
	if raw.ModelExclude != nil {
		cfg.ModelExclude = strings.TrimSpace(*raw.ModelExclude)
	}
	if raw.CreatedAt != nil {
		cfg.CreatedAt = strings.TrimSpace(*raw.CreatedAt)
	}
	if raw.ProTier != nil {
		cfg.ProTier = strings.TrimSpace(*raw.ProTier)
	}
	if raw.UltraTier != nil {
		cfg.UltraTier = strings.TrimSpace(*raw.UltraTier)
	}
	if raw.SuccessType != nil {
		cfg.SuccessType = *raw.SuccessType
		cfg.successExplicit = true
	}
	return cfg
}

func (c *Config) applyEnv() {
	if c == nil {
		return
	}
	dsn := strings.TrimSpace(os.Getenv("NEW_API_DATABASE_DSN"))
	host := strings.TrimSpace(os.Getenv("NEW_API_DB_HOST"))
	user := strings.TrimSpace(os.Getenv("NEW_API_DB_USER"))
	password, passwordSet := os.LookupEnv("NEW_API_DB_PASSWORD")
	database := strings.TrimSpace(os.Getenv("NEW_API_DB_NAME"))
	sslmode := strings.TrimSpace(os.Getenv("NEW_API_DB_SSLMODE"))
	portRaw := strings.TrimSpace(os.Getenv("NEW_API_DB_PORT"))
	partsOverride := host != "" || user != "" || (passwordSet && strings.TrimSpace(password) != "") || database != "" || sslmode != "" || portRaw != ""
	if dsn != "" {
		c.PostgresURI = dsn
	} else if partsOverride {
		// Individual fields replace a URI from the file so the env values are actually used.
		c.PostgresURI = ""
	}
	if host != "" {
		c.Host = host
	}
	if user != "" {
		c.User = user
	}
	if passwordSet && strings.TrimSpace(password) != "" {
		c.Password = password
	}
	if database != "" {
		c.Database = database
	}
	if sslmode != "" {
		c.SSLMode = sslmode
	}
	if portRaw != "" {
		if port, errPort := strconv.Atoi(portRaw); errPort == nil && port > 0 {
			c.Port = port
		}
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_QUOTA_PER_UNIT")); raw != "" {
		if value, errValue := strconv.ParseFloat(raw, 64); errValue == nil && value > 0 {
			c.QuotaPerUnit = value
		}
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_GROUP_RATIO")); raw != "" {
		if value, errValue := strconv.ParseFloat(raw, 64); errValue == nil && value >= 0 {
			c.GroupRatio = value
			c.groupExplicit = true
		}
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_CHANNEL_ID")); raw != "" {
		if value, errValue := strconv.Atoi(raw); errValue == nil && value >= 0 {
			c.ChannelID = value
			c.channelExplicit = true
		}
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_BILLING_TIMEZONE")); raw != "" {
		c.Timezone = raw
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_MODEL_PREFIX")); raw != "" {
		c.ModelPrefix = raw
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_MODELS")); raw != "" {
		c.Models = cleanModels(strings.Split(raw, ","))
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_MODEL_EXCLUDE")); raw != "" {
		c.ModelExclude = raw
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_CREATED_AT")); raw != "" {
		c.CreatedAt = raw
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_PRO_TIER")); raw != "" {
		c.ProTier = raw
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_ULTRA_TIER")); raw != "" {
		c.UltraTier = raw
	}
	if raw := strings.TrimSpace(os.Getenv("NEW_API_SUCCESS_TYPE")); raw != "" {
		if value, errValue := strconv.Atoi(raw); errValue == nil && value >= 0 {
			c.SuccessType = value
			c.successExplicit = true
		}
	}
}

func (c Config) normalized() Config {
	if c.Port == 0 {
		c.Port = 5432
	}
	if strings.TrimSpace(c.SSLMode) == "" {
		c.SSLMode = "disable"
	}
	if c.QuotaPerUnit <= 0 {
		c.QuotaPerUnit = DefaultQuotaPerUnit
	}
	if !c.groupExplicit {
		c.GroupRatio = DefaultGroupRatio
	}
	if !c.channelExplicit {
		c.ChannelID = DefaultChannelID
	}
	if strings.TrimSpace(c.Timezone) == "" {
		c.Timezone = DefaultTimezone
	}
	if strings.TrimSpace(c.ModelPrefix) == "" {
		c.ModelPrefix = DefaultModelPrefix
	}
	if strings.TrimSpace(c.ModelExclude) == "" {
		c.ModelExclude = DefaultModelExclude
	}
	c.CreatedAt = strings.ToLower(strings.TrimSpace(c.CreatedAt))
	switch c.CreatedAt {
	case "unix", "unix_ms", "timestamptz":
	default:
		c.CreatedAt = DefaultCreatedAt
	}
	if strings.TrimSpace(c.ProTier) == "" {
		c.ProTier = DefaultProTier
	}
	if strings.TrimSpace(c.UltraTier) == "" {
		c.UltraTier = DefaultUltraTier
	}
	if !c.successExplicit {
		c.SuccessType = DefaultSuccessType
	}
	c.Models = cleanModels(c.Models)
	c.PostgresURI = strings.TrimSpace(c.PostgresURI)
	c.Host = strings.TrimSpace(c.Host)
	c.Database = strings.TrimSpace(c.Database)
	return c
}

// Configured reports whether a new-api DSN or host plus database is present.
func (c Config) Configured() bool {
	if strings.TrimSpace(c.PostgresURI) != "" {
		return true
	}
	return strings.TrimSpace(c.Host) != "" && strings.TrimSpace(c.Database) != ""
}

// SafeString identifies the source without the password or the DSN.
func (c Config) SafeString() string {
	host := c.Host
	if strings.TrimSpace(c.PostgresURI) != "" {
		host = uriHost(c.PostgresURI)
	}
	return fmt.Sprintf("host=%s database=%s user=%s channel=%d configured=%t", host, c.Database, c.User, c.ChannelID, c.Configured())
}

func uriHost(raw string) string {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Hostname() == "" {
		return "[dsn]"
	}
	return parsed.Hostname()
}

func (c Config) connectionString() (string, error) {
	cfg := c.normalized()
	if !cfg.Configured() {
		return "", ErrNotConfigured
	}
	if cfg.PostgresURI != "" {
		return withReadOnly(cfg.PostgresURI), nil
	}
	parts := []string{
		"host=" + quoteDSN(cfg.Host),
		"port=" + strconv.Itoa(cfg.Port),
		"user=" + quoteDSN(cfg.User),
		"password=" + quoteDSN(cfg.Password),
		"dbname=" + quoteDSN(cfg.Database),
		"sslmode=" + quoteDSN(cfg.SSLMode),
	}
	return withReadOnly(strings.Join(parts, " ")), nil
}

func withReadOnly(dsn string) string {
	const session = "-c default_transaction_read_only=on -c statement_timeout=25s"
	if strings.Contains(dsn, "default_transaction_read_only") {
		return dsn
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, errParse := url.Parse(dsn)
		if errParse != nil {
			return dsn
		}
		query := parsed.Query()
		existing := query.Get("options")
		if existing == "" {
			query.Set("options", session)
		} else {
			query.Set("options", existing+" "+session)
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " options='" + session + "'"
}

func quoteDSN(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\r\n\\'") {
		return value
	}
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

func cleanModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

var (
	userinfoPattern = regexp.MustCompile(`://[^/\s@]*@`)
	passwordPattern = regexp.MustCompile(`(?i)(password=)[^\s']*`)
)

func redactSecrets(message, password, dsn string) string {
	if dsn != "" {
		message = strings.ReplaceAll(message, dsn, "[redacted-dsn]")
	}
	if password != "" {
		message = strings.ReplaceAll(message, password, "[redacted]")
	}
	message = userinfoPattern.ReplaceAllString(message, "://[redacted]@")
	message = passwordPattern.ReplaceAllString(message, "${1}[redacted]")
	return message
}
