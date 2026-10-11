package get

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/registry"
	"github.com/router-for-me/CLIProxyAPIHome/internal/respserver/dispatch"
	"github.com/tidwall/gjson"
)

func TestBuildModelsJSONScopedFiltersByKeyScope(t *testing.T) {
	rt := newGetTestRuntime(t)
	ctx := coreauth.WithSkipPersist(context.Background())
	for _, id := range []string{"scope-a", "scope-b"} {
		if _, err := rt.CoreManager().Register(ctx, &coreauth.Auth{ID: id, Index: id, Provider: "antigravity", Status: coreauth.StatusActive}); err != nil {
			t.Fatal(err)
		}
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("scope-a", "antigravity", []*registry.ModelInfo{{ID: "m-shared"}, {ID: "m-only-a"}})
	reg.RegisterClient("scope-b", "antigravity", []*registry.ModelInfo{{ID: "m-shared"}, {ID: "m-only-b"}})
	t.Cleanup(func() { reg.UnregisterClient("scope-a"); reg.UnregisterClient("scope-b") })
	env := dispatch.Env{Runtime: rt}

	ids := func(payload string) map[string]bool {
		out := map[string]bool{}
		for _, m := range gjson.Get(payload, "antigravity.#.id").Array() {
			out[m.String()] = true
		}
		return out
	}
	all, err := buildModelsJSONScoped(env, nil, nil)
	if err != nil || !ids(all)["m-only-a"] || !ids(all)["m-only-b"] {
		t.Fatalf("unrestricted: %v %s", err, all)
	}
	byCred, _ := buildModelsJSONScoped(env, []string{"scope-b"}, nil)
	if got := ids(byCred); got["m-only-a"] || !got["m-only-b"] || !got["m-shared"] {
		t.Fatalf("credential scope: %v", got)
	}
	byModel, _ := buildModelsJSONScoped(env, nil, []string{"M-Shared"})
	if got := ids(byModel); len(got) != 1 || !got["m-shared"] {
		t.Fatalf("model scope: %v", got)
	}
	none, _ := buildModelsJSONScoped(env, nil, []string{})
	if got := ids(none); len(got) != 0 {
		t.Fatalf("empty allow-list must list nothing: %v", got)
	}
}
