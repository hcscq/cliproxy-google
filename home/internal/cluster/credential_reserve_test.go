package cluster

import (
	"testing"
	"time"
)

func TestCredentialReserveRoundTrip(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if err := db.Exec(`INSERT INTO auth (uuid, auth_json, id, status, disabled, created_at) VALUES (?, '{}', ?, 'active', false, ?)`, id, id, time.Now()).Error; err != nil {
			t.Fatal(err)
		}
	}
	n, err := repo.SetCredentialsReserved(ctx, []string{"a1", "a2", "a2", "missing-ok"}, true, "reserve", "t")
	if err != nil || n != 3 {
		t.Fatalf("reserve n=%d err=%v", n, err)
	}
	got, err := repo.ListReservedCredentials(ctx)
	if err != nil || len(got) != 2 || got["a1"].IsZero() {
		t.Fatalf("list = %v err=%v (only live auths)", got, err)
	}
	if active, _ := repo.CountActiveCredentials(ctx); active != 1 {
		t.Fatalf("active = %d", active)
	}
	cands, err := repo.ListReserveCandidates(ctx)
	if err != nil || len(cands) != 2 {
		t.Fatalf("cands = %v err=%v", cands, err)
	}
	if n, _ := repo.SetCredentialsReserved(ctx, []string{"a1"}, false, "promote", "auto"); n != 1 {
		t.Fatal("promote")
	}
	if n, _ := repo.SetCredentialsReserved(ctx, []string{"a3"}, false, "unreserve", ""); n != 0 {
		t.Fatal("unreserving a never-reserved credential must be a no-op")
	}
	if c, _ := repo.CountReserveEvents(ctx, "promote", time.Now().Add(-time.Minute)); c != 1 {
		t.Fatalf("promote events = %d", c)
	}
	evs, _ := repo.RecentReserveEvents(ctx, 10)
	if len(evs) != 4 || evs[0].Kind != "promote" || !evs[0].WasReserved || evs[0].ReservedSince == nil {
		t.Fatalf("events = %+v", evs)
	}
	if active, _ := repo.CountActiveCredentials(ctx); active != 2 {
		t.Fatalf("active after promote = %d", active)
	}
}
