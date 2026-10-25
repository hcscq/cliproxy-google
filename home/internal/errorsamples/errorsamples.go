// Package errorsamples holds the live error-sample settings
// (observability.logs.error-samples-*) so the push handler follows
// hot-reloaded config.
package errorsamples

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	DefaultPerType  = 5
	DefaultMaxBytes = 1 << 20
	DefaultMaxTypes = 40
	MaxPerType      = 50
	MaxMaxTypes     = 200
	MaxMaxBytes     = 8 << 20
)

// Settings is the normalized configuration.
type Settings struct {
	Enabled   bool
	PerType   int
	MaxBytes  int
	MaxTypes  int
	Exclude   []string
	Overrides map[string]int
}

var current atomic.Pointer[Settings]

func init() { Set(nil, 0, 0, 0, "", "") }

// Set normalizes and stores the configured values.
func Set(enabled *bool, perType, maxBytes, maxTypes int, exclude, overrides string) {
	s := &Settings{Enabled: enabled == nil || *enabled, PerType: perType, MaxBytes: maxBytes, MaxTypes: maxTypes, Overrides: map[string]int{}}
	if s.PerType <= 0 {
		s.PerType = DefaultPerType
	}
	if s.PerType > MaxPerType {
		s.PerType = MaxPerType
	}
	if s.MaxBytes <= 0 {
		s.MaxBytes = DefaultMaxBytes
	}
	if s.MaxBytes > MaxMaxBytes {
		s.MaxBytes = MaxMaxBytes
	}
	if s.MaxBytes < 4096 {
		s.MaxBytes = 4096
	}
	if s.MaxTypes <= 0 {
		s.MaxTypes = DefaultMaxTypes
	}
	if s.MaxTypes > MaxMaxTypes {
		s.MaxTypes = MaxMaxTypes
	}
	for _, p := range splitList(exclude) {
		s.Exclude = append(s.Exclude, strings.ToLower(p))
	}
	for _, p := range splitList(overrides) {
		i := strings.LastIndex(p, "=")
		if i <= 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(p[i+1:]))
		if err != nil || n < 0 {
			continue
		}
		if n > MaxPerType {
			n = MaxPerType
		}
		s.Overrides[strings.ToLower(strings.TrimSpace(p[:i]))] = n
	}
	current.Store(s)
}

func splitList(v string) []string {
	f := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
	out := f[:0]
	for _, x := range f {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// Get returns the current settings.
func Get() *Settings { return current.Load() }

// Keep returns how many samples to keep for a type (0 = do not store).
// Exclusions and overrides match the exact type or a prefix ("429" matches
// "429:quota"). The longest matching override wins.
func (s *Settings) Keep(typ string) int {
	if s == nil || !s.Enabled {
		return 0
	}
	t := strings.ToLower(typ)
	for _, e := range s.Exclude {
		if t == e || strings.HasPrefix(t, e+":") || strings.HasPrefix(t, e) && strings.HasSuffix(e, "*") {
			return 0
		}
	}
	best, n := -1, s.PerType
	for k, v := range s.Overrides {
		if (t == k || strings.HasPrefix(t, k+":")) && len(k) > best {
			best, n = len(k), v
		}
	}
	return n
}

// Record is one captured failed-request context. Data is the
// gzip-compressed sample JSON (already redacted by the proxy).
type Record struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Type           string    `gorm:"column:type;not null;index:idx_error_sample_type_id,priority:1" json:"type"`
	CreatedAt      time.Time `gorm:"column:created_at;not null" json:"created_at"`
	Status         int       `gorm:"column:status" json:"status"`
	RequestedModel string    `gorm:"column:requested_model" json:"requested_model"`
	RequestID      string    `gorm:"column:request_id" json:"request_id"`
	ClientIP       string    `gorm:"column:client_ip" json:"client_ip"`
	HomeIP         string    `gorm:"column:home_ip" json:"home_ip"`
	Size           int64     `gorm:"column:size" json:"size"`
	Data           []byte    `gorm:"column:data" json:"-"`
}

// TableName returns the database table name.
func (Record) TableName() string { return "error_sample" }
