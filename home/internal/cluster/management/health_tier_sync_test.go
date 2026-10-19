package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestReconcileHealthTierUpgradeClearsNonPro(t *testing.T) {
	// Prod shape of 262c346a: failed only on tier at import, now Pro.
	rec := cluster.CredentialHealthCheck{AuthUUID: "x", Status: "fail", Categories: "tier", Reasons: "订阅为 free-tier，期望 pro", Tier: "free-tier", RecheckCount: 2}
	if !reconcileHealthTier(&rec, "g1-pro-tier", "pro") {
		t.Fatal("expected change")
	}
	if rec.Status != "pass" || rec.Categories != "" || rec.Reasons != "" || rec.Tier != "g1-pro-tier" || rec.RecheckCount != 0 {
		t.Fatalf("got %+v", rec)
	}
	if reconcileHealthTier(&rec, "g1-pro-tier", "pro") {
		t.Fatal("second run must be a no-op")
	}
}

func TestReconcileHealthTierKeepsOtherFailures(t *testing.T) {
	rec := cluster.CredentialHealthCheck{Status: "fail", Categories: "tier,quota_used", Reasons: "订阅为 free-tier，期望 pro\n额度非新", Tier: "free-tier"}
	reconcileHealthTier(&rec, "g1-pro-tier", "pro")
	if rec.Status != "fail" || rec.Categories != "quota_used" || rec.Reasons != "额度非新" {
		t.Fatalf("got %+v", rec)
	}
	warn := cluster.CredentialHealthCheck{Status: "fail", Categories: "region_warn,tier", Reasons: "地区提示\n订阅为 free-tier，期望 pro"}
	reconcileHealthTier(&warn, "g1-pro-tier", "pro")
	if warn.Status != "pass" || warn.Categories != "region_warn" {
		t.Fatalf("region_warn alone is a pass: %+v", warn)
	}
}

func TestReconcileHealthTierDowngradeAndNoops(t *testing.T) {
	rec := cluster.CredentialHealthCheck{Status: "pass", Tier: "g1-pro-tier"}
	if !reconcileHealthTier(&rec, "free-tier", "pro") || rec.Status != "fail" || rec.Categories != "tier" || rec.Reasons != "订阅为 free-tier，期望 pro" {
		t.Fatalf("downgrade: %+v", rec)
	}
	running := cluster.CredentialHealthCheck{Status: "running", Categories: ""}
	if reconcileHealthTier(&running, "free-tier", "pro") {
		t.Fatal("running check must not be touched")
	}
	anyTier := cluster.CredentialHealthCheck{Status: "pass", Tier: "free-tier"}
	if reconcileHealthTier(&anyTier, "free-tier", "") {
		t.Fatal("no change expected when no tier is expected")
	}
	if reconcileHealthTier(&anyTier, "", "pro") {
		t.Fatal("empty tier ignored")
	}
}
