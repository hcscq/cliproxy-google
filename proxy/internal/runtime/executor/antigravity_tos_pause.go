package executor

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Antigravity TOS_VIOLATION handling, ported from upstream CLIProxyAPI v8.0.70
// (commit 5a351d9, issue #7515) and adapted to the Home architecture:
//
//   - Detection: a 403 PERMISSION_DENIED whose google.rpc.ErrorInfo reason is
//     TOS_VIOLATION (the service-scoped ban wave signature).
//   - Single-credential deactivation is NOT done here. Home's account-block
//     guard already classifies this exact 403 as account_suspended and disables
//     the credential (persisted, visible reason, re-enable by toggle), so the
//     proxy does not mark it a second time.
//   - Optional pool-wide protective pause (antigravity.tos-pause-window): after
//     a TOS_VIOLATION every Antigravity request short-circuits with 503 for the
//     window, so sibling credentials are not hammered during a sweep. The
//     deadline is shared across proxy pods through the Home KV store. Unlike
//     upstream (default 6h) the pause is OFF unless an explicit positive
//     duration is configured. Transient 429s never arm or extend it.

const (
	antigravityTOSPauseKVKey  = "cpa:antigravity:tos-pause:until"
	antigravityTOSPausePool   = "pool"
	antigravityTOSPausedMsg   = "antigravity pool paused after TOS_VIOLATION"
	antigravityTOSReasonValue = "TOS_VIOLATION"
)

// antigravityTOSPauseUntil is the in-process copy of the pause deadline (used
// standalone and as a fast path in Home mode).
var antigravityTOSPauseUntil sync.Map

type antigravityTOSViolation struct {
	AppealURL     string
	AppealURLText string
}

// parseAntigravityTOSViolation extracts the TOS_VIOLATION signature from a
// Google RPC error body (or an SSE error frame). It returns nil for every other
// shape, including generic 429 RESOURCE_EXHAUSTED and non-TOS 403s.
func parseAntigravityTOSViolation(body []byte) *antigravityTOSViolation {
	if len(body) == 0 {
		return nil
	}
	if int(gjson.GetBytes(body, "error.code").Int()) != http.StatusForbidden {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "error.status").String()), "PERMISSION_DENIED") {
		return nil
	}
	details := gjson.GetBytes(body, "error.details")
	if !details.IsArray() {
		return nil
	}
	for _, detail := range details.Array() {
		if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(detail.Get("reason").String()), antigravityTOSReasonValue) {
			continue
		}
		meta := detail.Get("metadata")
		return &antigravityTOSViolation{
			AppealURL:     strings.TrimSpace(meta.Get("appeal_url").String()),
			AppealURLText: strings.TrimSpace(meta.Get("appeal_url_link_text").String()),
		}
	}
	return nil
}

// antigravityTOSPauseWindow resolves the configured pause window. Default OFF.
func antigravityTOSPauseWindow(cfg *config.Config) (time.Duration, bool) {
	if cfg == nil {
		return 0, false
	}
	raw := strings.ToLower(strings.TrimSpace(cfg.Antigravity.TOSPauseWindow))
	switch raw {
	case "", "0", "off", "false", "none", "disabled", "no":
		return 0, false
	}
	window, err := time.ParseDuration(raw)
	if err != nil || window <= 0 {
		// An unparseable value must not silently enable a multi-hour outage.
		return 0, false
	}
	return window, true
}

// observeAntigravityTOS is called on every upstream 403 body (HTTP error or
// SSE error frame). On a TOS_VIOLATION it logs the event (appeal URL without
// query/fragment) and arms the optional pause. No-op for anything else.
func (e *AntigravityExecutor) observeAntigravityTOS(auth *cliproxyauth.Auth, statusCode int, body []byte) {
	if statusCode != http.StatusForbidden {
		return
	}
	violation := parseAntigravityTOSViolation(body)
	if violation == nil {
		return
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	armed := markAntigravityTOSPause(cfg)
	log.WithFields(log.Fields{
		"auth_id":      authID,
		"appeal_link":  violation.AppealURL != "",
		"pause_armed":  armed,
		"credential":   "deactivated by home account-block guard",
		"tos_reason":   antigravityTOSReasonValue,
		"pause_config": cfg != nil && strings.TrimSpace(cfg.Antigravity.TOSPauseWindow) != "",
	}).Warn("antigravity executor: upstream TOS_VIOLATION")
}

// markAntigravityTOSPause arms the pool-wide pause when enabled. Returns true
// when a pause was armed.
func markAntigravityTOSPause(cfg *config.Config) bool {
	window, enabled := antigravityTOSPauseWindow(cfg)
	if !enabled {
		return false
	}
	until := time.Now().Add(window)
	antigravityTOSPauseUntil.Store(antigravityTOSPausePool, until)
	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("antigravity executor: home kv tos pause write error: %v", errClient)
		} else {
			payload := []byte(strconv.FormatInt(until.UnixNano(), 10))
			if _, errSet := client.KVSet(context.Background(), antigravityTOSPauseKVKey, payload, homekv.KVSetOptions{EX: window + 5*time.Second}); errSet != nil {
				log.Errorf("antigravity executor: home kv tos pause write error: %v", errSet)
			}
		}
	}
	log.Warnf("antigravity executor: TOS_VIOLATION pool pause armed for %s (until %s)", window, until.Format(time.RFC3339))
	return true
}

// antigravityInTOSPause reports whether the Antigravity pool is paused. It
// returns false without touching the KV store when the pause is disabled.
func antigravityInTOSPause(cfg *config.Config) bool {
	if _, enabled := antigravityTOSPauseWindow(cfg); !enabled {
		return false
	}
	now := time.Now()
	if value, ok := antigravityTOSPauseUntil.Load(antigravityTOSPausePool); ok {
		if until, okTime := value.(time.Time); okTime && until.After(now) {
			return true
		}
		antigravityTOSPauseUntil.CompareAndDelete(antigravityTOSPausePool, value)
	}
	if client, homeMode, errClient := currentAntigravityKVClient(); homeMode {
		if errClient != nil {
			return false
		}
		raw, found, errGet := client.KVGet(context.Background(), antigravityTOSPauseKVKey)
		if errGet != nil || !found {
			return false
		}
		untilNano, errParse := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if errParse != nil {
			return false
		}
		until := time.Unix(0, untilNano)
		if until.After(now) {
			antigravityTOSPauseUntil.Store(antigravityTOSPausePool, until)
			return true
		}
	}
	return false
}

// antigravityTOSPausedErr is returned while the pool pause is active.
func antigravityTOSPausedErr() statusErr {
	return statusErr{code: http.StatusServiceUnavailable, msg: antigravityTOSPausedMsg}
}
