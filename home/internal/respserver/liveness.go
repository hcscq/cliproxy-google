package respserver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	log "github.com/sirupsen/logrus"
)

// subscriptionLivenessAttempts is the initial refresh plus two short retries.
// A single lock_timeout must not fence the subscription.
const subscriptionLivenessAttempts = 3

// subscriptionLivenessRetryDelay is the pause between lock-timeout retries.
// The database lock_timeout already bounded the failed attempt.
var subscriptionLivenessRetryDelay = 25 * time.Millisecond

// subscriptionLivenessState tracks one subscription goroutine's DB refresh.
// After the first successful refresh, pings continue while a later refresh
// waits on a row lock.
type subscriptionLivenessState struct {
	mu       sync.Mutex
	ready    bool
	inflight bool
}

func (s *subscriptionLivenessState) confirmed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func (s *subscriptionLivenessState) confirm() {
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
}

func (s *subscriptionLivenessState) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight {
		return false
	}
	s.inflight = true
	return true
}

func (s *subscriptionLivenessState) end() {
	s.mu.Lock()
	s.inflight = false
	s.mu.Unlock()
}

func (s *Server) refreshSubscriptionLiveness(ctx context.Context, lifetime cluster.ConnectionLifetime) error {
	if s == nil || s.cluster == nil {
		return fmt.Errorf("cluster lifecycle unavailable")
	}
	var errRefresh error
	for attempt := 0; attempt < subscriptionLivenessAttempts; attempt++ {
		if ctx != nil && ctx.Err() != nil {
			if errRefresh != nil {
				return errRefresh
			}
			return ctx.Err()
		}
		if attempt > 0 {
			if errWait := waitSubscriptionLivenessRetry(ctx); errWait != nil {
				if errRefresh != nil {
					return errRefresh
				}
				return errWait
			}
		}
		errRefresh = s.cluster.RefreshCPALiveness(ctx, lifetime)
		if errRefresh == nil || !cluster.IsRetriableLockError(errRefresh) {
			return errRefresh
		}
	}
	return errRefresh
}

func waitSubscriptionLivenessRetry(ctx context.Context) error {
	delay := subscriptionLivenessRetryDelay
	if delay <= 0 {
		if ctx == nil {
			return nil
		}
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	if ctx == nil {
		<-timer.C
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Server) failSubscriptionLiveness(ctx context.Context, lifetime cluster.ConnectionLifetime, errLiveness error) {
	if errLiveness == nil {
		return
	}
	log.Warnf("failed to refresh subscription liveness: %v", errLiveness)
	if s == nil || s.fingerprints == nil {
		return
	}
	fenceCtx := ctx
	if fenceCtx == nil {
		fenceCtx = context.Background()
	}
	if errFence := s.fingerprints.Fence(context.WithoutCancel(fenceCtx), lifetime, s.fingerprints.LatestFenceRevision(lifetime)); errFence != nil {
		log.Warnf("failed to fence subscription after liveness failure: %v", errFence)
	}
}
