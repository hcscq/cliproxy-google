package management

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Fake token with the same shape as real Google refresh tokens ("1//06" prefix, contains '-').
const fakeRTShape = "1//06FakeTokenCgYIARAAGAYSNwF-L9IrFakeFakeFakeFake-abc_DEF0123456789xyzE68s"

func TestParseRefreshTokenInput_EmailSeparatorVariants(t *testing.T) {
	cases := map[string]string{
		"plain":           "user.x-y@gmail.com----" + fakeRTShape,
		"crlf":            "user.x-y@gmail.com----" + fakeRTShape + "\r\n",
		"bom_zero_width":  "\ufeffuser.x-y@gmail.com\u200b----" + fakeRTShape + "\u200b",
		"three_dashes":    "user.x-y@gmail.com---" + fakeRTShape,
		"five_dashes":     "user.x-y@gmail.com-----" + fakeRTShape,
		"fullwidth":       "user.x-y@gmail.com－－－－" + fakeRTShape,
		"em_dash":         "user.x-y@gmail.com——" + "——" + fakeRTShape,
		"spaces":          "user.x-y@gmail.com ---- " + fakeRTShape,
		"nbsp":            "\u00a0user.x-y@gmail.com----" + fakeRTShape + "\u00a0",
		"extra_fields":    "user.x-y@gmail.com----" + fakeRTShape + "----password----x",
		"cr_only_newline": "user.x-y@gmail.com----" + fakeRTShape + "\r",
	}
	for name, in := range cases {
		got := parseRefreshTokenInput(in)
		if len(got) != 1 || got[0].ParseError != "" || got[0].RefreshToken != fakeRTShape || got[0].Email != "user.x-y@gmail.com" {
			t.Errorf("%s: got %+v", name, got)
		}
	}
	if got := parseRefreshTokenInput(fakeRTShape); len(got) != 1 || got[0].RefreshToken != fakeRTShape || got[0].Email != "" {
		t.Errorf("bare token: %+v", got)
	}
	if got := parseRefreshTokenInput("notanemail----" + fakeRTShape); len(got) != 1 || got[0].ParseError == "" {
		t.Errorf("missing @ should fail: %+v", got)
	}
}

const (
	fakeExportPass   = "FakePassw0rd!x"
	fakeExportID     = "0123456789abcdef0123456789abcdef"
	fakeExportAccess = "ya29.FakeAccessTokenNotUsed"
)

func vendorExportLine(prefixEmail, jsonEmail, project, typ string, rawJSON string) string {
	if rawJSON != "" {
		return prefixEmail + "----" + fakeExportPass + "----" + fakeExportID + "----" + rawJSON
	}
	obj := map[string]any{
		"type": typ, "email": jsonEmail, "refresh_token": fakeRTShape,
		"project_id": project, "access_token": fakeExportAccess,
		"expires_in": 3600, "expired": "2020-01-01T00:00:00Z", "disabled": false,
	}
	b, _ := json.Marshal(obj)
	return prefixEmail + "----" + fakeExportPass + "----" + fakeExportID + "----" + string(b)
}

func assertNoExportSecrets(t *testing.T, s string) {
	t.Helper()
	for _, secret := range []string{fakeExportPass, fakeExportID, fakeExportAccess} {
		if strings.Contains(s, secret) {
			t.Errorf("value leaked a discarded export field (len=%d)", len(secret))
		}
	}
}

func TestParseRefreshTokenInput_VendorExportLines(t *testing.T) {
	line := vendorExportLine("user.x-y@gmail.com", "user.x-y@gmail.com", "proj-abc", "antigravity", "")
	got := parseRefreshTokenInput(line)
	if len(got) != 1 {
		t.Fatalf("entries=%d", len(got))
	}
	e := got[0]
	if e.ParseError != "" || e.RefreshToken != fakeRTShape || e.Email != "user.x-y@gmail.com" || e.ProjectID != "proj-abc" || e.Warning != "" {
		t.Fatalf("entry email=%q project=%q err=%q warn=%q token_ok=%v", e.Email, e.ProjectID, e.ParseError, e.Warning, e.RefreshToken == fakeRTShape)
	}
	if e.Extra["access_token"] != nil || e.Extra["disabled"] != nil || e.Extra["password"] != nil {
		t.Fatalf("discarded fields leaked into Extra: %#v", e.Extra)
	}
	assertNoExportSecrets(t, e.ParseError+e.Warning+e.Source)
}

func TestParseRefreshTokenInput_VendorExportEmailMismatchUsesJSON(t *testing.T) {
	line := vendorExportLine("prefix@gmail.com", "json-acct@gmail.com", "proj-abc", "antigravity", "")
	got := parseRefreshTokenInput(line)
	if len(got) != 1 || got[0].ParseError != "" {
		t.Fatalf("parse failed: %+v", len(got))
	}
	e := got[0]
	if e.Email != "json-acct@gmail.com" {
		t.Fatalf("email=%q, want JSON email", e.Email)
	}
	if !strings.Contains(e.Warning, "prefix@gmail.com") || !strings.Contains(e.Warning, "json-acct@gmail.com") || !strings.Contains(e.Warning, "using the JSON email") {
		t.Fatalf("warning=%q", e.Warning)
	}
	assertNoExportSecrets(t, e.Warning)
}

func TestParseRefreshTokenInput_VendorExportMixedAndBadLines(t *testing.T) {
	good := vendorExportLine("user.x-y@gmail.com", "user.x-y@gmail.com", "proj-abc", "antigravity", "")
	badJSON := "user.x-y@gmail.com----" + fakeExportPass + "----" + fakeExportID + "----{\"refresh_token\":"
	// close it so trailingJSONObject matches, but Unmarshal fails
	badJSON += " }"
	missingRT := vendorExportLine("user.x-y@gmail.com", "user.x-y@gmail.com", "proj-abc", "antigravity", `{"type":"antigravity","email":"user.x-y@gmail.com","project_id":"proj-abc"}`)
	wrongType := vendorExportLine("user.x-y@gmail.com", "user.x-y@gmail.com", "proj-abc", "codex", "")
	mixed := strings.Join([]string{
		fakeRTShape,
		"alice@gmail.com----" + fakeRTShape,
		good,
		badJSON,
		missingRT,
		wrongType,
	}, "\n")
	got := parseRefreshTokenInput(mixed)
	if len(got) != 6 {
		t.Fatalf("entries=%d, want 6", len(got))
	}
	if got[0].ParseError != "" || got[0].RefreshToken != fakeRTShape || got[0].Email != "" {
		t.Fatalf("bare token: err=%q email=%q", got[0].ParseError, got[0].Email)
	}
	if got[1].ParseError != "" || got[1].Email != "alice@gmail.com" || got[1].RefreshToken != fakeRTShape {
		t.Fatalf("email----rt: err=%q email=%q", got[1].ParseError, got[1].Email)
	}
	if got[2].ParseError != "" || got[2].ProjectID != "proj-abc" || got[2].Email != "user.x-y@gmail.com" {
		t.Fatalf("vendor good: err=%q email=%q project=%q", got[2].ParseError, got[2].Email, got[2].ProjectID)
	}
	if got[3].ParseError != "line ends with invalid JSON" {
		t.Fatalf("bad json err=%q", got[3].ParseError)
	}
	if got[4].ParseError != "object has no refresh_token" {
		t.Fatalf("missing rt err=%q", got[4].ParseError)
	}
	if !strings.Contains(got[5].ParseError, "object type is codex") {
		t.Fatalf("wrong type err=%q", got[5].ParseError)
	}
	for i, e := range got {
		assertNoExportSecrets(t, e.ParseError+e.Warning)
		if strings.Contains(e.ParseError, fakeRTShape) {
			t.Errorf("item %d ParseError leaked refresh token", i)
		}
	}
}

func TestParseRefreshTokenInput_VendorExportOptionalFile(t *testing.T) {
	path := os.Getenv("CPA_RT_PARSE_FILE")
	if path == "" {
		t.Skip("set CPA_RT_PARSE_FILE to parse a file without importing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := parseRefreshTokenInput(string(data))
	if len(got) == 0 {
		t.Fatal("no entries")
	}
	var failed []int
	for _, e := range got {
		if e.ParseError != "" || e.RefreshToken == "" || e.Email == "" {
			failed = append(failed, e.Index+1)
		}
		if e.ParseError != "" && (strings.Contains(e.ParseError, "1//") || strings.Contains(e.ParseError, "----")) {
			t.Errorf("line %d error may contain secret material", e.Index+1)
		}
	}
	if len(failed) > 0 {
		t.Fatalf("parse failed on %d/%d lines: %v", len(failed), len(got), failed)
	}
	t.Logf("parsed %d entries, 0 errors", len(got))
}

func TestLineOrientedRTPayloadSkipsWholeJSON(t *testing.T) {
	if _, ok := lineOrientedRTPayload([]byte(`[{"refresh_token":"1//x"}]`), "x.json"); ok {
		t.Fatal("JSON array should not be claimed as line-oriented")
	}
	many, ok := lineOrientedRTPayload([]byte(vendorExportLine("a@gmail.com", "a@gmail.com", "p", "antigravity", "")), "export.txt")
	if !ok || len(many) != 1 || many[0].Source != "export.txt" || many[0].Email != "a@gmail.com" {
		t.Fatalf("txt payload: ok=%v n=%d", ok, len(many))
	}
}
