package cluster

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func newBootstrapTestRepo(t *testing.T) *Repository {
	t.Helper()
	ctx := context.Background()
	db, errOpen := OpenSQLite(ctx, filepath.Join(t.TempDir(), "home.db"))
	if errOpen != nil {
		t.Fatalf("OpenSQLite() error = %v", errOpen)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	if errMigrate := AutoMigrate(db); errMigrate != nil {
		t.Fatalf("AutoMigrate() error = %v", errMigrate)
	}
	return NewRepository(db)
}

func TestBootstrapClientCertificateIssuesEphemeralIdentities(t *testing.T) {
	t.Setenv(BootstrapTokenEnv, "bootstrap-secret")
	repo := newBootstrapTestRepo(t)
	ctx := context.Background()

	if _, err := repo.BootstrapClientCertificate(ctx, "wrong", "pod-a", nil); err == nil {
		t.Fatal("expected invalid token error")
	}
	ids := map[string]bool{}
	for _, name := range []string{"pod-a", "pod-a", "pod-b"} {
		_, csr := createClientCSR(t, "ignored")
		raw, err := repo.BootstrapClientCertificate(ctx, "bootstrap-secret", name, csr)
		if err != nil {
			t.Fatalf("BootstrapClientCertificate(%s) error = %v", name, err)
		}
		var resp bootstrapCertificateResponse
		if err := json.Unmarshal(raw, &resp); err != nil || !resp.OK || resp.CertificateID == "" || resp.Certificate == "" || resp.CA == "" {
			t.Fatalf("bad response %+v err=%v", resp, err)
		}
		fp, _ := certificateFingerprintPEM([]byte(resp.Certificate))
		active, err := repo.ClientCertificateFingerprintActive(ctx, resp.CertificateID, fp)
		if err != nil || !active {
			t.Fatalf("certificate not active: %v", err)
		}
		ids[resp.CertificateID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 distinct identities (token is reusable), got %d", len(ids))
	}

	// Nothing has heartbeated; after the TTL every identity is collected.
	time.Sleep(2100 * time.Millisecond) // SQLite DatabaseNow has second precision
	removed, err := repo.GarbageCollectEphemeralNodes(ctx, time.Second)
	if err != nil {
		t.Fatalf("GarbageCollectEphemeralNodes() error = %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
}

func TestBootstrapDisabledWithoutToken(t *testing.T) {
	t.Setenv(BootstrapTokenEnv, "")
	repo := newBootstrapTestRepo(t)
	if _, err := repo.BootstrapClientCertificate(context.Background(), "x", "pod", nil); err == nil {
		t.Fatal("expected disabled error")
	}
}

// A reconnecting proxy is absent from the CPA node snapshot; its recently
// touched membership must keep its ephemeral identity alive.
func TestGarbageCollectKeepsIdentityWithRecentMembership(t *testing.T) {
	t.Setenv(BootstrapTokenEnv, "bootstrap-secret")
	repo := newBootstrapTestRepo(t)
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"pod-a", "pod-b"} {
		_, csr := createClientCSR(t, "ignored")
		raw, err := repo.BootstrapClientCertificate(ctx, "bootstrap-secret", name, csr)
		if err != nil {
			t.Fatal(err)
		}
		var resp bootstrapCertificateResponse
		_ = json.Unmarshal(raw, &resp)
		ids = append(ids, resp.CertificateID)
	}
	time.Sleep(2100 * time.Millisecond)
	now := time.Now().UTC()
	db, _ := repo.database()
	m := CPANodeMembershipRecord{CertificateFingerprint: "fp-a", NodeID: ids[0], State: MembershipStateClosed, LastSeenAt: now.Add(-time.Hour), UpdatedAt: now, ConnectedAt: now.Add(-time.Hour), HomeStartedAt: now.Add(-time.Hour)}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := repo.GarbageCollectEphemeralNodes(ctx, time.Minute/60)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (pod-b only)", removed)
	}
	var count int64
	db.Model(&CertificateRecord{}).Where("id = ?", ids[0]).Count(&count)
	if count != 1 {
		t.Fatal("identity with recent membership was collected")
	}
}
