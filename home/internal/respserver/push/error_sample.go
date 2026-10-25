package push

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/errorsamples"
	"github.com/router-for-me/CLIProxyAPIHome/internal/respserver/dispatch"
	"github.com/tidwall/gjson"
)

var errorSampleBodyFields = []string{"request_body", "upstream_request", "upstream_response", "response"}

// handleErrorSample accepts a failed-request sample from a CPA proxy and
// queues it for storage (most recent N per type). It never blocks.
func handleErrorSample(ctx context.Context, env dispatch.Env, args []string) dispatch.Reply {
	if len(args) != 3 {
		return dispatch.Err("wrong number of arguments for 'rpush' command")
	}
	if !strings.EqualFold(strings.TrimSpace(args[1]), "error-sample") {
		return dispatch.Err("unsupported key")
	}
	settings := errorsamples.Get()
	payload := args[2]
	typ := strings.TrimSpace(gjson.Get(payload, "type").String())
	if typ == "" || len(typ) > 96 {
		return dispatch.Integer(0)
	}
	keep := settings.Keep(typ)
	if keep <= 0 || env.Runtime == nil {
		return dispatch.Integer(0)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return dispatch.Err("invalid error-sample json")
	}
	clientIP := strings.TrimSpace(env.ClientIP)
	obj["proxy_ip"] = clientIP
	if env.NodeID != "" {
		obj["proxy_node"] = env.NodeID
	}
	CapErrorSample(obj, settings.MaxBytes)
	raw, err := json.Marshal(obj)
	if err != nil {
		return dispatch.Integer(0)
	}
	rec := &errorsamples.Record{
		Type:           typ,
		CreatedAt:      time.Now().UTC(),
		Status:         int(gjson.Get(payload, "status").Int()),
		RequestedModel: truncate(gjson.Get(payload, "requested_model").String(), 128),
		RequestID:      truncate(gjson.Get(payload, "request_id").String(), 64),
		ClientIP:       clientIP,
	}
	if env.Runtime.EnqueueErrorSample(rec, raw, keep, settings.MaxTypes) {
		return dispatch.Integer(1)
	}
	return dispatch.Integer(0)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// CapErrorSample truncates the body fields so their total fits maxBytes,
// shrinking the largest fields first (fair share), with a visible marker.
func CapErrorSample(obj map[string]any, maxBytes int) {
	type f struct {
		key string
		val string
	}
	fields := make([]f, 0, len(errorSampleBodyFields))
	total := 0
	for _, k := range errorSampleBodyFields {
		if v, ok := obj[k].(string); ok && v != "" {
			fields = append(fields, f{k, v})
			total += len(v)
		}
	}
	if total <= maxBytes || len(fields) == 0 {
		return
	}
	// Fair share: fields smaller than the share keep everything.
	budget := maxBytes
	remaining := len(fields)
	limits := map[string]int{}
	for {
		share := budget / remaining
		changed := false
		for _, x := range fields {
			if _, done := limits[x.key]; done {
				continue
			}
			if len(x.val) <= share {
				limits[x.key] = len(x.val)
				budget -= len(x.val)
				remaining--
				changed = true
			}
		}
		if !changed || remaining == 0 {
			for _, x := range fields {
				if _, done := limits[x.key]; !done {
					limits[x.key] = share
				}
			}
			break
		}
	}
	for _, x := range fields {
		if lim := limits[x.key]; len(x.val) > lim {
			cut := lim
			for cut > 0 && cut < len(x.val) && (x.val[cut]&0xC0) == 0x80 {
				cut--
			}
			obj[x.key] = x.val[:cut] + "\n…[truncated: " + strconv.Itoa(len(x.val)) + " bytes total]"
		}
	}
	obj["truncated"] = true
}
