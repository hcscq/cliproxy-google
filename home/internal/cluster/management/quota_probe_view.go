package management

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

// annotateQuotaProbes adds the prober's last result and the live cooldown settings to
// every cooling per-model 429 entry of the given credential list entries.
func (h *Handler) annotateQuotaProbes(ctx context.Context, entries []gin.H) {
	if h == nil || h.runtime == nil {
		return
	}
	cfg := h.oauthConfig()
	shortSecs := int(cfg.QuotaShortCooldown() / time.Second)
	probeSecs := int(cfg.QuotaProbeInterval() / time.Second)
	var keys []string
	var refs []gin.H
	for _, e := range entries {
		id := fmt.Sprint(e["id"])
		list, _ := e["model_cooldowns"].([]gin.H)
		for _, item := range list {
			if item["cooling"] != true || item["http_status"] != 429 {
				continue
			}
			if item["error_code"] == home.QuotaGroupExhaustedCode || item["error_code"] == home.QuotaGroupLowCode {
				// Group cooldown: released by catalog refresh / the group's representative probe.
				item["group_cooldown"] = true
				continue
			}
			if item["error_code"] == home.QuotaBucketExhaustedCode {
				// Re-checked by the credential quota probe, not by per-model probes.
				item["bucket_exhausted"] = true
				continue
			}
			if item["error_code"] == coreauth.ModelQuotaExhaustedCode {
				// Explicit per-model QUOTA_EXHAUSTED: a hidden per-model quota window.
				item["model_quota"] = true
			}
			item["probe_managed"] = true
			item["quota_short_cooldown_seconds"] = shortSecs
			item["quota_probe_interval_seconds"] = probeSecs
			keys = append(keys, home.QuotaProbeKey(id, fmt.Sprint(item["model"])))
			refs = append(refs, item)
		}
	}
	if len(keys) == 0 {
		return
	}
	kctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	vals, err := h.runtime.KVMGet(kctx, keys)
	if err != nil {
		return
	}
	for i, v := range vals {
		if i >= len(refs) || !v.Found {
			continue
		}
		var st home.QuotaProbeStatus
		if json.Unmarshal(v.Value, &st) == nil {
			refs[i]["probe"] = st
		}
	}
}
