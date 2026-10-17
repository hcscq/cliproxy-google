package home

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	log "github.com/sirupsen/logrus"
)

type fakeReserveStore struct {
	reserved map[string]time.Time
	cands    []ReserveCandidate
	active   int
	promotes []time.Time
	bans     int
}

func (f *fakeReserveStore) ListReservedCredentials(context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	for k, v := range f.reserved {
		out[k] = v
	}
	return out, nil
}
func (f *fakeReserveStore) SetCredentialsReserved(_ context.Context, ids []string, reserved bool, kind, _ string) (int, error) {
	n := 0
	for _, id := range ids {
		_, was := f.reserved[id]
		if was == reserved {
			continue
		}
		n++
		if reserved {
			f.reserved[id] = time.Now()
		} else {
			delete(f.reserved, id)
			f.active++
			if kind == "promote" {
				f.promotes = append(f.promotes, time.Now())
			}
			for i, c := range f.cands {
				if c.ID == id {
					f.cands = append(f.cands[:i], f.cands[i+1:]...)
					break
				}
			}
		}
	}
	return n, nil
}
func (f *fakeReserveStore) ListReserveCandidates(context.Context) ([]ReserveCandidate, error) {
	return append([]ReserveCandidate(nil), f.cands...), nil
}
func (f *fakeReserveStore) CountActiveCredentials(context.Context) (int, error) { return f.active, nil }
func (f *fakeReserveStore) CountReserveEvents(_ context.Context, _ string, since time.Time) (int, error) {
	n := 0
	for _, t := range f.promotes {
		if !t.Before(since) {
			n++
		}
	}
	return n, nil
}
func (f *fakeReserveStore) RecordReserveBans(context.Context) (int, error) { f.bans++; return 0, nil }

func TestOrderReserveCandidates(t *testing.T) {
	base := time.Now()
	in := []ReserveCandidate{
		{ID: "new-healthy", Seq: 9, Healthy: true, Tier: "g1-pro-tier", CreatedAt: base},
		{ID: "old-sick", Seq: 1, Healthy: false, Tier: "g1-pro-tier", CreatedAt: base},
		{ID: "old-healthy", Seq: 2, Healthy: true, Tier: "free-tier", CreatedAt: base},
	}
	got := orderReserveCandidates(in, nil, "any")
	if got[0].ID != "old-healthy" || got[1].ID != "new-healthy" || got[2].ID != "old-sick" {
		t.Fatalf("order = %v", got)
	}
	got = orderReserveCandidates(in, []string{"pro"}, "any")
	if len(got) != 2 || got[0].ID != "new-healthy" {
		t.Fatalf("tier filter = %v", got)
	}
}

func TestReserveSettingsDefaults(t *testing.T) {
	s := (&config.Config{}).ReserveSettings()
	if s.MinActive != 0 || s.ActivationDelay != 5*time.Minute || s.MaxPromotions != 3 || s.PromotionWindow != time.Hour || len(s.Tiers) != 0 {
		t.Fatalf("defaults = %+v", s)
	}
	s = (&config.Config{ReserveMinActive: 10, ReserveActivationDelay: "0s", ReserveTierFilter: "Pro, ultra,bogus"}).ReserveSettings()
	if s.MinActive != 10 || s.ActivationDelay != 0 || len(s.Tiers) != 2 {
		t.Fatalf("parsed = %+v", s)
	}
}

func reserveTestRuntime(store *fakeReserveStore, cfg *config.Config) *Runtime {
	r := &Runtime{}
	r.reserveStoreOverride = store
	r.reserveCfgOverride = cfg
	return r
}

func TestReserveControlDelayAndRateLimit(t *testing.T) {
	store := &fakeReserveStore{reserved: map[string]time.Time{}, active: 2}
	for i, id := range []string{"r1", "r2", "r3", "r4", "r5"} {
		store.reserved[id] = time.Now()
		store.cands = append(store.cands, ReserveCandidate{ID: id, Seq: int64(i + 1), Healthy: true, Health: "pass"})
	}
	r := reserveTestRuntime(store, &config.Config{ReserveMinActive: 6, ReserveActivationDelay: "5m", ReserveMaxPromotions: 2, ReservePromotionWindow: "1h"})
	defer coreauth.SetReservedCredentials(nil)
	now := time.Now()
	r.reserveControl(context.Background(), now)
	if len(store.promotes) != 0 {
		t.Fatal("must wait for the activation delay")
	}
	r.reserveControl(context.Background(), now.Add(6*time.Minute))
	if len(store.promotes) != 2 || store.active != 4 {
		t.Fatalf("promotes=%d active=%d, want 2/4 (rate limit)", len(store.promotes), store.active)
	}
	if _, ok := store.reserved["r1"]; ok {
		t.Fatal("oldest must be promoted first")
	}
	r.reserveControl(context.Background(), now.Add(7*time.Minute))
	if len(store.promotes) != 2 {
		t.Fatal("rate limit must hold within the window")
	}
	if !coreauth.IsCredentialReserved("r3") || coreauth.IsCredentialReserved("r1") {
		t.Fatal("scheduler set must follow promotions")
	}
}

func TestReserveControlOffAndRecovered(t *testing.T) {
	store := &fakeReserveStore{reserved: map[string]time.Time{"r1": time.Now()}, cands: []ReserveCandidate{{ID: "r1", Healthy: true, Health: "pass"}}, active: 1}
	r := reserveTestRuntime(store, &config.Config{})
	r.reserveControl(context.Background(), time.Now().Add(time.Hour))
	if len(store.promotes) != 0 || store.bans != 1 {
		t.Fatal("min 0 = off (ban audit still runs)")
	}
	r.reserveCfgOverride = &config.Config{ReserveMinActive: 1, ReserveActivationDelay: "0s"}
	r.reserveControl(context.Background(), time.Now())
	if len(store.promotes) != 0 {
		t.Fatal("no shortfall, no promotion")
	}
}

func TestReserveControlEmptyReserveQuietAndWarns(t *testing.T) {
	store := &fakeReserveStore{reserved: map[string]time.Time{}, active: 3}
	r := reserveTestRuntime(store, &config.Config{ReserveMinActive: 10, ReserveActivationDelay: "0s"})
	defer coreauth.SetReservedCredentials(nil)
	var buf bytes.Buffer
	prev := log.StandardLogger().Out
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	now := time.Now()
	for i := 0; i < 20; i++ { // 10 minutes of 30s ticks
		r.reserveControl(context.Background(), now.Add(time.Duration(i)*30*time.Second))
	}
	if len(store.promotes) != 0 {
		t.Fatal("nothing to promote")
	}
	if c := strings.Count(buf.String(), "储备不足"); c != 1 {
		t.Fatalf("empty-reserve warning logged %d times, want once per state change", c)
	}
	var st ReserveStats
	r.ReserveStatus(context.Background(), &st)
	if !st.Short || st.Active != 3 || st.Eligible != 0 || st.MinActive != 10 {
		t.Fatalf("status = %+v, want short", st)
	}
	// Reserves exist but none match the tier filter: same quiet behaviour.
	store.reserved["r1"] = now
	store.cands = []ReserveCandidate{{ID: "r1", Tier: "free-tier", Healthy: true, Health: "pass"}}
	r.reserveCfgOverride = &config.Config{ReserveMinActive: 10, ReserveActivationDelay: "0s", ReserveTierFilter: "ultra"}
	for i := 0; i < 5; i++ {
		r.reserveControl(context.Background(), now.Add(time.Hour+time.Duration(i)*30*time.Second))
	}
	if len(store.promotes) != 0 || strings.Count(buf.String(), "储备不足") != 1 {
		t.Fatal("tier-filtered reserve must not promote or re-log")
	}
	// Recovery then a new shortfall logs again (state change).
	store.active = 10
	r.reserveControl(context.Background(), now.Add(2*time.Hour))
	store.active = 3
	r.reserveControl(context.Background(), now.Add(2*time.Hour+30*time.Second))
	if c := strings.Count(buf.String(), "储备不足"); c != 2 {
		t.Fatalf("new shortfall should log again, got %d", c)
	}
}

func TestReservePromotionHealthPolicy(t *testing.T) {
	in := []ReserveCandidate{{ID: "p", Seq: 3, Health: "pass"}, {ID: "f", Seq: 1, Health: "fail"}, {ID: "u", Seq: 2}}
	ids := func(c []ReserveCandidate) string {
		s := ""
		for _, x := range c {
			s += x.ID
		}
		return s
	}
	if got := ids(orderReserveCandidates(in, nil, "pass")); got != "p" {
		t.Fatalf("pass policy = %q", got)
	}
	if got := ids(orderReserveCandidates(in, nil, "not-failed")); got != "up" {
		t.Fatalf("not-failed policy = %q", got)
	}
	if got := ids(orderReserveCandidates(in, nil, "any")); got != "fup" {
		t.Fatalf("any policy = %q", got)
	}
}
