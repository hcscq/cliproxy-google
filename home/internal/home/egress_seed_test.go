package home

import (
	"context"
	"testing"
	"time"
)

type fakePoolRepo struct {
	items   []EgressPoolItem
	deleted []string
}

func (f *fakePoolRepo) ListEgressPool(context.Context) ([]EgressPoolItem, error) { return f.items, nil }
func (f *fakePoolRepo) CreateEgressPool(_ context.Context, it EgressPoolItem) error {
	f.items = append(f.items, it)
	return nil
}
func (f *fakePoolRepo) UpdateEgressPool(context.Context, EgressPoolItem) error { return nil }
func (f *fakePoolRepo) MarkEgressResult(context.Context, string, bool, time.Time) error {
	return nil
}
func (f *fakePoolRepo) DeleteEgressPool(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func TestSeedEgressPoolDropsStale(t *testing.T) {
	repo := &fakePoolRepo{items: []EgressPoolItem{
		{ID: "old", Name: "egress-n", Scope: EgressScope},
		{ID: "keep", Name: "n/1", ProxyURL: "http://1@h:3141", Scope: EgressScope, Note: "egress IP 1", Enabled: true},
		{ID: "user", Name: "mine", Scope: "global"},
	}}
	r := &Runtime{}
	r.SetEgressRepo(repo)
	if err := r.seedEgressPool(context.Background(), []EgressSpec{{Name: "n/1", ProxyURL: "http://1@h:3141", IP: "1"}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != "old" {
		t.Fatalf("deleted %v", repo.deleted)
	}
}
