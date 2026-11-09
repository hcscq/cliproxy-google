package cluster

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

type sqlStateErr struct {
	state string
	msg   string
}

func (e sqlStateErr) Error() string { return e.msg }

func (e sqlStateErr) SQLState() string { return e.state }

func TestIsRetriableLockError(t *testing.T) {
	if !IsRetriableLockError(sqlStateErr{state: "55P03", msg: "canceling statement due to lock timeout"}) {
		t.Fatal("lock_timeout SQLSTATE should retry")
	}
	if !IsRetriableLockError(sqlStateErr{state: "40P01", msg: "deadlock detected"}) {
		t.Fatal("deadlock SQLSTATE should retry")
	}
	if !IsRetriableLockError(errors.New("ERROR: could not obtain lock on row")) {
		t.Fatal("could not obtain lock should retry")
	}
	if IsRetriableLockError(errors.New("canceling statement due to user request")) {
		t.Fatal("client cancellation is not a lock retry")
	}
	if IsRetriableLockError(ErrHomeIncarnationFenced) {
		t.Fatal("fenced home is not a lock retry")
	}
	if IsRetriableLockError(context.Canceled) {
		t.Fatal("canceled context is not a lock retry")
	}
}

func TestRefreshCPALivenessSkipsRecentWrite(t *testing.T) {
	ctx := context.Background()
	repo, home, member := newQuiescenceMembership(t, ctx, "fp-liveness-cadence")
	lifetime := ConnectionLifetime{
		Fingerprint:  member.CertificateFingerprint,
		ConnectedAt:  member.ConnectedAt,
		Home:         home,
		Subscription: true,
	}

	var lockTimeouts atomic.Int32
	original := setLocalLockTimeoutFn
	setLocalLockTimeoutFn = func(tx *gorm.DB) error {
		lockTimeouts.Add(1)
		return original(tx)
	}
	t.Cleanup(func() { setLocalLockTimeoutFn = original })

	var writes atomic.Int32
	const callbackName = "test:count_cpa_liveness_writes"
	if errRegister := repo.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		table := tx.Statement.Table
		if table == "" && tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		if table == (CPANodeMembershipRecord{}).TableName() {
			writes.Add(1)
		}
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { repo.db.Callback().Update().Remove(callbackName) })

	now := time.Unix(1_700_000_000, 0).UTC()
	repo.livenessNow = func() time.Time { return now }

	if errRefresh := repo.RefreshCPALiveness(ctx, lifetime); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if writes.Load() != 1 || lockTimeouts.Load() != 1 {
		t.Fatalf("first refresh writes=%d lock_timeouts=%d, want 1 and 1", writes.Load(), lockTimeouts.Load())
	}

	other := lifetime
	other.ConnectedAt = lifetime.ConnectedAt.Add(time.Second)
	if errRefresh := repo.RefreshCPALiveness(ctx, other); !errors.Is(errRefresh, ErrMembershipNotActive) {
		t.Fatalf("other lifetime refresh error = %v, want %v", errRefresh, ErrMembershipNotActive)
	}
	if writes.Load() != 1 || lockTimeouts.Load() != 2 {
		t.Fatalf("after other lifetime writes=%d lock_timeouts=%d, want 1 and 2", writes.Load(), lockTimeouts.Load())
	}

	now = now.Add(SubscriptionLivenessWriteInterval - time.Nanosecond)
	if errRefresh := repo.RefreshCPALiveness(ctx, lifetime); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if writes.Load() != 1 || lockTimeouts.Load() != 2 {
		t.Fatalf("recent refresh wrote again: writes=%d lock_timeouts=%d", writes.Load(), lockTimeouts.Load())
	}

	now = now.Add(time.Nanosecond)
	if errRefresh := repo.RefreshCPALiveness(ctx, lifetime); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if writes.Load() != 2 || lockTimeouts.Load() != 3 {
		t.Fatalf("refresh at the interval writes=%d lock_timeouts=%d, want 2 and 3", writes.Load(), lockTimeouts.Load())
	}
}

func TestRefreshCPALivenessLockTimeoutDoesNotSuppressRetry(t *testing.T) {
	ctx := context.Background()
	repo, home, member := newQuiescenceMembership(t, ctx, "fp-liveness-lock-retry")
	lifetime := ConnectionLifetime{
		Fingerprint:  member.CertificateFingerprint,
		ConnectedAt:  member.ConnectedAt,
		Home:         home,
		Subscription: true,
	}
	var failed atomic.Bool
	const callbackName = "test:fail_cpa_liveness_once"
	if errRegister := repo.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		table := tx.Statement.Table
		if table == "" && tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		if table != (CPANodeMembershipRecord{}).TableName() {
			return
		}
		if failed.CompareAndSwap(false, true) {
			_ = tx.AddError(errors.New("canceling statement due to lock timeout"))
		}
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { repo.db.Callback().Update().Remove(callbackName) })

	errRefresh := repo.RefreshCPALiveness(ctx, lifetime)
	if !IsRetriableLockError(errRefresh) {
		t.Fatalf("first refresh error = %v, want lock timeout", errRefresh)
	}
	if errRetry := repo.RefreshCPALiveness(ctx, lifetime); errRetry != nil {
		t.Fatalf("retry error = %v", errRetry)
	}
}

func TestCleanupStaleMembershipToleratesLivenessSampleGap(t *testing.T) {
	ctx := context.Background()
	repo, _, member := newQuiescenceMembership(t, ctx, "fp-liveness-stale-window")
	window := CPALivenessStaleWindow(member.CPAHeartbeatTimeout)
	if window <= SubscriptionLivenessWriteInterval {
		t.Fatalf("stale window %s must exceed the write interval %s", window, SubscriptionLivenessWriteInterval)
	}

	recent := time.Now().UTC().Add(-SubscriptionLivenessWriteInterval)
	if errUpdate := repo.db.Model(&CPANodeMembershipRecord{}).Where("certificate_fingerprint = ?", member.CertificateFingerprint).Update("last_seen_at", recent).Error; errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errCleanup := repo.CleanupStaleMemberships(ctx); errCleanup != nil {
		t.Fatal(errCleanup)
	}
	assertMembershipState(t, repo, member.CertificateFingerprint, MembershipStateActive)

	stale := time.Now().UTC().Add(-window - time.Second)
	if errUpdate := repo.db.Model(&CPANodeMembershipRecord{}).Where("certificate_fingerprint = ?", member.CertificateFingerprint).Update("last_seen_at", stale).Error; errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errCleanup := repo.CleanupStaleMemberships(ctx); errCleanup != nil {
		t.Fatal(errCleanup)
	}
	assertMembershipState(t, repo, member.CertificateFingerprint, MembershipStateCanceling)
}
