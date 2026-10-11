package cluster

import (
	"context"
	"testing"
	"time"
)

// A redelivered stream entry whose row was already committed (crash between
// commit and XACK) must not be stored or billed twice; a fresh entry with an
// unrelated stream id is stored.
func TestAppendUsageBatchSkipsRedeliveredStreamEntries(t *testing.T) {
	ctx := context.Background()
	repo, closeRepo := newBillingTestRepository(t, ctx)
	defer closeRepo()

	payload := `{"timestamp":"2026-06-10T01:02:03.123456789Z","provider":"openai","model":"m","endpoint":"/v1/chat/completions","request_id":"r1","tokens":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	meta := func(id string) UsageRuntimeMetadata {
		return UsageRuntimeMetadata{ReceivedAt: time.Now().UTC(), StreamID: id}
	}
	if n, err := repo.AppendUsageBatch(ctx, []UsagePayloadItem{{Payload: payload, Metadata: meta("1-0")}}); err != nil || n != 1 {
		t.Fatalf("first write n=%d err=%v", n, err)
	}
	n, err := repo.AppendUsageBatch(ctx, []UsagePayloadItem{
		{Payload: payload, Metadata: meta("1-0"), Redelivered: true},
		{Payload: payload, Metadata: meta("2-0"), Redelivered: true},
	})
	if err != nil || n != 1 {
		t.Fatalf("redelivered write n=%d err=%v want 1", n, err)
	}
	var rows int64
	repo.db.Model(&UsageRecord{}).Count(&rows)
	if rows != 2 {
		t.Fatalf("rows=%d want 2", rows)
	}
	var sid string
	repo.db.Model(&UsageRecord{}).Order("id").Limit(1).Pluck("stream_id", &sid)
	if sid != "1-0" {
		t.Fatalf("stream_id=%q", sid)
	}
}
