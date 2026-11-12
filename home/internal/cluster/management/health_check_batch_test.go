package management

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestHealthChecksForBatchFallsBackToImport(t *testing.T) {
	now := time.Now().UTC()
	imported := now.Add(-time.Minute)
	batch := "20261112-2235-7da7061c"
	imports := map[string]cluster.CredentialImport{
		"a1":    {AuthUUID: "a1", BatchID: batch, ImportedAt: imported},
		"a2":    {AuthUUID: "a2", BatchID: batch, ImportedAt: imported},
		"a3":    {AuthUUID: "a3", BatchID: batch, ImportedAt: imported},
		"other": {AuthUUID: "other", BatchID: "someone-else", ImportedAt: imported},
	}
	// a1: ownership intact; a2: sweep overwrote batch_id and imported_at;
	// a3: no health row yet. A credential from another batch must be ignored.
	checks := map[string]cluster.CredentialHealthCheck{
		"a1":    {AuthUUID: "a1", Status: "pass", BatchID: batch, ImportedAt: &imported, CheckedAt: now, Probe: "import"},
		"a2":    {AuthUUID: "a2", Status: "pass", BatchID: "", ImportedAt: nil, CheckedAt: now.Add(11 * time.Millisecond), Probe: "sweep"},
		"other": {AuthUUID: "other", Status: "fail", BatchID: "someone-else", CheckedAt: now},
	}

	got := healthChecksForBatch(checks, imports, batch)
	byID := map[string]cluster.CredentialHealthCheck{}
	for _, r := range got {
		byID[r.AuthUUID] = r
	}
	if len(got) != 3 {
		t.Fatalf("want 3 (this batch only), got %d: %+v", len(got), got)
	}
	if byID["a1"].Status != "pass" || byID["a1"].BatchID != batch {
		t.Fatalf("a1: %+v", byID["a1"])
	}
	if byID["a2"].Status != "pass" || byID["a2"].BatchID != batch {
		t.Fatalf("a2 should be recovered via import: %+v", byID["a2"])
	}
	if byID["a2"].ImportedAt == nil || !byID["a2"].ImportedAt.Equal(imported) {
		t.Fatalf("a2 imported_at should be restored: %+v", byID["a2"])
	}
	if byID["a3"].Status != "running" || byID["a3"].BatchID != batch {
		t.Fatalf("a3 should be a running stub: %+v", byID["a3"])
	}

	sum := healthSummary(got)
	if sum["total"] != 3 || sum["pass"] != 2 || sum["fail"] != 0 || sum["running"] != 1 {
		t.Fatalf("summary = %+v (panel remaining = imported-total+running must be 1)", sum)
	}

	if empty := healthChecksForBatch(checks, imports, ""); empty != nil {
		t.Fatalf("empty batch_id must return nil, got %v", empty)
	}
}
