package cluster

import (
	"os"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

func TestAffinityRedisBindSemantics(t *testing.T) {
	addr := os.Getenv("CLIPROXY_HOME_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("CLIPROXY_HOME_TEST_REDIS_ADDR is not configured")
	}
	repo, ctx := newTestKVRepository(t)
	if err := ensureAffinityTables(ctx, repo.db); err != nil {
		t.Fatal(err)
	}
	rc := redisx.Open(redisx.Config{Enabled: true, Addr: addr, KeyPrefix: "aff" + time.Now().Format("150405.000000") + ":"})
	defer rc.Close()
	redisx.SetDefault(rc)
	defer redisx.SetDefault(nil)
	k := AffinityBindingKey("key", "conv")

	if _, ok := repo.LookupAffinity(ctx, k); ok {
		t.Fatal("unexpected binding")
	}
	steps := []struct{ auth, prev, want string }{
		{"a1", "", "a1"},   // first bind creates
		{"a2", "", "a1"},   // a racing first bind loses (NX)
		{"a1", "a1", "a1"}, // hit refresh
		{"a3", "a2", "a1"}, // re-pin from a stale view does not clobber
		{"a3", "a1", "a3"}, // re-pin from the current value replaces
	}
	for i, s := range steps {
		got, err := repo.BindAffinityFrom(ctx, k, s.auth, s.prev, "m", "prefix", time.Minute)
		if err != nil || got != s.want {
			t.Fatalf("step %d: got %q %v, want %q", i, got, err, s.want)
		}
		if v, ok := repo.LookupAffinity(ctx, k); !ok || v != s.want {
			t.Fatalf("step %d: lookup %q %v, want %q", i, v, ok, s.want)
		}
	}
	// Redis paths must not touch the PostgreSQL binding table.
	var n int64
	repo.db.Model(&AffinityBindingRecord{}).Count(&n)
	if n != 0 {
		t.Fatalf("PostgreSQL bindings written while Redis healthy: %d", n)
	}
}
