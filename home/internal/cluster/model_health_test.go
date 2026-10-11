package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestModelHealthCheckUpsertAndList(t *testing.T) {
	db, errOpen := gorm.Open(sqlite.Open("file:mh?mode=memory&cache=shared"), &gorm.Config{})
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	repo := &Repository{db: db}
	ctx := context.Background()
	if err := repo.UpsertModelHealthCheck(ctx, ModelHealthCheckRecord{Model: "gemini-3.1-pro-high", Outcome: "rejected", HTTPStatus: 400, Message: "invalid"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertModelHealthCheck(ctx, ModelHealthCheckRecord{Model: "gemini-3.1-pro-high", Outcome: "ok", HTTPStatus: 200, CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListModelHealthChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["gemini-3.1-pro-high"].Outcome != "ok" || got["gemini-3.1-pro-high"].HTTPStatus != 200 {
		t.Fatalf("unexpected: %#v", got)
	}
}
