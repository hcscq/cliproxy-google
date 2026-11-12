package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
	log "github.com/sirupsen/logrus"
)

const bdTestSecret = "unit-test-bd-zone-password-MUST-NOT-LEAK"

type memBDRepo struct {
	mu   sync.Mutex
	rows map[string]BDEgressRow
}

func (m *memBDRepo) ListBDEgress(context.Context) ([]BDEgressRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]BDEgressRow, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}
func (m *memBDRepo) UpsertBDEgress(_ context.Context, row BDEgressRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]BDEgressRow{}
	}
	m.rows[row.ID] = row
	return nil
}
func (m *memBDRepo) DeleteBDEgress(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

func bdTestRuntime(t *testing.T, enabled bool, rows ...BDEgressRow) *Runtime {
	t.Helper()
	repo := &memBDRepo{rows: map[string]BDEgressRow{}}
	for _, r := range rows {
		if r.ID == "" {
			r.ID = proxyutil.BDEgressID(r.ExpectedIP)
		}
		if r.MaxCredentials == 0 {
			r.MaxCredentials = 1
		}
		repo.rows[r.ID] = r
	}
	rt := &Runtime{cfg: &config.Config{}}
	rt.cfg.Routing.Egress.BD.Enabled = enabled
	rt.cfg.Routing.Egress.BD.BaseUsername = "brd-customer-test-zone-isp"
	rt.SetBDEgressRepo(repo)
	t.Setenv("HOME_BD_RELAYS", "10.42.0.10:3142")
	secret := bdTestSecret
	proxyutil.SetBDPasswordForTest(&secret)
	t.Cleanup(func() { proxyutil.SetBDPasswordForTest(nil) })
	return rt
}

func assertNoSecret(t *testing.T, raw any, where string) {
	t.Helper()
	s := fmt.Sprintf("%v", raw)
	if b, err := json.Marshal(raw); err == nil {
		s = string(b) + s
	}
	if strings.Contains(s, bdTestSecret) {
		t.Fatalf("%s leaked BD password: %s", where, proxyutil.RedactProxyURL(s))
	}
}

func TestBDEnabledDefaultsFalse(t *testing.T) {
	if (&Runtime{}).BDEnabled() {
		t.Fatal("nil cfg must be disabled")
	}
	if (&Runtime{cfg: &config.Config{}}).BDEnabled() {
		t.Fatal("zero config must be disabled")
	}
	rt := &Runtime{cfg: &config.Config{}}
	rt.cfg.Routing.Egress.BD.Enabled = true
	if !rt.BDEnabled() {
		t.Fatal("explicit true")
	}
}

func TestBDSpecsOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	row := BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true}
	off := bdTestRuntime(t, false, row)
	if specs := off.bdEgressSpecs(ctx); len(specs) != 0 {
		t.Fatalf("flag off must not register: %+v", specs)
	}
	on := bdTestRuntime(t, true, row)
	specs := on.bdEgressSpecs(ctx)
	if len(specs) != 1 || specs[0].Name != "bd/198.51.100.10" || specs[0].Kind != EgressKindBD {
		t.Fatalf("specs = %+v", specs)
	}
	if strings.Contains(specs[0].ProxyURL, bdTestSecret) {
		t.Fatal("registered proxy_url has password")
	}
	if !strings.Contains(specs[0].ProxyURL, "198.51.100.10") || !strings.Contains(specs[0].ProxyURL, "10.42.0.10:3142") {
		t.Fatalf("proxy_url = %s", specs[0].ProxyURL)
	}
}

func TestPickEgressNeverSelectsBD(t *testing.T) {
	bd := EgressSpec{Name: "bd/198.51.100.10", Kind: EgressKindBD, ProxyURL: "http://u@r:3142", IP: "198.51.100.10"}
	self := EgressSpec{Name: "7fm6r3az/1.1.1.1", ProxyURL: "http://h:1", IP: "1.1.1.1"}
	status := map[string]*EgressStatus{
		bd.ProxyURL:   {Healthy: true},
		self.ProxyURL: {Healthy: true},
	}
	// Least loaded is BD (0 vs 5) — still must pick self.
	got, ok := pickEgress([]EgressSpec{bd, self}, status, map[string]int{self.ProxyURL: 5}, true)
	if !ok || got.Name != self.Name || isBDEgress(got) {
		t.Fatalf("picked %+v %v", got, ok)
	}
	// Only BD is healthy: pickEgress must return nothing (never BD).
	status[self.ProxyURL].Healthy = false
	if _, ok := pickEgress([]EgressSpec{bd, self}, status, nil, true); ok {
		t.Fatal("picked BD when it was the only healthy egress")
	}
	// assignableEgressSpecs drops BD even when nothing is disabled.
	gotSpecs := assignableEgressSpecs([]EgressSpec{bd, self}, EgressSettingsValue{})
	if len(gotSpecs) != 1 || isBDEgress(gotSpecs[0]) {
		t.Fatalf("assignable = %+v", gotSpecs)
	}
}

func TestResolveEgressBDRejectedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	rt := bdTestRuntime(t, false, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true})
	_, ok, err := rt.ResolveEgress(ctx, "bd/198.51.100.10", nil)
	if ok || err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("want disabled error, got ok=%v err=%v", ok, err)
	}
	assertNoSecret(t, err, "ResolveEgress error")
}

func TestResolveEgressBDExplicitWhenEnabled(t *testing.T) {
	ctx := context.Background()
	rt := bdTestRuntime(t, true, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true, MaxCredentials: 1})
	spec, ok, err := rt.ResolveEgress(ctx, "bd/198.51.100.10", nil)
	if err != nil || !ok || spec.Name != "bd/198.51.100.10" || spec.Kind != EgressKindBD {
		t.Fatalf("got %+v %v %v", spec, ok, err)
	}
	assertNoSecret(t, spec, "ResolveEgress spec")
	// Draining rejects new assignment.
	rt = bdTestRuntime(t, true, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true, Draining: true})
	_, ok, err = rt.ResolveEgress(ctx, "bd/198.51.100.10", nil)
	if ok || err == nil || !strings.Contains(strings.ToLower(err.Error()), "drain") {
		t.Fatalf("want draining error, got ok=%v err=%v", ok, err)
	}
}

func TestPickEgressForImportSkipsBD(t *testing.T) {
	ctx := context.Background()
	rt := bdTestRuntime(t, true, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true})
	// No self-built healthy egress in KV / env: auto must not fall back to BD.
	spec, ok := rt.PickEgressForImport(ctx, nil)
	if ok || isBDEgress(spec) {
		t.Fatalf("auto import picked BD: %+v %v", spec, ok)
	}
}

func TestApplyEgressMetadataBDHasNoPasswordAndSetsKind(t *testing.T) {
	url, err := proxyutil.BuildBDRelayURL("user-ip-198.51.100.10", "10.42.0.10:3142")
	if err != nil {
		t.Fatal(err)
	}
	// Even if a caller accidentally passes a password-bearing URL, we strip it.
	leaked, _ := proxyutil.InjectBDPassword(url, bdTestSecret)
	a := &coreauth.Auth{Metadata: map[string]any{}}
	ApplyEgressMetadata(a, EgressSpec{Name: "bd/198.51.100.10", Kind: EgressKindBD, ProxyURL: leaked, IP: "198.51.100.10"})
	if a.Metadata[EgressKindKey] != EgressKindBD || a.Metadata[EgressIDKey] != "bd/198.51.100.10" {
		t.Fatalf("kind/id: %+v", a.Metadata)
	}
	assertNoSecret(t, a, "auth after ApplyEgressMetadata")
	assertNoSecret(t, a.ProxyURL, "ProxyURL")
	assertNoSecret(t, a.Metadata["proxy_url"], "metadata proxy_url")
	raw, _ := json.Marshal(a)
	assertNoSecret(t, string(raw), "auth_json")

	ApplyEgressMetadata(a, EgressSpec{})
	if a.Metadata[EgressKindKey] != nil || a.ProxyURL != "" {
		t.Fatalf("unpin left kind: %+v", a)
	}
}

func TestBDEgressDownPausesInPlaceNeverRepins(t *testing.T) {
	ctx := context.Background()
	bdURL, _ := proxyutil.BuildBDRelayURL("user-ip-198.51.100.10", "10.42.0.10:3142")
	bd := EgressSpec{Name: "bd/198.51.100.10", Kind: EgressKindBD, ProxyURL: bdURL, IP: "198.51.100.10", ExpectedPublicIP: "198.51.100.10"}
	self := EgressSpec{Name: "n/1.1.1.1", ProxyURL: "http://h:1", IP: "1.1.1.1"}
	manager := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: "c-bd", Provider: "antigravity", Status: coreauth.StatusActive, Metadata: map[string]any{}}
	ApplyEgressMetadata(a, bd)
	if _, err := manager.Register(coreauth.WithSkipPersist(ctx), a); err != nil {
		t.Fatal(err)
	}
	rt := bdTestRuntime(t, true, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true})
	rt.coreManager = manager

	oldObs := observeEgressIP
	observeEgressIP = func(context.Context, string, string, time.Duration) (string, error) {
		return "198.51.100.10", nil
	}
	t.Cleanup(func() { observeEgressIP = oldObs })

	down := false
	old := egressChecker
	egressChecker = func(_ context.Context, u string, _ time.Duration) error {
		// The checker is given an injected URL at probe time (in-memory only).
		// The credential itself is asserted secret-free after each round.
		_ = u
		if down {
			return errors.New("down")
		}
		return nil
	}
	t.Cleanup(func() { egressChecker = old })

	rt.egressRound(ctx, []EgressSpec{bd, self})
	got, _ := manager.GetByID("c-bd")
	if got.Disabled || got.ProxyURL != bdURL {
		t.Fatalf("healthy: %+v", got)
	}
	assertNoSecret(t, got, "auth after healthy round")

	down = true
	for i := 0; i < egressFailThreshold; i++ {
		rt.egressRound(ctx, []EgressSpec{bd, self})
	}
	got, _ = manager.GetByID("c-bd")
	if !got.Disabled || got.Metadata[EgressPausedReasonKey] != egressPausedDown {
		t.Fatalf("not paused: %+v", got)
	}
	if got.ProxyURL != bdURL || got.Metadata[EgressIDKey] != "bd/198.51.100.10" {
		t.Fatalf("repinned or unpinned on down: %+v", got)
	}
	if got.ProxyURL == self.ProxyURL || got.ProxyURL == "" {
		t.Fatal("went to another egress or direct")
	}
	assertNoSecret(t, got, "auth after pause")

	// Recovery stays on the same BD egress.
	down = false
	rt.egressRound(ctx, []EgressSpec{bd, self})
	got, _ = manager.GetByID("c-bd")
	if got.Disabled || got.ProxyURL != bdURL {
		t.Fatalf("not resumed on same BD egress: %+v", got)
	}
}

func TestBDSettingsAndPanelPayloadHaveNoPassword(t *testing.T) {
	ctx := context.Background()
	rt := bdTestRuntime(t, false, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true})
	view, rows, err := rt.BDSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled {
		t.Fatal("enabled must reflect config (false)")
	}
	if !view.HasPassword {
		t.Fatal("has_password should be true when Secret is set")
	}
	assertNoSecret(t, view, "BDConfigView")
	assertNoSecret(t, rows, "inventory")
	raw, _ := json.Marshal(ginLike(view, rows))
	assertNoSecret(t, string(raw), "panel JSON")
	if strings.Contains(string(raw), `"password"`) && strings.Contains(strings.ToLower(string(raw)), bdTestSecret) {
		t.Fatal("password field")
	}
}

func ginLike(view BDConfigView, rows []BDEgressRow) map[string]any {
	return map[string]any{
		"enabled": view.Enabled, "endpoint": view.Endpoint, "base_username": view.BaseUsername,
		"username_template": view.UsernameTemplate, "has_password": view.HasPassword, "items": rows,
	}
}

func TestSanitizeEgressStatusStripsPassword(t *testing.T) {
	leaked, _ := proxyutil.InjectBDPassword("http://user@10.0.0.1:3142", bdTestSecret)
	st := SanitizeEgressStatus(EgressStatus{EgressSpec: EgressSpec{ProxyURL: leaked}, LastError: "Get http://user:" + bdTestSecret + "@x: EOF"})
	assertNoSecret(t, st, "status")
	if strings.Contains(st.LastError, bdTestSecret) {
		t.Fatalf("LastError: %s", st.LastError)
	}
}

func TestBDLogsDoNotContainPassword(t *testing.T) {
	var buf bytes.Buffer
	prev := log.StandardLogger().Out
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	ctx := context.Background()
	rt := bdTestRuntime(t, true, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true})
	_ = rt.bdEgressSpecs(ctx)
	_, _, _ = rt.ResolveEgress(ctx, "bd/198.51.100.10", nil)
	assertNoSecret(t, buf.String(), "logrus output")
}

func TestMaxCredentialsDefaultOne(t *testing.T) {
	ctx := context.Background()
	rt := bdTestRuntime(t, true, BDEgressRow{ExpectedIP: "198.51.100.10", Enabled: true})
	manager := coreauth.NewManager(nil, nil, nil)
	rt.coreManager = manager
	spec, err := rt.resolveBDEgress(ctx, "bd/198.51.100.10")
	if err != nil {
		t.Fatal(err)
	}
	a := &coreauth.Auth{ID: "taken", Provider: "antigravity", Status: coreauth.StatusActive, Metadata: map[string]any{}}
	ApplyEgressMetadata(a, spec)
	_, _ = manager.Register(coreauth.WithSkipPersist(ctx), a)
	_, err = rt.resolveBDEgress(ctx, "bd/198.51.100.10")
	if err == nil || !strings.Contains(err.Error(), "上限") && !strings.Contains(strings.ToLower(err.Error()), "full") {
		t.Fatalf("want full error, got %v", err)
	}
}

func TestMergeBDSpecs(t *testing.T) {
	self := EgressSpec{Name: "n/1", ProxyURL: "http://h"}
	bd := EgressSpec{Name: "bd/1.2.3.4", Kind: EgressKindBD, ProxyURL: "http://u@r"}
	got := mergeBDSpecs([]EgressSpec{self}, []EgressSpec{bd})
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	got = mergeBDSpecs([]EgressSpec{self, {Name: "bd/1.2.3.4", ProxyURL: "old"}}, []EgressSpec{bd})
	if len(got) != 2 || got[1].ProxyURL != "http://u@r" {
		t.Fatalf("replace: %+v", got)
	}
}
