package home

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/redisx"
	log "github.com/sirupsen/logrus"
)

// Cross-Home quota cooldown sync. Proxies report every upstream attempt to the Home
// they are connected to; that Home applies the 429 cooldown locally at once and
// persists it, but the other Home only learned it after the persisted transition and
// the cluster event round trip, and kept dispatching the exhausted (credential, model)
// pair meanwhile. The observing Home now publishes the cooldown on Redis right away and
// every other Home applies it to its in-memory state (never persisted from here; the
// persisted transition arrives later and wins). The same channel carries "probe now"
// requests so the quota prober (leader only) reacts to any Home's quota collector.

const quotaSyncChannel = "quota-sync"

// quotaSyncApplied counts remote cooldowns that changed this Home's state (logged).
var quotaSyncApplied atomic.Int64

var quotaSyncOrigin = func() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}()

type quotaSyncMsg struct {
	Origin  string `json:"o"`
	Kind    string `json:"k"` // cool | probe
	AuthID  string `json:"a"`
	Model   string `json:"m"`
	UntilMS int64  `json:"u,omitempty"`
	Code    string `json:"c,omitempty"`
	Message string `json:"msg,omitempty"`
}

func publishQuotaSync(msg quotaSyncMsg) {
	rc := redisx.Default()
	if rc == nil || !rc.Healthy() {
		return
	}
	msg.Origin = quotaSyncOrigin
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	ctx, cancel := rc.Ctx(context.Background())
	defer cancel()
	rc.Observe(rc.Raw().Publish(ctx, rc.Key(quotaSyncChannel), raw).Err())
}

// quotaCooldownUntil mirrors the deadline the auth manager derives for a 429 result.
func quotaCooldownUntil(result coreauth.Result, now time.Time) time.Time {
	if result.ResetAt != nil && result.ResetAt.After(now) {
		return result.ResetAt.UTC()
	}
	if result.RetryAfter != nil && *result.RetryAfter > 0 {
		return now.Add(*result.RetryAfter)
	}
	return now.Add(coreauth.QuotaShortCooldown())
}

// blockAndPublishQuotaCooldown fast-blocks a pair on this Home the moment a proxy reports
// its 429 and broadcasts the block to the other Homes.
func (r *Runtime) blockAndPublishQuotaCooldown(authID string, result coreauth.Result) {
	if result.Success || result.Error == nil || result.Error.HTTPStatus != http.StatusTooManyRequests || strings.TrimSpace(authID) == "" {
		return
	}
	if r.coreManager != nil && r.coreManager.QuotaCooldownDisabledFor(authID) {
		return
	}
	coreauth.MarkQuotaBlocked(authID, result.Model, quotaCooldownUntil(result, time.Now()))
	msg := strings.TrimSpace(result.Error.Message)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	publishQuotaSync(quotaSyncMsg{Kind: "cool", AuthID: authID, Model: result.Model,
		UntilMS: quotaCooldownUntil(result, time.Now()).UnixMilli(), Code: result.Error.Code, Message: msg})
}

func (r *Runtime) handleQuotaSync(raw string) {
	var msg quotaSyncMsg
	if json.Unmarshal([]byte(raw), &msg) != nil || msg.Origin == quotaSyncOrigin || r.coreManager == nil {
		return
	}
	switch msg.Kind {
	case "cool":
		if coreauth.MarkQuotaBlocked(msg.AuthID, msg.Model, time.UnixMilli(msg.UntilMS)) {
			quotaSyncApplied.Add(1)
		}
	case "clear":
		coreauth.ClearQuotaBlock(msg.AuthID, msg.Model)
	case "probe":
		r.prober.probeNow(msg.AuthID, msg.Model)
	}
}

func (r *Runtime) startQuotaSync(ctx context.Context) {
	coreauth.QuotaBlockClearHook.Store(func(authID, model string) {
		publishQuotaSync(quotaSyncMsg{Kind: "clear", AuthID: authID, Model: model})
	})
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := quotaSyncApplied.Swap(0); n > 0 {
					log.Infof("quota sync: applied %d remote 429 cooldown(s) in the last 5m", n)
				}
			}
		}
	}()
	go func() {
		for ctx.Err() == nil {
			rc := redisx.Default()
			if rc == nil || !rc.Healthy() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
			sub := rc.Raw().Subscribe(ctx, rc.Key(quotaSyncChannel))
			ch := sub.Channel()
		recv:
			for {
				select {
				case <-ctx.Done():
					break recv
				case m, ok := <-ch:
					if !ok {
						break recv
					}
					r.handleQuotaSync(m.Payload)
				}
			}
			_ = sub.Close()
			if ctx.Err() == nil {
				log.Debug("quota sync: subscription ended; resubscribing")
				time.Sleep(time.Second)
			}
		}
	}()
}
