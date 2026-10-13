package cluster

import (
	"context"
	"testing"
	"time"
)

func TestClassifyCall(t *testing.T) {
	cases := []struct {
		failed bool
		code   int
		body   string
		want   string
	}{
		{false, 200, "", CallClassOK},
		{true, 429, "", CallClass429},
		{true, 503, "", CallClass503},
		{true, 400, "", CallClass400},
		{true, 404, "", CallClass404},
		{true, 502, "proxyconnect tcp: dial tcp 1.2.3.4:3141: connection refused", CallClassNetwork},
		{true, 502, "bad gateway", CallClass5xx},
		{true, 499, "", CallClassCancelled},
		{true, 401, "", CallClassOther},
	}
	for _, c := range cases {
		if got := ClassifyCall(c.failed, c.code, c.body); got != c.want {
			t.Errorf("%v %d %q: got %s want %s", c.failed, c.code, c.body, got, c.want)
		}
	}
}

func TestRollupCredentialCallStats(t *testing.T) {
	ctx := context.Background()
	repo, closeRepo := newBillingTestRepository(t, ctx)
	defer closeRepo()
	now := time.Now().UTC()
	add := func(auth, model string, failed bool, code int) {
		rec := UsageRecord{Timestamp: now, AuthIndex: auth, Model: model, Failed: failed, FailStatusCode: code, PayloadJSON: JSONB(`{}`), CreatedAt: now}
		if err := repo.db.Create(&rec).Error; err != nil {
			t.Fatal(err)
		}
	}
	add("a", "m1", false, 200)
	add("a", "m1", true, 429)
	add("a", "m2", false, 200)
	add("b", "m1", true, 503)
	if n, err := repo.RollupCredentialCallStats(ctx); err != nil || n != 4 {
		t.Fatalf("rollup n=%d err=%v", n, err)
	}
	add("a", "m1", false, 200)
	if n, err := repo.RollupCredentialCallStats(ctx); err != nil || n != 1 {
		t.Fatalf("incremental n=%d err=%v", n, err)
	}
	stats, err := repo.LoadCredentialCallStats(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if a := stats["a"]; a == nil || a.Calls != 4 || a.OK != 3 || a.ByClass[CallClass429] != 1 {
		t.Fatalf("a = %+v", a)
	}
	if b := stats["b"]; b == nil || b.Calls != 1 || b.ByClass[CallClass503] != 1 {
		t.Fatalf("b = %+v", b)
	}
	models, err := repo.LoadCredentialModelCallStats(ctx, "a", now.Add(-time.Hour))
	if err != nil || len(models) != 2 {
		t.Fatalf("models %+v %v", models, err)
	}
}
