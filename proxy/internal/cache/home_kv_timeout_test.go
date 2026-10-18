package cache

import (
	"context"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

type stallingKV struct{}

func (stallingKV) KVGet(ctx context.Context, _ string) ([]byte, bool, error) {
	<-ctx.Done()
	return nil, false, ctx.Err()
}
func (stallingKV) KVSet(ctx context.Context, _ string, _ []byte, _ homekv.KVSetOptions) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}
func (stallingKV) KVCompareAndSwap(ctx context.Context, _ string, _ []byte, _ bool, _ []byte, _ time.Duration) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}
func (stallingKV) KVExpire(ctx context.Context, _ string, _ time.Duration) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func TestReasoningReplayHomeKVStallFailsFast(t *testing.T) {
	prev := currentAntigravityReasoningReplayKVClient
	currentAntigravityReasoningReplayKVClient = func() (antigravityReasoningReplayKVClient, bool, error) { return stallingKV{}, true, nil }
	t.Cleanup(func() { currentAntigravityReasoningReplayKVClient = prev; SetHomeKVTimeoutMillis(0) })
	SetHomeKVTimeoutMillis(40)

	start := time.Now()
	_, found, err := GetAntigravityReasoningReplayItemsRequired(context.Background(), "gemini-3-flash", "sess-1")
	if err == nil || found {
		t.Fatalf("expected timeout error, found=%v err=%v", found, err)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("read took %s", el)
	}
	start = time.Now()
	if CacheAntigravityReasoningReplayItemsBestEffort(context.Background(), "gemini-3-flash", "sess-1", [][]byte{[]byte(`{"type":"reasoning","encrypted_content":"x","summary":[]}`)}) {
		t.Fatal("stalled set reported success")
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Fatalf("set took %s", el)
	}
}

func TestSetHomeKVTimeoutMillis(t *testing.T) {
	t.Cleanup(func() { SetHomeKVTimeoutMillis(0) })
	SetHomeKVTimeoutMillis(0)
	if HomeKVTimeout() != DefaultHomeKVTimeout {
		t.Fatal(HomeKVTimeout())
	}
	SetHomeKVTimeoutMillis(250)
	if HomeKVTimeout() != 250*time.Millisecond {
		t.Fatal(HomeKVTimeout())
	}
	SetHomeKVTimeoutMillis(-1)
	if HomeKVTimeout() != 0 {
		t.Fatal(HomeKVTimeout())
	}
}
