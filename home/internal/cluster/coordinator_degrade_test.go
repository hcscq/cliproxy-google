package cluster

import (
	"context"
	"testing"
	"time"
)

func init() {
	fatalExitJitter = func() time.Duration { return 0 }
}

func TestCoordinatorDegradesInsteadOfExitingOnStalledDatabase(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	coordinator := NewCoordinator(repo, NodeIdentity{IP: "10.0.0.1", Port: 8317}, CoordinatorOptions{
		HeartbeatInterval: 5 * time.Millisecond,
		HeartbeatTimeout:  3100 * time.Millisecond, // smallest valid vs default CPA lifecycle,
	})
	if errInitialize := coordinator.Initialize(ctx); errInitialize != nil {
		t.Fatal(errInitialize)
	}
	startCtx, cancelStart := context.WithCancel(ctx)
	defer cancelStart()
	startDone := make(chan error, 1)
	go func() { startDone <- coordinator.Start(startCtx) }()
	time.Sleep(30 * time.Millisecond)
	if !coordinator.IsMaster() {
		t.Fatal("expected single coordinator to be master before the stall")
	}

	sqlDB, errSQL := repo.db.DB()
	if errSQL != nil {
		t.Fatal(errSQL)
	}
	_ = sqlDB.Close() // every heartbeat now fails with a transient error

	select {
	case errStart := <-startDone:
		t.Fatalf("Start() exited on transient DB failure: %v", errStart)
	case <-time.After(4 * time.Second): // well past the heartbeat timeout
	}
	if coordinator.IsMaster() {
		t.Fatal("coordinator kept master role while heartbeats were failing")
	}
	cancelStart()
	select {
	case errStart := <-startDone:
		if errStart != nil {
			t.Fatalf("Start() after cancel = %v, want nil", errStart)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after cancel")
	}
}

func TestFatalExitJitterBounded(t *testing.T) {
	saved := fatalExitJitter
	defer func() { fatalExitJitter = saved }()
	fatalExitJitter = func() time.Duration { return 0 }
	if !sleepCtx(context.Background(), fatalExitJitter()) {
		t.Fatal("sleepCtx with zero delay should succeed")
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(cctx, time.Hour) {
		t.Fatal("sleepCtx should abort on canceled context")
	}
}

func TestSetLocalLockTimeoutNoopOnNonPostgres(t *testing.T) {
	repo := newCredentialFoundationTestRepository(t)
	if err := setLocalLockTimeout(repo.db); err != nil {
		t.Fatal(err)
	}
}
