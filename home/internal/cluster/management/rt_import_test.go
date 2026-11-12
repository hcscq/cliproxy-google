package management

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

type fakeEnricher struct{ calls atomic.Int32 }

func (f *fakeEnricher) Enrich(_ context.Context, token, knownEmail, knownProject string) (*antigravityEnrichment, error) {
	f.calls.Add(1)
	if strings.HasPrefix(token, "dead") {
		return nil, &rtImportError{Code: "token_dead", Reason: "refresh token is invalid, expired or revoked (invalid_grant)"}
	}
	email := knownEmail
	if email == "" {
		email = strings.TrimPrefix(strings.SplitN(token, "-", 2)[0], "rt") + "@example.com"
	}
	project := knownProject
	if project == "" {
		project = "proj-" + email
	}
	return &antigravityEnrichment{AccessToken: "at-" + token, ExpiresIn: 3600, Email: email, ProjectID: project}, nil
}

func newRTImportHarness(t *testing.T) (*Handler, *cluster.Repository, *gin.Engine, func()) {
	t.Helper()
	db, cleanup := openManagementLogTestDB(t)
	repo := cluster.NewRepository(db)
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	h.SetAntigravityEnricher(&fakeEnricher{})
	engine := gin.New()
	engine.POST("/credentials/import/refresh-tokens", h.ImportRefreshTokens)
	engine.POST("/credentials", h.UploadAuthFile)
	return h, repo, engine, cleanup
}

func postJSON(t *testing.T, engine *gin.Engine, path string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return rec.Code, got
}

func agCreds(t *testing.T, repo *cluster.Repository) []*coreauth.Auth {
	t.Helper()
	auths, err := repo.ListAuths(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []*coreauth.Auth
	for _, a := range auths {
		if a.Provider == "antigravity" {
			out = append(out, a)
		}
	}
	return out
}

func TestParseRefreshTokenInputFormats(t *testing.T) {
	cases := map[string]int{
		"1//aaa\n1//bbb\n\n# comment\n":                              2,
		`["1//aaa","1//bbb",{"refresh_token":"1//ccc"}]`:             3,
		`{"type":"antigravity","refresh_token":"1//x"}`:              1,
		"{\"refresh_token\":\"1//a\"}\n{\"refresh_token\":\"1//b\"}": 2,
		"a@gmail.com----1//aaa\nb@gmail.com----1//bbb----extra\n":    2,
	}
	for input, want := range cases {
		got := parseRefreshTokenInput(input)
		if len(got) != want {
			t.Fatalf("%q -> %d entries, want %d", input, len(got), want)
		}
		for _, e := range got {
			if e.ParseError != "" || e.RefreshToken == "" {
				t.Fatalf("%q -> bad entry %+v", input, e)
			}
		}
	}
	if got := parseRefreshTokenInput(`[{"type":"codex","refresh_token":"x"}]`); got[0].ParseError == "" {
		t.Fatal("non-antigravity object should fail")
	}
}

func TestRefreshTokenImportDryRunDoesNotWrite(t *testing.T) {
	_, repo, engine, cleanup := newRTImportHarness(t)
	defer cleanup()
	code, got := postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"text": "rtalice-1\nrtbob-1\ndeadtoken-1\nrtalice-1", "dry_run": true})
	if code != 200 {
		t.Fatalf("code %d %v", code, got)
	}
	sum := got["summary"].(map[string]any)
	if sum["created"].(float64) != 2 || sum["failed"].(float64) != 1 || sum["skipped"].(float64) != 1 {
		t.Fatalf("summary %v", sum)
	}
	if n := len(agCreds(t, repo)); n != 0 {
		t.Fatalf("dry run wrote %d credentials", n)
	}
	if strings.Contains(string(mustJSON(got)), "rtalice-1") {
		t.Fatal("response leaked a token")
	}
}

func TestRefreshTokenImportCreatesThenDedupesByEmailAndKeepsFields(t *testing.T) {
	_, repo, engine, cleanup := newRTImportHarness(t)
	defer cleanup()
	code, got := postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"text": "rtalice-1\nrtbob-1"})
	if code != 200 || got["summary"].(map[string]any)["created"].(float64) != 2 {
		t.Fatalf("first import %d %v", code, got)
	}
	creds := agCreds(t, repo)
	if len(creds) != 2 {
		t.Fatalf("want 2 creds, got %d", len(creds))
	}
	var alice *coreauth.Auth
	for _, c := range creds {
		if c.Metadata["email"] == "alice@example.com" {
			alice = c
		}
	}
	if alice == nil || alice.Metadata["project_id"] != "proj-alice@example.com" || alice.Metadata["access_token"] != "at-rtalice-1" {
		t.Fatalf("alice not enriched: %+v", alice)
	}
	alice.Metadata["note"] = "keep me"
	if _, err := repo.UpsertAuth(context.Background(), alice, "upsert"); err != nil {
		t.Fatal(err)
	}
	// new token, same account email (upper-case) -> update, not duplicate
	code, got = postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"items": []any{map[string]any{"refresh_token": "rtnew-2", "email": "ALICE@example.com"}}})
	res := got["results"].([]any)[0].(map[string]any)
	if code != 200 || res["status"] != "updated" || res["matched_by"] != "email" || res["credential_id"] != alice.ID {
		t.Fatalf("second import %d %v", code, got)
	}
	creds = agCreds(t, repo)
	if len(creds) != 2 {
		t.Fatalf("duplicate created: %d", len(creds))
	}
	updated, _, _ := repo.GetAuth(context.Background(), alice.ID)
	if updated.Metadata["refresh_token"] != "rtnew-2" || updated.Metadata["note"] != "keep me" {
		t.Fatalf("update lost data: %v", updated.Metadata)
	}
	// same refresh token again -> matched by refresh_token
	_, got = postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"text": "rtbob-1"})
	if r := got["results"].([]any)[0].(map[string]any); r["status"] != "updated" || r["matched_by"] != "refresh_token" {
		t.Fatalf("token dedupe %v", r)
	}
}

func TestMultiFileUploadContinuesPastBadFiles(t *testing.T) {
	_, repo, engine, cleanup := newRTImportHarness(t)
	defer cleanup()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	add := func(name, body string) {
		w, _ := mw.CreateFormFile("file", name)
		_, _ = w.Write([]byte(body))
	}
	add("a.json", `{"type":"antigravity","refresh_token":"rtcarol-1"}`)
	add("bad.json", `{not json`)
	add("dead.json", `{"type":"antigravity","refresh_token":"deadx-1"}`)
	add("b.json", `{"type":"antigravity","refresh_token":"rtdave-1","email":"dave@corp.io","project_id":"p-d"}`)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/credentials", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["status"] != "partial" {
		t.Fatalf("upload %d %v", rec.Code, got)
	}
	results := got["results"].([]any)
	want := []string{"created", "failed", "failed", "created"}
	for i, w := range want {
		if s := results[i].(map[string]any)["status"]; s != w {
			t.Fatalf("file %d status %v want %s (%v)", i, s, w, results)
		}
	}
	if n := len(agCreds(t, repo)); n != 2 {
		t.Fatalf("want 2 creds, got %d", n)
	}
}

func TestClassifyTokenRefreshFailure(t *testing.T) {
	err := classifyTokenRefreshFailure(400, []byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	ie := err.(*rtImportError)
	if ie.Code != "token_dead" || !strings.Contains(ie.Reason, "revoked") {
		t.Fatalf("%+v", ie)
	}
	if e := classifyTokenRefreshFailure(503, nil).(*rtImportError); e.Code != "upstream_unavailable" {
		t.Fatalf("%+v", e)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestEmailDashTokenLines(t *testing.T) {
	got := parseRefreshTokenInput("Alice@Gmail.com----1//tokA\nbad----1//x")
	if got[0].Email != "Alice@Gmail.com" || got[0].RefreshToken != "1//tokA" || got[1].ParseError == "" {
		t.Fatalf("%+v", got)
	}
}

func TestUploadSingleFileArrayImportsAll(t *testing.T) {
	_, repo, engine, cleanup := newRTImportHarness(t)
	defer cleanup()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("file", "all.json")
	_, _ = w.Write([]byte(`[{"type":"antigravity","refresh_token":"rte1-1"},{"refresh_token":"rte2-1","email":"e2@x.io"},"rte3-1"]`))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/credentials", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if n := len(agCreds(t, repo)); n != 3 {
		t.Fatalf("want 3 creds, got %d: %s", n, rec.Body.String())
	}
	// raw body array (paste) dry-run
	req = httptest.NewRequest(http.MethodPost, "/credentials?dry_run=1", strings.NewReader(`["rte4-1","rte1-1"]`))
	rec = httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"would_create"`) || !strings.Contains(rec.Body.String(), `"would_update"`) {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestVendorExportLineDryRunAndTxtUpload(t *testing.T) {
	_, repo, engine, cleanup := newRTImportHarness(t)
	defer cleanup()
	line := vendorExportLine("user.x-y@gmail.com", "user.x-y@gmail.com", "proj-abc", "antigravity", "")
	code, got := postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"text": line, "dry_run": true, "reserve": true})
	if code != 200 {
		t.Fatalf("code %d %v", code, got)
	}
	sum := got["summary"].(map[string]any)
	if sum["created"].(float64) != 1 || sum["failed"].(float64) != 0 {
		t.Fatalf("summary %v", sum)
	}
	res := got["results"].([]any)[0].(map[string]any)
	if res["status"] != "would_create" || res["email"] != "user.x-y@gmail.com" || res["project_id"] != "proj-abc" {
		t.Fatalf("result %v", res)
	}
	raw := string(mustJSON(got))
	for _, secret := range []string{fakeExportPass, fakeExportID, fakeExportAccess, fakeRTShape} {
		if strings.Contains(raw, secret) {
			t.Fatalf("dry-run response leaked a secret field of len %d", len(secret))
		}
	}
	if n := len(agCreds(t, repo)); n != 0 {
		t.Fatalf("dry run wrote %d credentials", n)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("file", "export.txt")
	_, _ = w.Write([]byte(line + "\n"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/credentials?dry_run=1", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"would_create"`) {
		t.Fatalf("txt upload %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), fakeExportPass) || strings.Contains(rec.Body.String(), fakeExportAccess) {
		t.Fatal("txt upload response leaked discarded fields")
	}
	if n := len(agCreds(t, repo)); n != 0 {
		t.Fatalf("txt dry run wrote %d credentials", n)
	}
}

func TestFailedImportResultKeepsParsedEmailAndProject(t *testing.T) {
	_, repo, engine, cleanup := newRTImportHarness(t)
	defer cleanup()
	dead := "dead-grant-1"
	line := vendorExportLine("prefix@example.com", "fail.user@example.com", "proj-fail", "antigravity", "")
	// replace the synthetic RT with a token the fake enricher rejects
	line = strings.Replace(line, fakeRTShape, dead, 1)
	code, got := postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"text": line, "dry_run": true})
	if code != 200 {
		t.Fatalf("code %d %v", code, got)
	}
	res := got["results"].([]any)[0].(map[string]any)
	if res["status"] != "failed" || res["code"] != "token_dead" {
		t.Fatalf("result %v", res)
	}
	if res["email"] != "fail.user@example.com" {
		t.Fatalf("email=%v, want JSON email on failed enrich", res["email"])
	}
	if res["project_id"] != "proj-fail" {
		t.Fatalf("project_id=%v", res["project_id"])
	}
	if _, ok := res["index"]; !ok {
		t.Fatal("missing index")
	}
	raw := string(mustJSON(got))
	for _, secret := range []string{fakeExportPass, fakeExportID, fakeExportAccess, dead, fakeRTShape} {
		if strings.Contains(raw, secret) {
			t.Fatalf("failed result leaked a secret field of len %d", len(secret))
		}
	}
	if n := len(agCreds(t, repo)); n != 0 {
		t.Fatalf("dry run wrote %d credentials", n)
	}

	// invalid trailing JSON still reports the prefix email
	bad := "keep.me@example.com----" + fakeExportPass + "----" + fakeExportID + "----{\"refresh_token\": }"
	_, got = postJSON(t, engine, "/credentials/import/refresh-tokens", map[string]any{"text": bad, "dry_run": true})
	res = got["results"].([]any)[0].(map[string]any)
	if res["status"] != "failed" || res["code"] != "invalid_input" || res["reason"] != "line ends with invalid JSON" {
		t.Fatalf("bad json result %v", res)
	}
	if res["email"] != "keep.me@example.com" {
		t.Fatalf("prefix email missing on invalid JSON: %v", res["email"])
	}
	if strings.Contains(string(mustJSON(got)), fakeExportPass) {
		t.Fatal("invalid JSON response leaked password")
	}
}
