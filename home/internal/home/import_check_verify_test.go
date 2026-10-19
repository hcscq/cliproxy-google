package home

import "testing"

func TestExtractValidationURL(t *testing.T) {
	errInfo := []byte(`{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/signin/continue?sarp=1&x=1"}},{"@type":"type.googleapis.com/google.rpc.Help","links":[{"description":"Learn more","url":"https://support.google.com/accounts?p=al_alert"}]}]}}`)
	if got := ExtractValidationURL(errInfo); got != "https://accounts.google.com/signin/continue?sarp=1&x=1" {
		t.Fatalf("errinfo: %q", got)
	}
	helpOnly := []byte(`{"error":{"details":[{"links":[{"url":"https://support.google.com/x"},{"url":"https://accounts.google.com/signin/continue?y=2"}]}]}}`)
	if got := ExtractValidationURL(helpOnly); got != "https://accounts.google.com/signin/continue?y=2" {
		t.Fatalf("help: %q", got)
	}
	lca := []byte(`{"ineligibleTiers":[{"reasonCode":"VALIDATION_REQUIRED","validationUrl":"https://accounts.google.com/v?z=3"}]}`)
	if got := ExtractValidationURL(lca); got != "https://accounts.google.com/v?z=3" {
		t.Fatalf("loadCodeAssist: %q", got)
	}
	for _, b := range []string{``, `not json`, `{"error":{"code":403,"message":"This service has been disabled"}}`, `{"validation_url":"http://insecure"}`} {
		if got := ExtractValidationURL([]byte(b)); got != "" {
			t.Fatalf("%q -> %q", b, got)
		}
	}
}
