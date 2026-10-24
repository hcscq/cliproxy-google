package management

import (
	"encoding/json"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestApplyOAuthFieldPatchSyncsExcludedModels(t *testing.T) {
	auth := &coreauth.Auth{ID: "a", Provider: "antigravity", Metadata: map[string]any{}, Attributes: map[string]string{"excluded_models": "old-global"}}
	changed, err := applyOAuthFieldPatch(auth, map[string]json.RawMessage{"excluded_models": json.RawMessage(`["Claude-*"," claude-*","gpt-oss-x"]`)})
	if err != nil || !changed {
		t.Fatalf("patch: changed=%v err=%v", changed, err)
	}
	if got := auth.Attributes["excluded_models"]; got != "claude-*,gpt-oss-x" {
		t.Fatalf("attribute = %q", got)
	}
	if !matchesExcluded(credentialExcludedModels(auth.Metadata), "claude-opus-4-6-thinking") {
		t.Fatal("expected claude model excluded")
	}
	if _, err := applyOAuthFieldPatch(auth, map[string]json.RawMessage{"excluded_models": json.RawMessage(`[]`)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := auth.Attributes["excluded_models"]; ok {
		t.Fatal("expected attribute cleared")
	}
	if _, ok := auth.Metadata["excluded_models"]; ok {
		t.Fatal("expected metadata cleared")
	}
}
