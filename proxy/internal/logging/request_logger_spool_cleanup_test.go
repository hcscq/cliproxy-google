package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgeStaleBodySpoolsKeepsCurrentProcessSpools(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "request-log-parts-request-body-1")
	fresh := filepath.Join(dir, "request-log-parts-request-body-2")
	other := filepath.Join(dir, "keep-me")
	for _, d := range []string{stale, fresh, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := processStart.Add(-time.Hour)
	_ = os.Chtimes(stale, old, old)
	_ = os.Chtimes(other, old, old)
	future := processStart.Add(time.Second)
	_ = os.Chtimes(fresh, future, future)
	purgeStaleBodySpools(dir)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale spool not removed")
	}
	for _, d := range []string{fresh, other} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s removed: %v", d, err)
		}
	}
}
