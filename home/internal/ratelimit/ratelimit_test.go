package ratelimit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
)

func fixedLimiter(homes int, at time.Time) *Limiter {
	l := New(func() int { return homes })
	l.now = func() time.Time { return at }
	return l
}

func TestLocalLimitSplitsAcrossHomesAndReleases(t *testing.T) {
	redisx.SetDefault(nil)
	at := time.Unix(1_700_000_040, 0) // 0s into a minute
	l := fixedLimiter(2, at)
	ctx := context.Background()
	ok := 0
	var last Ticket
	for i := 0; i < 20; i++ {
		if a, tk := l.Allow(ctx, "k", "key1", 10); a {
			ok++
			last = tk
		}
	}
	if ok != 5 { // 10 per minute over 2 live Homes
		t.Fatalf("allowed %d, want 5", ok)
	}
	last.Release(ctx)
	if a, _ := l.Allow(ctx, "k", "key1", 10); !a {
		t.Fatal("released slot was not reusable")
	}
	if a, _ := l.Allow(ctx, "k", "other", 10); !a {
		t.Fatal("ids must be independent")
	}
	if a, _ := l.Allow(ctx, "k", "key1", 0); !a {
		t.Fatal("limit 0 must be unlimited")
	}
}

func TestLocalSlidingWindowCarriesPreviousMinute(t *testing.T) {
	redisx.SetDefault(nil)
	base := time.Unix(1_700_000_040, 0)
	l := fixedLimiter(1, base)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		l.Allow(ctx, "c", "x", 10)
	}
	l.now = func() time.Time { return base.Add(90 * time.Second) } // 30s into next minute: prev weighs 50%
	ok := 0
	for i := 0; i < 10; i++ {
		if a, _ := l.Allow(ctx, "c", "x", 10); a {
			ok++
		}
	}
	if ok != 5 {
		t.Fatalf("allowed %d, want 5", ok)
	}
}

func TestRedisLimitIsGlobalAcrossHomes(t *testing.T) {
	addr := os.Getenv("CLIPROXY_HOME_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("CLIPROXY_HOME_TEST_REDIS_ADDR is not configured")
	}
	rc := redisx.Open(redisx.Config{Enabled: true, Addr: addr, KeyPrefix: "t" + time.Now().Format("150405.000000") + ":"})
	defer rc.Close()
	redisx.SetDefault(rc)
	defer redisx.SetDefault(nil)
	at := time.Unix(1_700_000_040, 0)
	a, b := fixedLimiter(2, at), fixedLimiter(2, at) // two Homes
	ctx := context.Background()
	ok := 0
	for i := 0; i < 20; i++ {
		for _, l := range []*Limiter{a, b} {
			if g, _ := l.Allow(ctx, "k", "key1", 10); g {
				ok++
			}
		}
	}
	if ok != 10 {
		t.Fatalf("allowed %d across homes, want exactly 10", ok)
	}
}
