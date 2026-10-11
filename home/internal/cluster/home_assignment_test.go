package cluster

import (
	"fmt"
	"testing"
	"time"
)

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return out
}

func spread(a map[string]string, homes []string) (min, max int) {
	counts := map[string]int{}
	for _, h := range a {
		counts[h]++
	}
	min, max = 1<<30, 0
	for _, h := range homes {
		if counts[h] < min {
			min = counts[h]
		}
		if counts[h] > max {
			max = counts[h]
		}
	}
	return
}

func moved(prev, next map[string]string) int {
	n := 0
	for p, h := range next {
		if old, ok := prev[p]; ok && old != h {
			n++
		}
	}
	return n
}

func TestBalanceAssignmentsEvenAndStable(t *testing.T) {
	for _, tc := range []struct{ proxies, homes int }{{3, 2}, {3, 3}, {4, 3}, {10, 4}, {1, 3}, {7, 1}} {
		proxies, homes := names("p", tc.proxies), names("h", tc.homes)
		a := balanceAssignments(nil, proxies, homes)
		if len(a) != tc.proxies {
			t.Fatalf("%v: %d assigned", tc, len(a))
		}
		if lo, hi := spread(a, homes); hi-lo > 1 {
			t.Fatalf("%v: uneven %d..%d", tc, lo, hi)
		}
		if again := balanceAssignments(a, proxies, homes); moved(a, again) != 0 {
			t.Fatalf("%v: rebalance of a balanced table moved proxies", tc)
		}
	}
}

func TestBalanceAssignmentsMinimalMovement(t *testing.T) {
	proxies := names("p", 6)
	two := names("h", 2)
	a := balanceAssignments(nil, proxies, two)
	// Add a Home: exactly 2 proxies (6/3) must move to it, nobody else.
	three := names("h", 3)
	b := balanceAssignments(a, proxies, three)
	if lo, hi := spread(b, three); hi-lo > 1 {
		t.Fatalf("uneven after add: %d..%d", lo, hi)
	}
	if m := moved(a, b); m != 2 {
		t.Fatalf("adding a home moved %d proxies, want 2", m)
	}
	// Remove a Home: only its proxies move.
	gone := "h1"
	var onGone int
	for _, h := range b {
		if h == gone {
			onGone++
		}
	}
	left := []string{"h0", "h2"}
	c := balanceAssignments(b, proxies, left)
	if lo, hi := spread(c, left); hi-lo > 1 {
		t.Fatalf("uneven after remove: %d..%d", lo, hi)
	}
	if m := moved(b, c); m != onGone {
		t.Fatalf("removing a home moved %d proxies, want %d (only its own)", m, onGone)
	}
	// A proxy leaves and a new one joins: the newcomer fills the gap, no moves.
	proxies2 := append(append([]string(nil), proxies[1:]...), "pnew")
	d := balanceAssignments(c, proxies2, left)
	if m := moved(c, d); m != 0 {
		t.Fatalf("proxy churn moved %d existing proxies", m)
	}
	if _, ok := d[proxies[0]]; ok {
		t.Fatal("departed proxy kept an assignment")
	}
}

func TestRebalanceHomeAssignmentsRepository(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	if err := ensureHomeAssignmentTable(ctx, repo.db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		if err := repo.db.Create(&ClusterNodeRecord{IP: ip, Port: 8327, StartedAt: now, LastSeenAt: now, IsMaster: i == 0}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		rec := CPANodeMembershipRecord{CertificateFingerprint: fmt.Sprintf("fp%d", i), NodeID: fmt.Sprintf("node%d", i),
			HomeIP: "10.0.0.1", HomePort: 8327, HomeStartedAt: now, State: MembershipStateActive, ConnectedAt: now, LastSeenAt: now, UpdatedAt: now}
		if err := repo.db.Create(&rec).Error; err != nil {
			t.Fatal(err)
		}
	}
	n, err := repo.RebalanceHomeAssignments(ctx, time.Minute)
	if err != nil || n != 3 {
		t.Fatalf("first rebalance: n=%d err=%v", n, err)
	}
	rows, _ := repo.ListHomeAssignments(ctx)
	per := map[string]int{}
	for _, r := range rows {
		per[r.HomeIP]++
	}
	if len(rows) != 3 || per["10.0.0.1"] < 1 || per["10.0.0.2"] < 1 {
		t.Fatalf("not spread: %+v", per)
	}
	if n, _ := repo.RebalanceHomeAssignments(ctx, time.Minute); n != 0 {
		t.Fatalf("second rebalance changed %d rows", n)
	}
	ip, port, ok, err := repo.HomeAssignmentFor(ctx, "node0")
	if err != nil || !ok || ip == "" || port != 8327 {
		t.Fatalf("lookup: %s %d %v %v", ip, port, ok, err)
	}
}

func TestAnnotateAssignmentAddsOnlyForAssignedProxy(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	if err := ensureHomeAssignmentTable(ctx, repo.db); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Create(&HomeAssignmentRecord{NodeID: "node-a", HomeIP: "10.0.0.2", HomePort: 8327, UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	h := &RESPHandler{repo: repo}
	payload := []byte(`{"ok":true,"nodes":[{"ip":"10.0.0.1","port":8327}]}`)
	got := string(h.AnnotateAssignment(ctx, payload, "node-a"))
	if want := `"assigned":{"ip":"10.0.0.2","port":8327}`; !contains(got, want) {
		t.Fatalf("missing assignment: %s", got)
	}
	if out := h.AnnotateAssignment(ctx, payload, "node-unknown"); string(out) != string(payload) {
		t.Fatalf("unassigned proxy payload changed: %s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRebalanceKeepsBrieflyAbsentProxy(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	if err := ensureHomeAssignmentTable(ctx, repo.db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		repo.db.Create(&ClusterNodeRecord{IP: ip, Port: 8327, StartedAt: now, LastSeenAt: now})
	}
	for i := 0; i < 2; i++ {
		repo.db.Create(&CPANodeMembershipRecord{CertificateFingerprint: fmt.Sprintf("fp%d", i), NodeID: fmt.Sprintf("node%d", i),
			HomeIP: "10.0.0.1", HomePort: 8327, HomeStartedAt: now, State: MembershipStateActive, ConnectedAt: now, LastSeenAt: now, UpdatedAt: now})
	}
	if _, err := repo.RebalanceHomeAssignments(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	before, _ := repo.ListHomeAssignments(ctx)
	// node0 is reconnecting (closed for a moment).
	repo.db.Model(&CPANodeMembershipRecord{}).Where("node_id = ?", "node0").Update("state", "closed")
	if _, err := repo.RebalanceHomeAssignments(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	repo.db.Model(&CPANodeMembershipRecord{}).Where("node_id = ?", "node0").Update("state", MembershipStateActive)
	if n, _ := repo.RebalanceHomeAssignments(ctx, time.Minute); n != 0 {
		t.Fatalf("returning proxy caused %d changes", n)
	}
	after, _ := repo.ListHomeAssignments(ctx)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("assignment changed across a brief absence:\n%v\n%v", before, after)
	}
}
