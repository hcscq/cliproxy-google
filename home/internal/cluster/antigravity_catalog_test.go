package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	"gorm.io/gorm"
)

func TestAntigravityCatalogRowRoundTrip(t *testing.T) {
	db, errOpen := gorm.Open(sqlite.Open("file:agcat?mode=memory&cache=shared"), &gorm.Config{})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	adapter := &RuntimeAdapter{repo: &Repository{db: db}}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	row := home.AntigravityCatalogRow{AuthID: "a1", Raw: []byte(`{"models":{}}`), FetchedAt: now, AttemptedAt: now, Status: "ok", ModelCount: 0, Removed: `{"x":"2026-10-11T05:00:00Z"}`, Trigger: "timer", FetchedBy: "home-0"}
	if err := adapter.SaveAntigravityCatalog(ctx, row); err != nil {
		t.Fatal(err)
	}
	row.Status, row.Error, row.Trigger = "error", "boom", "manual"
	if err := adapter.SaveAntigravityCatalog(ctx, row); err != nil {
		t.Fatal(err)
	}
	got, err := adapter.LoadAntigravityCatalogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := got["a1"]
	if len(got) != 1 || g.Status != "error" || g.Error != "boom" || g.Trigger != "manual" || string(g.Raw) != `{"models":{}}` || g.Removed == "" || !g.FetchedAt.Equal(now) {
		t.Fatalf("unexpected %#v", g)
	}
}
