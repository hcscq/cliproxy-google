package management

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestClearAccountBlockOnReauth(t *testing.T) {
	now := time.Now().UTC()

	t.Run("403 verify auto block is cleared", func(t *testing.T) {
		a := &coreauth.Auth{
			Disabled: true, Status: coreauth.StatusDisabled, StatusMessage: "需验证账号（403）",
			Metadata: map[string]any{home.AccountBlockReasonKey: "account_verification_required", "auto_disabled_at": "x", "disabled": true},
		}
		if !clearAccountBlockOnReauth(a, now) || a.Disabled || a.Status != coreauth.StatusActive || a.StatusMessage != "" || a.LastError != nil || a.Unavailable {
			t.Fatalf("auto block not cleared: %+v", a)
		}
		if a.Metadata[home.AccountBlockReasonKey] != nil || a.Metadata["auto_disabled_at"] != nil {
			t.Fatalf("auto reason not deleted: %+v", a.Metadata)
		}
		if a.Metadata["disabled"] != false || a.Metadata[home.AccountBlockClearedKey] == nil {
			t.Fatalf("cleared markers missing: %+v", a.Metadata)
		}
	})

	t.Run("401 unauthorized is cleared", func(t *testing.T) {
		a := &coreauth.Auth{
			Disabled: true, Status: coreauth.StatusDisabled, StatusMessage: "unauthorized",
			Unavailable: true,
			LastError: &coreauth.Error{
				Code: "authentication_error", Message: "credential unauthorized", HTTPStatus: http.StatusUnauthorized,
			},
			LastRefreshError: &coreauth.Error{Code: "authentication_error", HTTPStatus: http.StatusUnauthorized},
			Metadata:         map[string]any{"disabled": true},
			ModelStates: map[string]*coreauth.ModelState{
				"gemini-2.5-flash": {
					Status: coreauth.StatusDisabled, StatusMessage: "unauthorized", Unavailable: true,
					LastError: &coreauth.Error{Message: "Unauthorized", HTTPStatus: http.StatusUnauthorized},
				},
			},
		}
		if !clearAccountBlockOnReauth(a, now) {
			t.Fatal("401 should be re-enabled")
		}
		if a.Disabled || a.Status != coreauth.StatusActive || a.Unavailable || a.StatusMessage != "" || a.LastError != nil || a.LastRefreshError != nil || !a.NextRetryAfter.IsZero() {
			t.Fatalf("401 auth state not cleared: %+v", a)
		}
		if a.Metadata["disabled"] != false {
			t.Fatalf("metadata.disabled = %v, want false", a.Metadata["disabled"])
		}
		st := a.ModelStates["gemini-2.5-flash"]
		if st == nil || coreauth.IsUnauthorizedModelState(st) || st.Unavailable || st.Status != coreauth.StatusActive || st.LastError != nil {
			t.Fatalf("401 model state not cleared: %+v", st)
		}
	})

	t.Run("401 via LastError only is cleared", func(t *testing.T) {
		a := &coreauth.Auth{
			Disabled: true, Status: coreauth.StatusDisabled, Unavailable: true,
			LastError: &coreauth.Error{Code: "authentication_error", HTTPStatus: http.StatusUnauthorized},
			Metadata:  map[string]any{"disabled": true},
		}
		if !clearAccountBlockOnReauth(a, now) || a.Disabled || a.Status != coreauth.StatusActive || a.LastError != nil || a.Unavailable {
			t.Fatalf("LastError 401 not cleared: %+v", a)
		}
	})

	t.Run("manual disable is kept", func(t *testing.T) {
		m := &coreauth.Auth{Disabled: true, Status: coreauth.StatusDisabled, Metadata: map[string]any{"disabled": true}}
		if clearAccountBlockOnReauth(m, now) || !m.Disabled || m.Status != coreauth.StatusDisabled {
			t.Fatal("manual disable (no message) must be kept")
		}
		m2 := &coreauth.Auth{
			Disabled: true, Status: coreauth.StatusDisabled, StatusMessage: "disabled via management API",
			Metadata: map[string]any{"disabled": true},
		}
		if clearAccountBlockOnReauth(m2, now) || !m2.Disabled || m2.StatusMessage != "disabled via management API" {
			t.Fatal("manual disable via management API must be kept")
		}
	})

	t.Run("TOS account_suspended is kept", func(t *testing.T) {
		a := &coreauth.Auth{
			Disabled: true, Status: coreauth.StatusDisabled,
			StatusMessage: "账号已停用/违反条款（403）",
			LastError:     &coreauth.Error{Code: "account_suspended", HTTPStatus: http.StatusForbidden},
			Metadata: map[string]any{
				home.AccountBlockReasonKey: "account_suspended",
				"auto_disabled_at":         "x",
				"disabled":                 true,
			},
		}
		if clearAccountBlockOnReauth(a, now) || !a.Disabled || a.Status != coreauth.StatusDisabled {
			t.Fatalf("TOS disable must be kept: %+v", a)
		}
		if a.Metadata[home.AccountBlockReasonKey] != "account_suspended" {
			t.Fatalf("TOS auto reason must be kept: %+v", a.Metadata)
		}
	})
}
