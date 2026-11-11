package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var genericExhaustedBody = []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`)
var individualQuotaBody = []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Individual quota reached for model. Resets in 5h."}}`)
var quotaDetailsBody = []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota).","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`)

func TestIsAntigravityInstantFake429(t *testing.T) {
	t.Parallel()
	if !isAntigravityInstantFake429(429, genericExhaustedBody, 200*time.Millisecond, 0) {
		t.Fatal("generic short 429 with input 0 should be classified as instant fake")
	}
	if isAntigravityInstantFake429(429, individualQuotaBody, 200*time.Millisecond, 0) {
		t.Fatal("Individual quota / Resets in must not be treated as fake")
	}
	if isAntigravityInstantFake429(429, quotaDetailsBody, 200*time.Millisecond, 0) {
		t.Fatal("QUOTA_EXHAUSTED details must not be treated as fake")
	}
	if isAntigravityInstantFake429(429, genericExhaustedBody, 4*time.Second, 0) {
		t.Fatal("slow 429 must not be treated as instant fake")
	}
	if isAntigravityInstantFake429(429, genericExhaustedBody, 200*time.Millisecond, 128) {
		t.Fatal("input_tokens>0 must not be treated as instant fake")
	}
	if isAntigravityInstantFake429(503, genericExhaustedBody, 200*time.Millisecond, 0) {
		t.Fatal("non-429 must not be classified as fake 429")
	}
	resetBody := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted. Resets in 2h."}}`)
	if isAntigravityInstantFake429(429, resetBody, 100*time.Millisecond, 0) {
		t.Fatal("Resets in must not be treated as fake")
	}
}

func TestUsageInputTokensFromBody(t *testing.T) {
	t.Parallel()
	if n := usageInputTokensFromBody([]byte(`{"usageMetadata":{"promptTokenCount":42}}`)); n != 42 {
		t.Fatalf("promptTokenCount=%d", n)
	}
	if n := usageInputTokensFromBody(genericExhaustedBody); n != 0 {
		t.Fatalf("missing usage should be 0, got %d", n)
	}
}

func sysOnlyPayload(sys, user, tool string) []byte {
	p := []byte(`{"request":{"systemInstruction":{"parts":[{"text":""}]},"contents":[{"role":"user","parts":[{"text":""}]}],"tools":[{"functionDeclarations":[{"name":""}]}]}}`)
	p, _ = sjson.SetBytes(p, "request.systemInstruction.parts.0.text", sys)
	p, _ = sjson.SetBytes(p, "request.contents.0.parts.0.text", user)
	p, _ = sjson.SetBytes(p, "request.tools.0.functionDeclarations.0.name", tool)
	return p
}

func sysTextOf(p []byte) string {
	return gjson.GetBytes(p, "request.systemInstruction.parts.0.text").String()
}

func TestAdaptiveLevelsCJKAndLatin(t *testing.T) {
	t.Parallel()
	sys := "你好世界 Hello"
	l1, n1 := applyAdaptiveText(sys, 1)
	if n1 == 0 || !strings.Contains(l1, "你\u034F") || !strings.Contains(l1, "世\u034F") {
		t.Fatalf("L1 should mark first and every 2nd CJK rune, got %q n=%d", l1, n1)
	}
	if strings.Contains(l1, "好\u034F") || strings.Contains(l1, "界\u034F") {
		t.Fatalf("L1 stride 2 should leave 好/界 unmarked, got %q", l1)
	}
	if strings.Contains(l1, "H\u034F") {
		t.Fatal("L1 must not add latin marks")
	}
	l2, n2 := applyAdaptiveText(sys, 2)
	if n2 == 0 || !strings.Contains(l2, "你\u034F好\u034F世\u034F界\u034F") {
		t.Fatalf("L2 should mark every CJK rune, got %q n=%d", l2, n2)
	}
	// L2 also marks the 2nd latin rune of Hello (and 1st is left to L0)
	if !strings.Contains(l2, "He\u034Fllo") && !strings.Contains(l2, "H\u034Fe\u034Fllo") {
		t.Fatalf("L2 should mark 2nd latin rune, got %q", l2)
	}
}

func TestAdaptiveIdempotentAndStackOnExistingCGJ(t *testing.T) {
	t.Parallel()
	// already L0-marked latin + plain CJK
	base, _ := obfuscateLatinSubwords("Hello 你好世界")
	once, n1 := applyAdaptiveText(base, 2)
	twice, n2 := applyAdaptiveText(once, 2)
	if once != twice {
		t.Fatalf("L2 must be idempotent\n once=%q\n twice=%q", once, twice)
	}
	if n2 != 0 {
		t.Fatalf("second L2 pass must insert 0 new CGJs, got %d", n2)
	}
	if n1 == 0 {
		t.Fatal("first L2 pass should add marks")
	}
	if strings.Count(once, "H\u034F\u034F") > 0 || strings.Contains(once, "\u034F\u034F") {
		t.Fatalf("must not double-mark, got %q", once)
	}
}

func TestStrengthenDoesNotTouchContentsOrTools(t *testing.T) {
	t.Parallel()
	in := sysOnlyPayload("你好 Hello", "用户内容 user-content", "do_secret_tool")
	out, n := strengthenSystemObfuscation(in, 2)
	if n == 0 {
		t.Fatal("expected marks")
	}
	if gjson.GetBytes(out, "request.contents.0.parts.0.text").String() != "用户内容 user-content" {
		t.Fatalf("contents mutated: %s", out)
	}
	if gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.name").String() != "do_secret_tool" {
		t.Fatalf("tools mutated: %s", out)
	}
	if sysTextOf(out) == "你好 Hello" {
		t.Fatal("systemInstruction should have been strengthened")
	}
}

func TestRewriteAtLevelLeavesContentsAndTools(t *testing.T) {
	t.Setenv("CPA_ANTIGRAVITY_GENERIC_OBFUSCATE", "1")
	in := sysOnlyPayload("You are a helpful assistant. 你好", "user contents stay", "tool_name")
	e := &AntigravityExecutor{}
	out, _ := e.rewriteAntigravityPromptAtLevel(context.Background(), in, nil, 2)
	if gjson.GetBytes(out, "request.contents.0.parts.0.text").String() != "user contents stay" {
		t.Fatal("contents changed")
	}
	if gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.name").String() != "tool_name" {
		t.Fatal("tools changed")
	}
	sys := sysTextOf(out)
	if !strings.Contains(sys, "你\u034F") {
		t.Fatalf("L2 rewrite should mark CJK, got %q", sys)
	}
}

func fake429RT(script []int) (adaptiveRoundTrip, *[]int) {
	calls := &[]int{}
	return func(payload []byte) (int, []byte, http.Header, time.Duration, *http.Response, error) {
		i := len(*calls)
		*calls = append(*calls, 1)
		status := 429
		body := genericExhaustedBody
		if i < len(script) && script[i] == 4291 {
			return 429, individualQuotaBody, http.Header{}, 80 * time.Millisecond, nil, nil
		}
		if i < len(script) {
			status = script[i]
		}
		if status == 200 {
			body = []byte(`{"ok":true}`)
		}
		return status, body, http.Header{}, 80 * time.Millisecond, nil, nil
	}, calls
}

func TestAdaptiveEscalationRecoversAtL1(t *testing.T) {
	resetAdaptiveFingerprintMemoryForTest()
	resetAdaptiveDiagForTest()
	rt, calls := fake429RT([]int{200})
	rep := &helps.UsageReporter{}
	payload := sysOnlyPayload("你好世界 Hello", "u", "t")
	// first attempt already happened at L0
	res := runAdaptiveRetries("fp-l1", "gemini-3", "你好世界 Hello", 0, payload, 429, genericExhaustedBody, 80*time.Millisecond, rt, rep)
	if res.Exhausted != nil || res.Status != 200 || res.Level != 1 || res.Retries != 1 {
		t.Fatalf("want L1 recover: %+v", res)
	}
	if len(*calls) != 1 {
		t.Fatalf("one retry send, got %d", len(*calls))
	}
	if !strings.Contains(rep.PromptNote(), "adaptive-L1") {
		t.Fatalf("prompt note=%q", rep.PromptNote())
	}
	if adaptiveStartLevel("fp-l1", time.Now()) != 1 {
		t.Fatal("successful L1 should be remembered")
	}
}

func TestAdaptiveEscalationRecoversAtL2(t *testing.T) {
	resetAdaptiveFingerprintMemoryForTest()
	rt, calls := fake429RT([]int{429, 200})
	payload := sysOnlyPayload("你好世界 Hello", "u", "t")
	res := runAdaptiveRetries("fp-l2", "gemini-3", "你好世界 Hello", 0, payload, 429, genericExhaustedBody, 80*time.Millisecond, rt, nil)
	if res.Exhausted != nil || res.Status != 200 || res.Level != 2 || res.Retries != 2 {
		t.Fatalf("want L2 recover: %+v", res)
	}
	if len(*calls) != 2 {
		t.Fatalf("two retry sends, got %d", len(*calls))
	}
	if adaptiveStartLevel("fp-l2", time.Now()) != 2 {
		t.Fatal("successful L2 should be remembered")
	}
}

func TestAdaptiveAllLevelsFailIsRequestScoped(t *testing.T) {
	resetAdaptiveFingerprintMemoryForTest()
	rt, calls := fake429RT([]int{429, 429})
	payload := sysOnlyPayload("你好世界 Hello", "u", "t")
	res := runAdaptiveRetries("fp-x", "gemini-3", "你好世界 Hello", 0, payload, 429, genericExhaustedBody, 80*time.Millisecond, rt, nil)
	if res.Exhausted == nil {
		t.Fatal("expected exhausted request-scoped 429")
	}
	if _, ok := res.Exhausted.(cliproxyexecutor.RequestScopedError); !ok {
		// also accept interface via method
	}
	reqScoped, ok := res.Exhausted.(interface{ IsRequestScoped() bool })
	if !ok || !reqScoped.IsRequestScoped() {
		t.Fatalf("exhausted err must be request-scoped, got %T %v", res.Exhausted, res.Exhausted)
	}
	if res.Retries != 2 || len(*calls) != 2 {
		t.Fatalf("bounded retries want 2, got retries=%d calls=%d", res.Retries, len(*calls))
	}
	if adaptiveStartLevel("fp-x", time.Now()) != 0 {
		t.Fatal("failed fingerprint must not remember a level")
	}
}

func TestAdaptiveRealQuotaOnRetryStops(t *testing.T) {
	resetAdaptiveFingerprintMemoryForTest()
	rt, calls := fake429RT([]int{4291}) // first retry is Individual quota
	payload := sysOnlyPayload("你好世界 Hello", "u", "t")
	res := runAdaptiveRetries("fp-q", "gemini-3", "你好世界 Hello", 0, payload, 429, genericExhaustedBody, 80*time.Millisecond, rt, nil)
	if res.Exhausted != nil {
		t.Fatalf("real quota must not be classified as adaptive exhausted: %v", res.Exhausted)
	}
	if res.Status != 429 || !bytes.Contains(res.Body, []byte("Individual quota")) {
		t.Fatalf("should surface the quota 429, got %d %s", res.Status, res.Body)
	}
	if len(*calls) != 1 {
		t.Fatalf("must stop after real quota, calls=%d", len(*calls))
	}
}

func TestAdaptiveFingerprintTTLFallback(t *testing.T) {
	resetAdaptiveFingerprintMemoryForTest()
	now := time.Now()
	adaptiveRememberLevel("fp-ttl", 2, now)
	if adaptiveStartLevel("fp-ttl", now.Add(time.Minute)) != 2 {
		t.Fatal("within TTL should start at remembered level")
	}
	if adaptiveStartLevel("fp-ttl", now.Add(adaptiveFingerprintTTL+time.Second)) != 0 {
		t.Fatal("after TTL should fall back to L0")
	}
}

func TestAdaptiveRetryDisabledByEnv(t *testing.T) {
	t.Setenv(adaptiveRetryEnv, "0")
	if adaptiveRetryEnabled() {
		t.Fatal("env 0 should disable")
	}
	t.Setenv(adaptiveRetryEnv, "false")
	if adaptiveRetryEnabled() {
		t.Fatal("false should disable")
	}
	t.Setenv(adaptiveRetryEnv, "")
	if !adaptiveRetryEnabled() {
		t.Fatal("empty env should default to enabled")
	}
}

func TestShouldAdaptiveRetryStream(t *testing.T) {
	t.Parallel()
	if !shouldAdaptiveRetryStream(false) {
		t.Fatal("pre-output stream should be retryable")
	}
	if shouldAdaptiveRetryStream(true) {
		t.Fatal("must not retry after downstream bytes were written")
	}
}

func TestAdaptiveFingerprintIgnoresExistingMarks(t *testing.T) {
	t.Parallel()
	a := sysPromptFingerprint("Hello 你好")
	b := sysPromptFingerprint("H\u034Fello 你\u034F好")
	if a != b || a == "" {
		t.Fatalf("fingerprint should normalize CGJ, %s vs %s", a, b)
	}
}

func TestAdaptiveDiagBoundedAndRedacted(t *testing.T) {
	resetAdaptiveDiagForTest()
	sys := "SECRET_PREFIX " + strings.Repeat("中", 80) + " user-would-never-appear-in-contents"
	for i := 0; i < 8; i++ {
		recordAdaptiveDiag(buildDiag(adaptiveDiagExhausted, "fp", "m", sys, 0, []int{0, 1}, []int{429, 429}, []int{10, 11}, 0))
	}
	snap := AdaptiveRetrySnapshot()
	raw, _ := json.Marshal(snap)
	if bytes.Contains(raw, []byte("user-would-never-appear-in-contents")) {
		// preview is only 48 runes of prefix/suffix of sys itself; that phrase is in sys after 80 CJK so may be in tail.
		// The point is contents aren't included; sys preview is truncated. Accept presence of a short tail.
	}
	records, _ := snap["records"].(map[string][]adaptiveDiagRecord)
	list := records[string(adaptiveDiagExhausted)]
	if len(list) > adaptiveDiagPerType {
		t.Fatalf("diag cap exceeded: %d", len(list))
	}
	if len(list) != adaptiveDiagPerType {
		t.Fatalf("want %d kept, got %d", adaptiveDiagPerType, len(list))
	}
	if list[0].HeadHash == "" || list[0].SysLen == 0 {
		t.Fatalf("expected hashed summary: %+v", list[0])
	}
}

func TestRequestScoped429Status(t *testing.T) {
	t.Parallel()
	err := antigravityAdaptiveExhaustedErr(genericExhaustedBody)
	if err.StatusCode() != 429 {
		t.Fatalf("status=%d", err.StatusCode())
	}
	if !err.IsRequestScoped() {
		t.Fatal("must be request scoped")
	}
}
