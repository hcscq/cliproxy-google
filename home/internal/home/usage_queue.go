package home

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
	log "github.com/sirupsen/logrus"
)

type usagePayloadItem struct {
	payload    string
	receivedAt time.Time
}

type usagePayloadQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []usagePayloadItem
	head   int
	closed bool
}

func newUsagePayloadQueue() *usagePayloadQueue {
	q := &usagePayloadQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *usagePayloadQueue) Push(payload string, receivedAt time.Time) bool {
	if q == nil {
		return false
	}
	if receivedAt.IsZero() {
		receivedAt = time.Now().UTC()
	} else {
		receivedAt = receivedAt.UTC()
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.items = append(q.items, usagePayloadItem{payload: payload, receivedAt: receivedAt})
	q.cond.Signal()
	return true
}

func (q *usagePayloadQueue) Pop() (usagePayloadItem, bool) {
	if q == nil {
		return usagePayloadItem{}, false
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	for q.head >= len(q.items) && !q.closed {
		q.cond.Wait()
	}
	if q.head >= len(q.items) {
		return usagePayloadItem{}, false
	}

	item := q.items[q.head]
	q.items[q.head] = usagePayloadItem{}
	q.head++
	q.compactLocked()
	return item, true
}

func (q *usagePayloadQueue) Close() {
	if q == nil {
		return
	}

	// Pending items stay queued so the writer can drain them before exiting
	// (they used to be discarded on shutdown).
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// PopBatch blocks until at least one item is queued (or the queue is closed
// and drained) and returns up to max items.
func (q *usagePayloadQueue) PopBatch(max int) ([]usagePayloadItem, bool) {
	if q == nil {
		return nil, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.head >= len(q.items) && !q.closed {
		q.cond.Wait()
	}
	n := len(q.items) - q.head
	if n == 0 {
		return nil, false
	}
	if n > max {
		n = max
	}
	out := make([]usagePayloadItem, n)
	copy(out, q.items[q.head:q.head+n])
	for i := q.head; i < q.head+n; i++ {
		q.items[i] = usagePayloadItem{}
	}
	q.head += n
	q.compactLocked()
	return out, true
}

// Len returns the number of queued items.
func (q *usagePayloadQueue) Len() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items) - q.head
}

func (q *usagePayloadQueue) compactLocked() {
	if q.head < 1024 || q.head*2 < len(q.items) {
		return
	}
	next := append([]usagePayloadItem(nil), q.items[q.head:]...)
	q.items = next
	q.head = 0
}

func (r *Runtime) startClusterUsageWriter(ctx context.Context) {
	if r == nil || r.clusterAdapter == nil || !r.clusterAdapter.Enabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store, ok := r.clusterAdapter.(clusterUsageStore)
	if !ok || store == nil {
		log.Errorf("cluster usage store is unavailable")
		return
	}

	queue := newUsagePayloadQueue()
	r.clusterUsageQueueMu.Lock()
	if r.clusterUsageQueue != nil {
		r.clusterUsageQueueMu.Unlock()
		return
	}
	r.clusterUsageQueue = queue
	done := make(chan struct{})
	r.clusterUsageDone = done
	r.clusterUsageQueueMu.Unlock()

	go func() {
		<-ctx.Done()
		queue.Close()
	}()
	go func() {
		defer close(done)
		r.runClusterUsageWriter(ctx, store, queue)
	}()
	r.startUsageStream(ctx)
}

// startUsageStream enables Redis-stream ingestion of usage (HOME_USAGE_STREAM=1)
// when Redis is configured; otherwise usage keeps the in-memory queue path.
func (r *Runtime) startUsageStream(ctx context.Context) {
	if !usageStreamEnabled() {
		return
	}
	rc := redisx.Default()
	itemsStore, ok := r.clusterAdapter.(usageItemsStore)
	if rc == nil || !ok {
		log.Warn("usage stream requested but Redis or the batch store is unavailable; using the in-memory queue")
		return
	}
	st := newUsageStream(rc, itemsStore, "")
	st.fallbackPush = func(payload string, receivedAt time.Time) bool {
		q := r.getClusterUsageQueue()
		return q != nil && q.Push(payload, receivedAt)
	}
	r.clusterUsageQueueMu.Lock()
	r.usageStream = st
	r.clusterUsageQueueMu.Unlock()
	go st.runAppender()
	go func() {
		// Flush buffered payloads into Redis (or the fallback) on shutdown.
		<-ctx.Done()
		st.Close(5 * time.Second)
	}()
	go st.Run(ctx)
	// Extra consumers share the appender/counters but read and write in parallel
	// (each batch is one PG transaction; through pgbouncer one writer cannot keep up).
	workers := usageStreamWorkers()
	for i := 1; i < workers; i++ {
		go st.runWorker(ctx, fmt.Sprintf("%s-w%d", st.consumer, i))
	}
	log.Infof("usage stream enabled (consumer %s, %d workers, maxlen %d)", st.consumer, workers, st.maxLen)
}

func (r *Runtime) getUsageStream() *usageStream {
	if r == nil {
		return nil
	}
	r.clusterUsageQueueMu.Lock()
	defer r.clusterUsageQueueMu.Unlock()
	return r.usageStream
}

// UsageStreamStatus reports the usage stream for the panel (Enabled=false when off).
func (r *Runtime) UsageStreamStatus(ctx context.Context) UsageStreamStatus {
	if st := r.getUsageStream(); st != nil {
		return st.Status(ctx)
	}
	return UsageStreamStatus{}
}

func (r *Runtime) stopClusterUsageWriter() {
	if r == nil {
		return
	}

	r.clusterUsageQueueMu.Lock()
	queue := r.clusterUsageQueue
	done := r.clusterUsageDone
	stream := r.usageStream
	r.clusterUsageQueue = nil
	r.clusterUsageDone = nil
	r.usageStream = nil
	r.clusterUsageQueueMu.Unlock()
	stream.Close(5 * time.Second)
	if queue != nil {
		queue.Close()
	}
	if done != nil {
		// Wait for queued usage to be flushed (bounded) so a rolling update
		// does not lose request-log / billing rows.
		select {
		case <-done:
		case <-time.After(usageWriterDrainLimit + time.Second):
			log.Warn("usage writer did not finish draining before shutdown")
		}
	}
}

func (r *Runtime) getClusterUsageQueue() *usagePayloadQueue {
	if r == nil {
		return nil
	}

	r.clusterUsageQueueMu.Lock()
	defer r.clusterUsageQueueMu.Unlock()
	return r.clusterUsageQueue
}

// clusterUsageBatchStore is implemented by stores that can write many usage
// payloads in one transaction.
type clusterUsageBatchStore interface {
	StoreUsagePayloads(ctx context.Context, payloads []string, receivedAt []time.Time) (int, error)
}

const (
	usageWriterBatchMax   = 500
	usageWriterLinger     = 100 * time.Millisecond
	usageWriterDrainLimit = 20 * time.Second
)

func (r *Runtime) runClusterUsageWriter(ctx context.Context, store clusterUsageStore, queue *usagePayloadQueue) {
	if store == nil || queue == nil {
		return
	}
	batchStore, batched := store.(clusterUsageBatchStore)
	writeCtx := func() (context.Context, context.CancelFunc) {
		// Keep writing (drain) after shutdown starts, but bounded.
		if ctx.Err() != nil {
			return context.WithTimeout(context.Background(), usageWriterDrainLimit)
		}
		return context.WithCancel(ctx)
	}
	for {
		items, ok := queue.PopBatch(usageWriterBatchMax)
		if !ok {
			return
		}
		if batched && len(items) < usageWriterBatchMax && ctx.Err() == nil {
			// Let a small batch fill a little so bursts become one transaction.
			time.Sleep(usageWriterLinger)
			if more, okMore := queue.popNoWait(usageWriterBatchMax - len(items)); okMore {
				items = append(items, more...)
			}
		}
		payloads := make([]string, 0, len(items))
		times := make([]time.Time, 0, len(items))
		for _, item := range items {
			if strings.TrimSpace(item.payload) == "" {
				continue
			}
			payloads = append(payloads, item.payload)
			times = append(times, item.receivedAt)
		}
		if len(payloads) == 0 {
			continue
		}
		wctx, cancel := writeCtx()
		if batched {
			if _, err := batchStore.StoreUsagePayloads(wctx, payloads, times); err != nil {
				log.Errorf("usage database batch write error: %v", err)
			}
		} else {
			for i := range payloads {
				if err := store.StoreUsagePayload(wctx, payloads[i], times[i]); err != nil {
					log.Errorf("usage database async write error: %v", err)
				}
			}
		}
		cancel()
	}
}

// popNoWait returns up to max queued items without blocking.
func (q *usagePayloadQueue) popNoWait(max int) ([]usagePayloadItem, bool) {
	q.mu.Lock()
	empty := q.head >= len(q.items)
	q.mu.Unlock()
	if empty || max <= 0 {
		return nil, false
	}
	return q.PopBatch(max)
}
