package cluster

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
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
	if active, _ := repo.CountActiveCredentials(ctx, true); active != 1 {
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
	if active, _ := repo.CountActiveCredentials(ctx, true); active != 2 {
		t.Fatalf("active after promote = %d", active)
	}
	// Failed health check, error status and quota-cooled (when not counted) are not available.
	_ = repo.SaveHealthCheck(ctx, CredentialHealthCheck{AuthUUID: "a1", Status: "fail", CheckedAt: time.Now()})
	if active, _ := repo.CountActiveCredentials(ctx, true); active != 1 {
		t.Fatalf("active with failed check = %d", active)
	}
	_ = db.Exec(`UPDATE auth SET unavailable = true, next_retry_after = ? WHERE uuid = 'a3'`, time.Now().Add(time.Hour)).Error
	if active, _ := repo.CountActiveCredentials(ctx, true); active != 1 {
		t.Fatalf("cooled counted = %d", active)
	}
	if active, _ := repo.CountActiveCredentials(ctx, false); active != 0 {
		t.Fatalf("cooled not counted = %d", active)
	}
	_ = db.Exec(`UPDATE auth SET unavailable = false, status = 'error' WHERE uuid = 'a3'`).Error
	if active, _ := repo.CountActiveCredentials(ctx, true); active != 0 {
		t.Fatalf("error status counted = %d", active)
	}
}

func newReserveAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{
		ID: id, Index: id, Provider: "antigravity", Status: coreauth.StatusActive,
		Metadata: map[string]any{"refresh_token": "rt"},
	}
}

func reserveFlag(t *testing.T, repo *Repository, id string) CredentialReserve {
	t.Helper()
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	var row CredentialReserve
	if err := db.Where("auth_uuid = ?", id).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// Disabling a reserved credential clears reserved and leaves other reserves,
// the auth row, and the promotable count alone.
func TestDisableClearsCredentialReserve(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	for _, id := range []string{"live", "tos", "unauth", "plain"} {
		if _, err := repo.UpsertAuth(ctx, newReserveAuth(id), "create"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.SetCredentialsReserved(ctx, []string{"live", "tos", "unauth"}, true, "reserve", "t"); err != nil || n != 3 {
		t.Fatalf("reserve n=%d err=%v", n, err)
	}
	activeBefore, err := repo.CountActiveCredentials(ctx, true)
	if err != nil || activeBefore != 1 {
		t.Fatalf("active before = %d err=%v, want 1 (plain)", activeBefore, err)
	}

	// A non-disable update must not drop the reserve mark.
	live, _, err := repo.GetAuth(ctx, "live")
	if err != nil {
		t.Fatal(err)
	}
	live.Label = "renamed"
	if _, err := repo.UpsertAuth(ctx, live, "update"); err != nil {
		t.Fatal(err)
	}
	if row := reserveFlag(t, repo, "live"); !row.Reserved || row.PromotedAt != nil {
		t.Fatalf("live reserve changed by a non-disable update: %+v", row)
	}

	tos, _, err := repo.GetAuth(ctx, "tos")
	if err != nil {
		t.Fatal(err)
	}
	since := reserveFlag(t, repo, "tos").ReservedSince
	tos.Disabled = true
	tos.Status = coreauth.StatusDisabled
	tos.StatusMessage = "账号已停用/违反条款（403） · 10-29 12:00 停用：terms of service"
	if _, err := repo.UpsertAuth(ctx, tos, "update"); err != nil {
		t.Fatal(err)
	}
	row := reserveFlag(t, repo, "tos")
	if row.Reserved || row.PromotedAt != nil || row.ReservedSince == nil || !row.ReservedSince.Equal(*since) {
		t.Fatalf("tos reserve = %+v, want reserved cleared without a promotion", row)
	}
	got, rec, err := repo.GetAuth(ctx, "tos")
	if err != nil || !got.Disabled || got.Status != coreauth.StatusDisabled || rec.DeletedAt.Valid {
		t.Fatalf("tos auth changed beyond disable: auth=%+v deleted=%v err=%v", got, rec != nil && rec.DeletedAt.Valid, err)
	}
	if n, _ := repo.CountReserveEvents(ctx, "ban", time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("ban events = %d, want 1 (was reserved)", n)
	}
	evs, err := repo.RecentReserveEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	var ban *CredentialReserveEvent
	for i := range evs {
		if evs[i].Kind == "ban" && evs[i].AuthUUID == "tos" {
			ban = &evs[i]
			break
		}
	}
	if ban == nil || !ban.WasReserved || ban.ReservedSince == nil {
		t.Fatalf("ban audit = %+v", ban)
	}

	// Verify-account and unauthorized share the same persist hook.
	_, _, changed, err := repo.MutateAuth(ctx, "unauth", "update", func(auth *coreauth.Auth) bool {
		auth.Disabled = true
		auth.Status = coreauth.StatusDisabled
		auth.StatusMessage = "unauthorized"
		return true
	})
	if err != nil || !changed {
		t.Fatalf("mutate disable changed=%v err=%v", changed, err)
	}
	if row := reserveFlag(t, repo, "unauth"); row.Reserved {
		t.Fatalf("unauthorized disable left reserved: %+v", row)
	}
	unauth, _, err := repo.GetAuth(ctx, "unauth")
	if err != nil || !unauth.Disabled || unauth.StatusMessage != "unauthorized" {
		t.Fatalf("unauth auth = %+v err=%v", unauth, err)
	}
	if n, _ := repo.CountReserveEvents(ctx, "ban", time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("unauthorized disable recorded a ban: %d", n)
	}
	if n, _ := repo.CountReserveEvents(ctx, "clear-on-disable", time.Now().Add(-time.Minute)); n != 2 {
		t.Fatalf("clear-on-disable events = %d, want 2 (tos + unauth)", n)
	}
	if n, _ := repo.CountReserveEvents(ctx, "promote", time.Now().Add(-time.Minute)); n != 0 {
		t.Fatalf("promote events = %d", n)
	}
	evs, err = repo.RecentReserveEvents(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	cleared := map[string]bool{}
	for i := range evs {
		if evs[i].Kind == "clear-on-disable" {
			if !evs[i].WasReserved || evs[i].Detail != "停用时清除储备" {
				t.Fatalf("clear-on-disable = %+v", evs[i])
			}
			cleared[evs[i].AuthUUID] = true
		}
	}
	if !cleared["tos"] || !cleared["unauth"] {
		t.Fatalf("clear-on-disable auth ids = %v", cleared)
	}
	if row := reserveFlag(t, repo, "live"); !row.Reserved {
		t.Fatal("disabling other credentials cleared the live reserve")
	}
	if _, err := repo.ListReservedCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	listed, _ := repo.ListReservedCredentials(ctx)
	if len(listed) != 1 || listed["live"].IsZero() {
		t.Fatalf("schedulable reserves = %v", listed)
	}
	if active, _ := repo.CountActiveCredentials(ctx, true); active != activeBefore {
		t.Fatalf("active after disable = %d, want %d", active, activeBefore)
	}

	// A later write of an already-disabled credential must not duplicate the ban.
	again, _, err := repo.GetAuth(ctx, "tos")
	if err != nil {
		t.Fatal(err)
	}
	again.Label = "still-disabled"
	if _, err := repo.UpsertAuth(ctx, again, "update"); err != nil {
		t.Fatal(err)
	}
	if n, _ := repo.CountReserveEvents(ctx, "ban", time.Now().Add(-time.Minute)); n != 1 {
		t.Fatalf("ban events after rewrite = %d", n)
	}
	if row := reserveFlag(t, repo, "tos"); row.Reserved {
		t.Fatal("rewrite re-reserved a disabled credential")
	}
}

// The sweeper clears disabled+reserved zombies and leaves live reserves reserved.
func TestUnreserveDisabledCredentialsLeavesLiveReserves(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"zombie", "live", "plain", "already"} {
		if _, err := repo.UpsertAuth(ctx, newReserveAuth(id), "create"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.SetCredentialsReserved(ctx, []string{"zombie", "live"}, true, "reserve", "t"); err != nil || n != 2 {
		t.Fatalf("reserve n=%d err=%v", n, err)
	}
	// Bypass the disable hook so the row stays a zombie, as historical data does.
	if err := db.Exec(`UPDATE auth SET disabled = true, status = ? WHERE uuid = ?`, coreauth.StatusDisabled, "zombie").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE auth SET disabled = true, status = ? WHERE uuid = ?`, coreauth.StatusDisabled, "already").Error; err != nil {
		t.Fatal(err)
	}
	activeBefore, err := repo.CountActiveCredentials(ctx, true)
	if err != nil || activeBefore != 1 {
		t.Fatalf("active before = %d err=%v, want 1 (plain; live is reserved, zombie and already are disabled)", activeBefore, err)
	}
	n, err := repo.UnreserveDisabledCredentials(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v, want 1", n, err)
	}
	if c, _ := repo.CountReserveEvents(ctx, "clear-on-disable", time.Now().Add(-time.Minute)); c != 1 {
		t.Fatalf("sweep clear-on-disable events = %d, want 1", c)
	}
	evs, err := repo.RecentReserveEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range evs {
		if evs[i].Kind == "clear-on-disable" && evs[i].AuthUUID == "zombie" {
			found = true
			if !evs[i].WasReserved || evs[i].Detail != "停用时清除储备" {
				t.Fatalf("sweep event = %+v", evs[i])
			}
		}
	}
	if !found {
		t.Fatalf("missing clear-on-disable for zombie: %+v", evs)
	}
	if row := reserveFlag(t, repo, "zombie"); row.Reserved {
		t.Fatalf("zombie still reserved: %+v", row)
	}
	if row := reserveFlag(t, repo, "live"); !row.Reserved || row.PromotedAt != nil {
		t.Fatalf("live reserve = %+v", row)
	}
	_, rec, err := repo.GetAuth(ctx, "zombie")
	if err != nil || rec == nil || !rec.Disabled || rec.DeletedAt.Valid {
		t.Fatalf("sweep changed the credential: disabled=%v deleted=%v err=%v", rec != nil && rec.Disabled, rec != nil && rec.DeletedAt.Valid, err)
	}
	var reserveRows int64
	if err := db.Model(&CredentialReserve{}).Count(&reserveRows).Error; err != nil || reserveRows != 2 {
		t.Fatalf("reserve rows = %d err=%v, want the marks updated in place", reserveRows, err)
	}
	if active, _ := repo.CountActiveCredentials(ctx, true); active != activeBefore {
		t.Fatalf("active after sweep = %d, want %d", active, activeBefore)
	}
	listed, err := repo.ListReservedCredentials(ctx)
	if err != nil || len(listed) != 1 || listed["live"].IsZero() {
		t.Fatalf("listed = %v err=%v", listed, err)
	}
	n, err = repo.UnreserveDisabledCredentials(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second sweep n=%d err=%v, want 0", n, err)
	}
	if c, _ := repo.CountReserveEvents(ctx, "clear-on-disable", time.Now().Add(-time.Minute)); c != 1 {
		t.Fatalf("second sweep duplicated clear-on-disable: %d", c)
	}
	if row := reserveFlag(t, repo, "live"); !row.Reserved {
		t.Fatal("second sweep cleared a live reserve")
	}
}


// Egress pauses set Disabled=true but must keep reserve intact (probe flap).
func TestClearReserveSkipsEgressPause(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	auth := newReserveAuth("paused")
	auth.Metadata["auto_paused_reason"] = "egress_down"
	if _, err := repo.UpsertAuth(ctx, auth, "create"); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.SetCredentialsReserved(ctx, []string{"paused"}, true, "reserve", "t"); err != nil || n != 1 {
		t.Fatalf("reserve n=%d err=%v", n, err)
	}
	auth.Disabled = true
	auth.Status = coreauth.StatusDisabled
	auth.StatusMessage = "已暂停 · 出口离线 1.2.3.4"
	if _, err := repo.UpsertAuth(ctx, auth, "update"); err != nil {
		t.Fatal(err)
	}
	if row := reserveFlag(t, repo, "paused"); !row.Reserved {
		t.Fatalf("egress pause cleared reserve: %+v", row)
	}
	if c, _ := repo.CountReserveEvents(ctx, "clear-on-disable", time.Now().Add(-time.Minute)); c != 0 {
		t.Fatalf("clear-on-disable events = %d, want 0", c)
	}
}

func TestUnreserveDisabledSkipsEgressPause(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"eg-pause", "real-ban"} {
		if _, err := repo.UpsertAuth(ctx, newReserveAuth(id), "create"); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := repo.SetCredentialsReserved(ctx, []string{"eg-pause", "real-ban"}, true, "reserve", "t"); err != nil || n != 2 {
		t.Fatalf("reserve n=%d err=%v", n, err)
	}
	// Persist egress-pause metadata + disabled on eg-pause; plain disable on real-ban.
	paused := newReserveAuth("eg-pause")
	paused.Disabled = true
	paused.Status = coreauth.StatusDisabled
	paused.Metadata["auto_paused_reason"] = "egress_down"
	if _, err := repo.UpsertAuth(ctx, paused, "update"); err != nil {
		t.Fatal(err)
	}
	// UpsertAuth for egress pause must not have cleared reserve already.
	if row := reserveFlag(t, repo, "eg-pause"); !row.Reserved {
		t.Fatal("upsert of egress pause cleared reserve before sweeper")
	}
	if err := db.Exec(`UPDATE auth SET disabled = true, status = ? WHERE uuid = ?`, coreauth.StatusDisabled, "real-ban").Error; err != nil {
		t.Fatal(err)
	}
	n, err := repo.UnreserveDisabledCredentials(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v, want 1 (only real-ban)", n, err)
	}
	if row := reserveFlag(t, repo, "eg-pause"); !row.Reserved {
		t.Fatal("sweeper cleared egress-paused reserve")
	}
	if row := reserveFlag(t, repo, "real-ban"); row.Reserved {
		t.Fatal("sweeper left real disable reserved")
	}
}

func TestClearReserveSkipsEgressRemoved(t *testing.T) {
	repo, ctx := newTestKVRepository(t)
	auth := newReserveAuth("removed-ip")
	auth.Metadata["auto_paused_reason"] = "egress_removed"
	auth.Disabled = true
	auth.Status = coreauth.StatusDisabled
	if _, err := repo.UpsertAuth(ctx, auth, "create"); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.SetCredentialsReserved(ctx, []string{"removed-ip"}, true, "reserve", "t"); err != nil || n != 1 {
		t.Fatalf("reserve n=%d err=%v", n, err)
	}
	// Re-upsert as newly disabled transition (existing already disabled — sweeper path).
	n, err := repo.UnreserveDisabledCredentials(ctx)
	if err != nil || n != 0 {
		t.Fatalf("sweep n=%d err=%v, want 0", n, err)
	}
	if row := reserveFlag(t, repo, "removed-ip"); !row.Reserved {
		t.Fatal("egress_removed pause cleared reserve")
	}
}
