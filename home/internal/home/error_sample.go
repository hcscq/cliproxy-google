package home

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/errorsamples"
	log "github.com/sirupsen/logrus"
)

type errorSampleStore interface {
	StoreErrorSample(ctx context.Context, rec *errorsamples.Record, raw []byte, keep, maxTypes int) error
}

type errorSampleJob struct {
	rec            *errorsamples.Record
	raw            []byte
	keep, maxTypes int
}

var (
	errorSampleOnce  sync.Once
	errorSampleQueue chan errorSampleJob
)

// EnqueueErrorSample queues a sample for an async database write. It never
// blocks: when the queue is full the sample is dropped (returns false).
func (r *Runtime) EnqueueErrorSample(rec *errorsamples.Record, raw []byte, keep, maxTypes int) bool {
	if r == nil || r.clusterAdapter == nil || !r.clusterAdapter.Enabled() {
		return false
	}
	store, ok := r.clusterAdapter.(errorSampleStore)
	if !ok || store == nil {
		return false
	}
	errorSampleOnce.Do(func() {
		errorSampleQueue = make(chan errorSampleJob, 16)
		go func() {
			for job := range errorSampleQueue {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				if err := store.StoreErrorSample(ctx, job.rec, job.raw, job.keep, job.maxTypes); err != nil {
					log.Warnf("error sample store failed: %v", err)
				}
				cancel()
			}
		}()
	})
	select {
	case errorSampleQueue <- errorSampleJob{rec: rec, raw: raw, keep: keep, maxTypes: maxTypes}:
		return true
	default:
		return false
	}
}
