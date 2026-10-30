package auth

import (
	"testing"
	"time"
)

func TestMatchModelPattern(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"gemini-4-*", "gemini-4-pro", true},
		{"gemini-4-*", "gemini-4-argon-a", true},
		{"gemini-4-*", "gemini-3-pro", false},
		{"claude-*", "claude-opus", true},
		{"exact", "exact", true},
		{"exact", "exact-extra", false},
		{"*-pro", "gemini-4-pro", true},
		{"", "anything", false},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXc", false},
	}
	for _, tc := range cases {
		if got := matchModelPattern(tc.pattern, tc.value); got != tc.want {
			t.Fatalf("matchModelPattern(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

func TestAuthPriorityForModel(t *testing.T) {
	scoped := &Auth{
		ID: "ultra",
		Attributes: map[string]string{
			"priority":        "1",
			"priority_models": "gemini-4-*,claude-*",
		},
	}
	legacy := &Auth{
		ID:         "legacy",
		Attributes: map[string]string{"priority": "5"},
	}
	zero := &Auth{ID: "zero", Attributes: map[string]string{"priority": "0"}}

	if got := authPriorityForModel(scoped, "gemini-4-pro"); got != 1 {
		t.Fatalf("scoped gemini-4-pro = %d, want 1", got)
	}
	if got := authPriorityForModel(scoped, "claude-sonnet"); got != 1 {
		t.Fatalf("scoped claude = %d, want 1", got)
	}
	if got := authPriorityForModel(scoped, "gpt-5.5"); got != 0 {
		t.Fatalf("scoped gpt = %d, want 0", got)
	}
	if got := authPriorityForModel(legacy, "gpt-5.5"); got != 5 {
		t.Fatalf("legacy all-models = %d, want 5", got)
	}
	if got := authPriorityForModel(zero, "gemini-4-pro"); got != 0 {
		t.Fatalf("zero priority = %d, want 0", got)
	}
	fromMeta := &Auth{
		ID: "meta",
		Metadata: map[string]any{
			"priority_models": []any{"Gemini-4-*", "gemini-4-*", " Claude-* "},
		},
		Attributes: map[string]string{"priority": "3"},
	}
	patterns := authPriorityModels(fromMeta)
	if len(patterns) != 2 || patterns[0] != "claude-*" || patterns[1] != "gemini-4-*" {
		t.Fatalf("normalized patterns = %#v", patterns)
	}
	if got := authPriorityForModel(fromMeta, "gemini-4-flash"); got != 3 {
		t.Fatalf("meta gemini = %d, want 3", got)
	}
}

func TestCollectAvailableByPriorityRespectsPriorityModels(t *testing.T) {
	now := time.Now()
	highScoped := &Auth{
		ID:     "high-scoped",
		Status: StatusActive,
		Attributes: map[string]string{
			"priority":        "10",
			"priority_models": "gemini-4-*",
		},
	}
	normal := &Auth{
		ID:         "normal",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "0"},
	}
	available, _, _ := collectAvailableByPriority([]*Auth{highScoped, normal}, "gemini-4-pro", now)
	if len(available[10]) != 1 || available[10][0].ID != "high-scoped" {
		t.Fatalf("gemini-4 available = %#v, want high-scoped at priority 10", available)
	}
	availableOther, _, _ := collectAvailableByPriority([]*Auth{highScoped, normal}, "gpt-5.5", now)
	if len(availableOther[10]) != 0 {
		t.Fatalf("gpt should not see scoped priority: %#v", availableOther)
	}
	if len(availableOther[0]) != 2 {
		t.Fatalf("gpt available at 0 = %#v, want both", availableOther[0])
	}
}

func TestScheduledAuthMetaPriorityForModel(t *testing.T) {
	meta := &scheduledAuthMeta{
		priority:       7,
		priorityModels: []string{"gemini-4-*"},
	}
	if got := meta.priorityForModel("gemini-4-pro"); got != 7 {
		t.Fatalf("match = %d, want 7", got)
	}
	if got := meta.priorityForModel("claude-opus"); got != 0 {
		t.Fatalf("miss = %d, want 0", got)
	}
	all := &scheduledAuthMeta{priority: 7}
	if got := all.priorityForModel("anything"); got != 7 {
		t.Fatalf("empty patterns = %d, want 7", got)
	}
}

func TestGetAvailableAuthsPrefersScopedPriorityOnlyForMatchingModel(t *testing.T) {
	now := time.Now()
	vip := &Auth{
		ID:       "vip",
		Provider: "antigravity",
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority":        "1",
			"priority_models": "gemini-4-*",
		},
	}
	pool := &Auth{
		ID:         "pool",
		Provider:   "antigravity",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "0"},
	}
	got, err := getAvailableAuths([]*Auth{vip, pool}, "antigravity", "gemini-4-pro", now)
	if err != nil || len(got) != 1 || got[0].ID != "vip" {
		t.Fatalf("gemini pick = %#v err=%v, want only vip", got, err)
	}
	gotOther, err := getAvailableAuths([]*Auth{vip, pool}, "antigravity", "gpt-5.5", now)
	if err != nil || len(gotOther) != 2 {
		t.Fatalf("other model = %#v err=%v, want both at equal priority", gotOther, err)
	}
}
