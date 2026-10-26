package home

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

type fakeModelStore struct {
	*fakeReserveStore
	rows       []ModelCapacityRow
	lastLike   string
	activeRule int
}

func (f *fakeModelStore) ReserveModelCapacity(_ context.Context, pattern string) ([]ModelCapacityRow, error) {
	f.lastLike = pattern
	out := make([]ModelCapacityRow, 0, len(f.rows))
	for _, r := range f.rows {
		if _, still := f.reserved[r.ID]; r.Reserved && !still {
			r.Reserved = false
		}
		out = append(out, r)
	}
	return out, nil
}

func TestGlobToLike(t *testing.T) {
	like, prefix := GlobToLike("gemini-4-argon-*")
	if like != "gemini-4-argon-%" || prefix != "gemini-4-argon-" {
		t.Fatalf("%q %q", like, prefix)
	}
	like, prefix = GlobToLike("a_b")
	if like != `a\_b` || prefix != `a_b"` {
		t.Fatalf("%q %q", like, prefix)
	}
}

func TestParseReserveModelRules(t *testing.T) {
	if r := (&config.Config{}).ReserveSettings().ModelRules; len(r) != 1 || r[0].Pattern != "gemini-4-argon-*" || r[0].Min != 6 {
		t.Fatalf("default = %+v", r)
	}
	if r := (&config.Config{ReserveModelRules: "off"}).ReserveSettings().ModelRules; len(r) != 0 {
		t.Fatalf("off = %+v", r)
	}
	if r := config.ParseReserveModelRules("x-*:2, bad, y:0, z*:3"); len(r) != 2 || r[1].Pattern != "z*" {
		t.Fatalf("parsed = %+v", r)
	}
}

func modelTestStore(active int) *fakeModelStore {
	base := &fakeReserveStore{reserved: map[string]time.Time{}, active: active}
	for i, id := range []string{"n1", "n2", "a1", "a2", "n3"} {
		base.reserved[id] = time.Now()
		base.cands = append(base.cands, ReserveCandidate{ID: id, Seq: int64(i + 1), Healthy: true, Health: "pass"})
	}
	return &fakeModelStore{fakeReserveStore: base, rows: []ModelCapacityRow{
		{ID: "x1", Usable: true, Remaining: 0.5},
		{ID: "x2", Usable: false},
		{ID: "a1", Reserved: true, Usable: true, Remaining: 0.3},
		{ID: "a2", Reserved: true, Usable: true, Remaining: 0.9},
	}}
}

func TestReserveModelRuleAlonePromotesCapable(t *testing.T) {
	store := modelTestStore(50) // overall count fine
	r := &Runtime{}
	r.reserveStoreOverride = store
	r.reserveCfgOverride = &config.Config{ReserveMinActive: 10, ReserveModelRules: "gemini-4-argon-*:6"}
	t0 := time.Now()
	r.reserveControl(context.Background(), t0)
	if len(store.promotes) != 0 {
		t.Fatal("must wait for the activation delay")
	}
	r.reserveControl(context.Background(), t0.Add(6*time.Minute))
	if len(store.promotes) != 2 {
		t.Fatalf("promotes = %d", len(store.promotes))
	}
	for _, id := range []string{"a1", "a2"} {
		if _, ok := store.reserved[id]; ok {
			t.Fatalf("%s not promoted", id)
		}
	}
	if _, ok := store.reserved["n1"]; !ok {
		t.Fatal("non-capable reserve must stay when only the model rule is short")
	}
}

func TestReserveGenericPrefersCapableWhileRuleShort(t *testing.T) {
	store := modelTestStore(7)
	r := &Runtime{}
	r.reserveStoreOverride = store
	r.reserveCfgOverride = &config.Config{ReserveMinActive: 10, ReserveMaxPromotions: 2, ReserveActivationDelay: "0s"}
	r.reserveControl(context.Background(), time.Now())
	if len(store.promotes) != 2 {
		t.Fatalf("promotes = %d (cap 2)", len(store.promotes))
	}
	if _, ok := store.reserved["a2"]; ok {
		t.Fatal("highest remaining argon reserve first")
	}
	if _, ok := store.reserved["a1"]; ok {
		t.Fatal("second argon reserve next")
	}
	if _, ok := store.reserved["n1"]; !ok {
		t.Fatal("generic order only after capable ones")
	}
}
