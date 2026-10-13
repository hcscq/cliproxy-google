package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ---- credential attributes used for filtering and rules ----

// CredentialEmail returns the account e-mail of a credential ("" if unknown).
func CredentialEmail(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Metadata != nil {
		if s, ok := a.Metadata["email"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if a.Attributes != nil {
		if s := strings.TrimSpace(a.Attributes["email"]); s != "" {
			return s
		}
	}
	if strings.Contains(a.Label, "@") {
		return strings.TrimSpace(a.Label)
	}
	return ""
}

// EmailDomain returns the lower-case domain part of an e-mail.
func EmailDomain(email string) string {
	if i := strings.LastIndex(email, "@"); i >= 0 && i < len(email)-1 {
		return strings.ToLower(email[i+1:])
	}
	return ""
}

// CredentialTags returns the normalised tags stored in credential metadata
// ("tags": ["a","b"] or "a,b").
func CredentialTags(a *coreauth.Auth) []string {
	if a == nil || a.Metadata == nil {
		return nil
	}
	var raw []string
	switch v := a.Metadata["tags"].(type) {
	case []string:
		raw = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				raw = append(raw, s)
			}
		}
	case string:
		raw = strings.Split(v, ",")
	}
	return NormalizeTags(raw)
}

// NormalizeTags trims, lower-cases, de-duplicates and sorts tags.
func NormalizeTags(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || len(t) > 64 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// CredentialPriority returns the routing priority (0 when unset).
func CredentialPriority(a *coreauth.Auth) int {
	if a == nil {
		return 0
	}
	if a.Attributes != nil {
		if p, err := strconv.Atoi(strings.TrimSpace(a.Attributes["priority"])); err == nil {
			return p
		}
	}
	if a.Metadata != nil {
		switch v := a.Metadata["priority"].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return 0
}

// ---- rule-based (dynamic) credential group membership ----

// GroupRule selects credentials dynamically. All set conditions must match;
// a rule with no condition matches nothing unless MatchAll is set.
type GroupRule struct {
	Providers    []string `json:"providers,omitempty"`
	EmailPattern string   `json:"email_pattern,omitempty"` // glob on e-mail, e.g. *@corp.com
	LabelPattern string   `json:"label_pattern,omitempty"` // glob on label / name
	TagsAny      []string `json:"tags_any,omitempty"`
	TagsAll      []string `json:"tags_all,omitempty"`
	MinPriority  *int     `json:"min_priority,omitempty"`
	MaxPriority  *int     `json:"max_priority,omitempty"`
	MatchAll     bool     `json:"match_all,omitempty"`
}

// Empty reports whether the rule has no condition.
func (r GroupRule) Empty() bool {
	return len(r.Providers) == 0 && r.EmailPattern == "" && r.LabelPattern == "" && len(r.TagsAny) == 0 &&
		len(r.TagsAll) == 0 && r.MinPriority == nil && r.MaxPriority == nil
}

// Validate checks glob syntax.
func (r GroupRule) Validate() error {
	for _, p := range []string{r.EmailPattern, r.LabelPattern} {
		if p == "" {
			continue
		}
		if _, err := path.Match(strings.ToLower(p), "x"); err != nil {
			return fmt.Errorf("invalid pattern %q: %w", p, err)
		}
	}
	return nil
}

func globMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(value))
	return err == nil && ok
}

// Matches evaluates the rule against a credential.
func (r GroupRule) Matches(a *coreauth.Auth) bool {
	if a == nil {
		return false
	}
	if r.Empty() {
		return r.MatchAll
	}
	if len(r.Providers) > 0 {
		ok := false
		for _, p := range r.Providers {
			if strings.EqualFold(strings.TrimSpace(p), a.Provider) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if r.EmailPattern != "" && !globMatch(r.EmailPattern, CredentialEmail(a)) {
		return false
	}
	if r.LabelPattern != "" && !globMatch(r.LabelPattern, a.Label) {
		return false
	}
	if len(r.TagsAny) > 0 || len(r.TagsAll) > 0 {
		tags := map[string]bool{}
		for _, t := range CredentialTags(a) {
			tags[t] = true
		}
		if len(r.TagsAny) > 0 {
			ok := false
			for _, t := range NormalizeTags(r.TagsAny) {
				if tags[t] {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
		for _, t := range NormalizeTags(r.TagsAll) {
			if !tags[t] {
				return false
			}
		}
	}
	p := CredentialPriority(a)
	if r.MinPriority != nil && p < *r.MinPriority {
		return false
	}
	if r.MaxPriority != nil && p > *r.MaxPriority {
		return false
	}
	return true
}

// ChannelGroupRuleRecord stores the dynamic membership rule of a credential group.
type ChannelGroupRuleRecord struct {
	ChannelGroupID uint      `gorm:"column:channel_group_id;primaryKey"`
	Enabled        bool      `gorm:"column:enabled;not null;default:true"`
	Rule           string    `gorm:"column:rule;type:text;not null;default:''"`
	UpdatedAt      time.Time `gorm:"column:updated_at"`
}

// TableName returns the database table name.
func (ChannelGroupRuleRecord) TableName() string { return "channel_group_rule" }

func ensureChannelGroupRuleTable(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Migrator().HasTable(&ChannelGroupRuleRecord{}) {
		return nil
	}
	if err := tx.Migrator().CreateTable(&ChannelGroupRuleRecord{}); err != nil {
		// Concurrent Home starters can race HasTable/CreateTable on a fresh DB.
		if tx.Migrator().HasTable(&ChannelGroupRuleRecord{}) {
			return nil
		}
		return err
	}
	return nil
}

// ParsedRule decodes the stored rule.
func (r ChannelGroupRuleRecord) ParsedRule() (GroupRule, error) {
	var rule GroupRule
	if strings.TrimSpace(r.Rule) == "" {
		return rule, nil
	}
	err := json.Unmarshal([]byte(r.Rule), &rule)
	return rule, err
}

// ListChannelGroupRules returns all rules keyed by group id.
func (r *Repository) ListChannelGroupRules(ctx context.Context) (map[uint]ChannelGroupRuleRecord, error) {
	out := map[uint]ChannelGroupRuleRecord{}
	db, err := r.database()
	if err != nil {
		return out, err
	}
	if err := ensureChannelGroupRuleTable(ctx, db); err != nil {
		return out, err
	}
	var rows []ChannelGroupRuleRecord
	if err := db.WithContext(contextOrBackground(ctx)).Find(&rows).Error; err != nil {
		return out, err
	}
	for _, row := range rows {
		out[row.ChannelGroupID] = row
	}
	return out, nil
}

// PutChannelGroupRule stores (or, with rule == nil, deletes) a group's rule.
func (r *Repository) PutChannelGroupRule(ctx context.Context, groupID uint, rule *GroupRule, enabled bool) error {
	db, err := r.database()
	if err != nil {
		return err
	}
	if err := ensureChannelGroupRuleTable(ctx, db); err != nil {
		return err
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if rule == nil {
		return tx.Where("channel_group_id = ?", groupID).Delete(&ChannelGroupRuleRecord{}).Error
	}
	if err := rule.Validate(); err != nil {
		return err
	}
	var group ChannelGroupRecord
	if err := tx.Where("id = ?", groupID).First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("channel group %d not found", groupID)
		}
		return err
	}
	raw, _ := json.Marshal(rule)
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "channel_group_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "rule", "updated_at"}),
	}).Create(&ChannelGroupRuleRecord{ChannelGroupID: groupID, Enabled: enabled, Rule: string(raw), UpdatedAt: time.Now().UTC()}).Error
}
