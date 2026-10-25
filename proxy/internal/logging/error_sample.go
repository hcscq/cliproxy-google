package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

// Error samples: on a failed request the proxy captures the full context
// (redacted client headers, client body, translated upstream request(s),
// upstream response, client-facing response) and forwards it to Home, which
// keeps the most recent N per error type. Capture is best effort and never
// blocks the request path: a per-type rate limit decides cheaply whether a
// sample is wanted, payloads are bounded, and the send queue drops when full.

const (
	errorSampleFieldMax     = 2 << 20 // per body field sent to Home; Home applies the panel cap
	errorSampleQueueSize    = 8
	errorSampleMinInterval  = 5 * time.Second // per type per proxy
	errorSampleMaxTypeState = 2000
)

type homeErrorSampleClient interface {
	HeartbeatOK() bool
	RPushErrorSample(ctx context.Context, payload []byte) error
}

var currentHomeErrorSampleClient = func() homeErrorSampleClient {
	c := home.Current()
	if c == nil {
		return nil
	}
	return c
}

type errorSampler struct {
	once  sync.Once
	queue chan []byte
	mu    sync.Mutex
	last  map[string]time.Time
}

var defaultErrorSampler = &errorSampler{last: make(map[string]time.Time)}

func (s *errorSampler) start() {
	s.once.Do(func() {
		s.queue = make(chan []byte, errorSampleQueueSize)
		go func() {
			for payload := range s.queue {
				client := currentHomeErrorSampleClient()
				if client == nil || !client.HeartbeatOK() {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = client.RPushErrorSample(ctx, payload)
				cancel()
			}
		}()
	})
}

// admit reports whether a sample of this type should be captured now.
func (s *errorSampler) admit(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.last[key]; ok && now.Sub(t) < errorSampleMinInterval {
		return false
	}
	if len(s.last) >= errorSampleMaxTypeState {
		s.last = make(map[string]time.Time)
	}
	s.last[key] = now
	return true
}

func (s *errorSampler) enqueue(payload []byte) bool {
	s.start()
	select {
	case s.queue <- payload:
		return true
	default:
		return false
	}
}

var (
	reErrSampleUUID   = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	reErrSampleHex    = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	reErrSampleNum    = regexp.MustCompile(`\d+(\.\d+)?`)
	reErrSampleNonTok = regexp.MustCompile(`[^a-z#*]+`)
	reErrSampleQuoted = regexp.MustCompile(`"[^"]{0,200}"|'[^']{0,200}'`)
)

// ErrorSampleType returns a stable, bounded error-type key such as
// "400:input-token-exceeds", "503:no-capacity" or "429:quota".
func ErrorSampleType(status int, texts ...string) string {
	code := strconv.Itoa(status)
	if status <= 0 {
		code = "err"
	}
	var msg string
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if m := gjson.Get(t, "error.message"); m.Exists() && strings.TrimSpace(m.String()) != "" {
			msg = m.String()
			break
		}
		if m := gjson.Get(t, "0.error.message"); m.Exists() && strings.TrimSpace(m.String()) != "" {
			msg = m.String()
			break
		}
		if msg == "" {
			msg = t
		}
	}
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(l, "input token count exceeds") || strings.Contains(l, "exceeds the maximum number of tokens") || strings.Contains(l, "prompt is too long") || strings.Contains(l, "context length"):
		return code + ":input-token-exceeds"
	case strings.Contains(l, "no capacity"):
		return code + ":no-capacity"
	case strings.Contains(l, "individual quota") || strings.Contains(l, "exhausted your capacity") || strings.Contains(l, "resource_exhausted") || strings.Contains(l, "resource exhausted") || strings.Contains(l, "quota"):
		return code + ":quota"
	case strings.Contains(l, "no first byte"):
		return code + ":first-byte-timeout"
	case strings.Contains(l, "deadline") || strings.Contains(l, "timeout") || strings.Contains(l, "timed out"):
		return code + ":timeout"
	case strings.Contains(l, "context canceled") || strings.Contains(l, "client disconnected"):
		return code + ":client-cancel"
	case strings.Contains(l, "unknown provider for model") || strings.Contains(l, "model_not_found") || strings.Contains(l, "model not found"):
		return code + ":model-not-found"
	case strings.Contains(l, "no auth available") || strings.Contains(l, "auth_not_found") || strings.Contains(l, "auth_unavailable"):
		return code + ":no-auth"
	case strings.Contains(l, "max_tokens") || strings.Contains(l, "maxoutputtokens") || strings.Contains(l, "max_output_tokens"):
		return code + ":max-tokens"
	case strings.Contains(l, "permission") || strings.Contains(l, "forbidden"):
		return code + ":forbidden"
	case strings.Contains(l, "unauthenticated") || strings.Contains(l, "invalid authentication") || strings.Contains(l, "invalid_grant"):
		return code + ":unauthenticated"
	}
	// Generic: normalize away quoted values, ids and numbers.
	l = reErrSampleQuoted.ReplaceAllString(l, "*")
	l = reErrSampleUUID.ReplaceAllString(l, "*")
	l = reErrSampleHex.ReplaceAllString(l, "*")
	l = reErrSampleNum.ReplaceAllString(l, "#")
	l = strings.Trim(reErrSampleNonTok.ReplaceAllString(l, "-"), "-")
	if len(l) > 48 {
		l = strings.TrimRight(l[:48], "-")
	}
	if l == "" {
		return code
	}
	return code + ":" + l
}

var (
	reErrSampleHeaderLine = regexp.MustCompile(`(?im)^(\s*(authorization|proxy-authorization|x-goog-api-key|x-api-key|api-key|cookie|set-cookie|x-cpa-[a-z-]*key[a-z-]*)\s*:\s*).*$`)
	reErrSampleTokenJSON  = regexp.MustCompile(`(?i)("(access_token|refresh_token|id_token|client_secret|api_key|apikey)"\s*:\s*")[^"]*(")`)
	reErrSampleYa29       = regexp.MustCompile(`ya29\.[A-Za-z0-9_\-\.]+`)
	reErrSampleQueryKey   = regexp.MustCompile(`(?i)([?&](key|api_key|access_token)=)[^&\s"]+`)
	reErrSampleBearer     = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9_\-\.=+/]{8,}`)
)

// RedactErrorSampleText removes credentials from captured text.
func RedactErrorSampleText(s string) string {
	if s == "" {
		return s
	}
	s = reErrSampleHeaderLine.ReplaceAllString(s, "${1}[REDACTED]")
	s = reErrSampleTokenJSON.ReplaceAllString(s, "${1}[REDACTED]${3}")
	s = reErrSampleYa29.ReplaceAllString(s, "ya29.[REDACTED]")
	s = reErrSampleQueryKey.ReplaceAllString(s, "${1}[REDACTED]")
	s = reErrSampleBearer.ReplaceAllString(s, "${1}[REDACTED]")
	return s
}

func redactErrorSampleHeaders(h map[string][]string) (map[string][]string, string) {
	out := make(map[string][]string, len(h))
	keyPrefix := ""
	for k, vs := range h {
		lk := strings.ToLower(strings.TrimSpace(k))
		switch lk {
		case "authorization", "proxy-authorization", "x-goog-api-key", "x-api-key", "api-key", "cookie", "set-cookie":
			if keyPrefix == "" && len(vs) > 0 {
				v := strings.TrimSpace(vs[0])
				v = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "Bearer "), "bearer "))
				if lk != "cookie" && lk != "set-cookie" && len(v) >= 6 {
					keyPrefix = v[:6]
				}
			}
			out[k] = []string{"[REDACTED]"}
			continue
		}
		cp := make([]string, len(vs))
		for i, v := range vs {
			cp[i] = RedactErrorSampleText(v)
		}
		out[k] = cp
	}
	return out, keyPrefix
}

type capWriter struct {
	buf   bytes.Buffer
	limit int
	total int64
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.total += int64(len(p))
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			w.buf.Write(p[:room])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func capErrorSampleField(data []byte, source *FileBodySource) string {
	w := &capWriter{limit: errorSampleFieldMax}
	if len(data) > 0 {
		_, _ = w.Write(data)
	}
	if source != nil && source.HasPayload() {
		if len(data) > 0 {
			_, _ = w.Write([]byte("\n\n"))
		}
		_, _ = source.WriteTo(io.Writer(w))
	}
	out := w.buf.String()
	if w.total > int64(w.buf.Len()) {
		out += "\n…[truncated for storage: " + strconv.FormatInt(w.total, 10) + " bytes total]"
	}
	return RedactErrorSampleText(out)
}

type errorSamplePayload struct {
	Type             string              `json:"type"`
	RequestID        string              `json:"request_id,omitempty"`
	Time             time.Time           `json:"time"`
	Status           int                 `json:"status"`
	Method           string              `json:"method,omitempty"`
	URL              string              `json:"url,omitempty"`
	ClientKeyPrefix  string              `json:"client_key_prefix,omitempty"`
	RequestedModel   string              `json:"requested_model,omitempty"`
	LatencyMs        int64               `json:"latency_ms,omitempty"`
	RequestHeaders   map[string][]string `json:"request_headers,omitempty"`
	RequestBody      string              `json:"request_body,omitempty"`
	UpstreamRequest  string              `json:"upstream_request,omitempty"`
	UpstreamResponse string              `json:"upstream_response,omitempty"`
	Response         string              `json:"response,omitempty"`
	Errors           []string            `json:"errors,omitempty"`
}

var reErrSampleModelPath = regexp.MustCompile(`/models/([^/:?]+)`)

// captureErrorSample builds and enqueues a sample; it never blocks.
func captureErrorSample(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, response []byte, apiRequest []byte, apiRequestSource *FileBodySource, apiResponse []byte, apiResponseSource *FileBodySource, apiResponseErrors []*interfaces.ErrorMessage, requestID string, requestTimestamp time.Time) {
	defer func() { _ = recover() }()
	if currentHomeErrorSampleClient() == nil {
		return
	}
	status := statusCode
	errTexts := make([]string, 0, len(apiResponseErrors))
	for _, e := range apiResponseErrors {
		if e == nil {
			continue
		}
		if e.StatusCode > 0 && (status < 400) {
			status = e.StatusCode
		}
		if e.Error != nil {
			errTexts = append(errTexts, e.Error.Error())
		}
	}
	if status < 400 && len(errTexts) == 0 {
		return
	}
	cands := append(append([]string{}, errTexts...), string(response))
	key := ErrorSampleType(status, cands...)
	now := time.Now()
	if !defaultErrorSampler.admit(key, now) {
		return
	}
	headers, keyPrefix := redactErrorSampleHeaders(requestHeaders)
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		if m := reErrSampleModelPath.FindStringSubmatch(url); len(m) == 2 {
			model = m[1]
		}
	}
	p := errorSamplePayload{
		Type: key, RequestID: requestID, Time: now.UTC(), Status: status, Method: method,
		URL: RedactErrorSampleText(url), ClientKeyPrefix: keyPrefix, RequestedModel: model,
		RequestHeaders: headers,
		RequestBody:      capErrorSampleField(body, nil),
		UpstreamRequest:  capErrorSampleField(apiRequest, apiRequestSource),
		UpstreamResponse: capErrorSampleField(apiResponse, apiResponseSource),
		Response:         capErrorSampleField(response, nil),
	}
	if !requestTimestamp.IsZero() {
		p.LatencyMs = now.Sub(requestTimestamp).Milliseconds()
	}
	for _, t := range errTexts {
		if len(t) > 8192 {
			t = t[:8192] + "…"
		}
		p.Errors = append(p.Errors, RedactErrorSampleText(t))
	}
	raw, err := json.Marshal(&p)
	if err != nil {
		return
	}
	defaultErrorSampler.enqueue(raw)
}
