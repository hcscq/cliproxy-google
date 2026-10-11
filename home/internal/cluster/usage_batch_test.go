package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A batch must store every row and charge billing exactly like sequential
// writes (each charge sees the previous balance), skipping bad payloads.
func TestAppendUsageBatchMatchesSequentialBilling(t *testing.T) {
	ctx := context.Background()
	repo, closeRepo := newBillingTestRepository(t, ctx)
	defer closeRepo()

	username := "batch@example.com"
	credits := 10.0
	user, err := repo.CreateUser(ctx, UserUpdate{Username: &username, Credits: &credits})
	if err != nil {
		t.Fatal(err)
	}
	key := "batch-key"
	if _, err := repo.CreateAPIKeyForUser(ctx, user.ID, APIKeyUserUpdate{APIKey: &key}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateBillingModelPrice(ctx, BillingModelPriceUpdate{Provider: "openai", Model: "gpt-4.1-mini", InputPricePerMillion: 2, OutputPricePerMillion: 8, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var items []UsagePayloadItem
	for i := 0; i < 3; i++ {
		items = append(items, UsagePayloadItem{Payload: fmt.Sprintf(`{"timestamp":"2026-06-10T01:02:0%dZ","provider":"openai","model":"gpt-4.1-mini","endpoint":"/v1/chat/completions","api_key":"batch-key","request_id":"req-%d","tokens":{"input_tokens":100000,"output_tokens":100000,"total_tokens":200000}}`, i, i)})
	}
	items = append(items, UsagePayloadItem{Payload: "not json"}, UsagePayloadItem{Payload: "  "})
	stored, err := repo.AppendUsageBatch(ctx, items)
	if err != nil || stored != 3 {
		t.Fatalf("stored=%d err=%v want 3", stored, err)
	}
	var n int64
	repo.db.Model(&UsageRecord{}).Count(&n)
	if n != 3 {
		t.Fatalf("usage rows=%d want 3", n)
	}
	charges, err := repo.ListBillingCharges(ctx, BillingChargeQuery{UserID: &user.ID, Limit: 10})
	if err != nil || len(charges.Records) != 3 {
		t.Fatalf("charges=%d err=%v want 3", len(charges.Records), err)
	}
	// Each charge is 0.2 + 0.8 = 1.0; balances must chain 10 -> 9 -> 8 -> 7.
	seen := map[float64]bool{}
	for _, c := range charges.Records {
		if c.Amount != 1 || c.BalanceBefore-c.BalanceAfter != 1 {
			t.Fatalf("charge %+v", c)
		}
		seen[c.BalanceAfter] = true
	}
	for _, want := range []float64{9, 8, 7} {
		if !seen[want] {
			t.Fatalf("balance chain missing %v: %+v", want, seen)
		}
	}
}

func TestHeartbeatErrorTransient(t *testing.T) {
	if heartbeatErrorTransient(ErrHomeIncarnationFenced) || heartbeatErrorTransient(fmt.Errorf("wrap: %w", ErrHomeIncarnationFenced)) {
		t.Fatal("fenced must be fatal")
	}
	if heartbeatErrorTransient(ErrHomeIncarnationInactive) || heartbeatErrorTransient(ErrHomeIncarnationNotFound) {
		t.Fatal("inactive / not found must be fatal")
	}
	if !heartbeatErrorTransient(context.DeadlineExceeded) || !heartbeatErrorTransient(errors.New("conn reset")) {
		t.Fatal("timeouts / connection errors must be retried")
	}
	if maxDuration(time.Second, 2*time.Second) != 2*time.Second {
		t.Fatal("maxDuration")
	}
}
