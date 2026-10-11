package redisqueue

import (
	"fmt"
	"testing"
)

func TestQueueBoundsDropOldestAndRequeueFront(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	oldItems, oldBytes := maxQueueItems, maxQueueBytes
	maxQueueItems, maxQueueBytes = 5, 1<<20
	defer func() { maxQueueItems, maxQueueBytes = oldItems, oldBytes }()
	before := droppedFull.Load()

	for i := 0; i < 8; i++ {
		Enqueue([]byte(fmt.Sprintf("p%d", i)))
	}
	if got := droppedFull.Load() - before; got != 3 {
		t.Fatalf("dropped = %d, want 3", got)
	}
	items := PopOldest(2)
	if string(items[0]) != "p3" || string(items[1]) != "p4" {
		t.Fatalf("oldest = %q, want p3,p4", items)
	}
	RequeueFront(items)
	all := PopOldest(10)
	want := []string{"p3", "p4", "p5", "p6", "p7"}
	if len(all) != len(want) {
		t.Fatalf("len = %d, want %d", len(all), len(want))
	}
	for i := range want {
		if string(all[i]) != want[i] {
			t.Fatalf("order = %q, want %v", all, want)
		}
	}
	if st := UsageStats(); st.Queued != 0 || st.Bytes != 0 {
		t.Fatalf("stats after drain = %+v", st)
	}
}

func TestQueueByteBound(t *testing.T) {
	SetEnabled(true)
	defer SetEnabled(false)
	oldItems, oldBytes := maxQueueItems, maxQueueBytes
	maxQueueItems, maxQueueBytes = 1000, 10
	defer func() { maxQueueItems, maxQueueBytes = oldItems, oldBytes }()
	for i := 0; i < 4; i++ {
		Enqueue([]byte("abcd"))
	}
	if st := UsageStats(); st.Queued != 2 || st.Bytes != 8 {
		t.Fatalf("stats = %+v, want 2 items / 8 bytes", st)
	}
}
