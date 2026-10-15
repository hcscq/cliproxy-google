package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestClearAccountBlockOnReauth(t *testing.T) {
	a := &coreauth.Auth{Disabled: true, Status: coreauth.StatusDisabled, StatusMessage: "需验证账号（403）",
		Metadata: map[string]any{home.AccountBlockReasonKey: "account_verification_required", "auto_disabled_at": "x", "disabled": true}}
	if !clearAccountBlockOnReauth(a, time.Now()) || a.Disabled || a.Status != coreauth.StatusActive || a.Metadata[home.AccountBlockReasonKey] != nil {
		t.Fatalf("auto block not cleared: %+v", a)
	}
	m := &coreauth.Auth{Disabled: true, Status: coreauth.StatusDisabled, Metadata: map[string]any{"disabled": true}}
	if clearAccountBlockOnReauth(m, time.Now()) || !m.Disabled {
		t.Fatal("manual disable must be kept")
	}
}
