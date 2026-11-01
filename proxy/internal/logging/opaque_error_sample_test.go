package logging

import (
	"strings"
	"testing"
	"time"
)

func TestErrorSampleTypeOpaqueAndThinking(t *testing.T) {
	if got := ErrorSampleType(500, `{"error":{"code":500,"message":"Unknown Error.","status":"UNKNOWN"}}`); got != "500:unknown-error" {
		t.Fatalf("unknown error type = %s", got)
	}
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`
	if got := ErrorSampleType(400, body); got != "400:invalid-thinking-signature" {
		t.Fatalf("thinking signature type = %s", got)
	}
}

func TestIsOpaqueUpstreamError(t *testing.T) {
	SetOpaqueErrorSampleSettings(OpaqueErrorSampleSettings{Enabled: true, IncludeThinkingSignature400: true})
	defer SetOpaqueErrorSampleSettings(OpaqueErrorSampleSettings{Enabled: true, IncludeThinkingSignature400: true})
	if !IsOpaqueUpstreamError(500, `{"error":{"code":500,"message":"Unknown Error.","status":"UNKNOWN"}}`) {
		t.Fatal("expected opaque match")
	}
	if IsOpaqueUpstreamError(502, `upstream returned an empty response`) {
		t.Fatal("empty response should not match opaque")
	}
	sig := `Invalid signature in thinking block`
	if !IsOpaqueUpstreamError(400, sig) {
		t.Fatal("expected thinking signature 400 match")
	}
	SetOpaqueErrorSampleSettings(OpaqueErrorSampleSettings{Enabled: true, IncludeThinkingSignature400: false, Matchers: []string{"unknown error"}})
	if IsOpaqueUpstreamError(400, sig) {
		t.Fatal("thinking signature should be disabled")
	}
}

func TestOpaqueErrorSampleAdmitPerSessionAndDaily(t *testing.T) {
	ResetOpaqueErrorSampleLimiterForTest()
	SetOpaqueErrorSampleSettings(OpaqueErrorSampleSettings{Enabled: true, PerSession: 1, DailyCap: 2, Matchers: []string{"unknown error"}})
	defer func() {
		ResetOpaqueErrorSampleLimiterForTest()
		SetOpaqueErrorSampleSettings(OpaqueErrorSampleSettings{Enabled: true, IncludeThinkingSignature400: true})
	}()
	now := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	if !OpaqueErrorSampleAdmit("500:unknown-error", "sess-a", now) {
		t.Fatal("first sess-a should admit")
	}
	if OpaqueErrorSampleAdmit("500:unknown-error", "sess-a", now) {
		t.Fatal("second sess-a should deny")
	}
	if !OpaqueErrorSampleAdmit("500:unknown-error", "sess-b", now) {
		t.Fatal("sess-b should admit")
	}
	if OpaqueErrorSampleAdmit("500:unknown-error", "sess-c", now) {
		t.Fatal("daily cap should deny sess-c")
	}
}

func TestSummarizeRequestForErrorSample(t *testing.T) {
	body := []byte(`{
	  "model":"claude-opus-5-5-high",
	  "session_id":"e586867e-0280-89f6-99c1",
	  "messages":[
	    {"role":"user","content":"hi"},
	    {"role":"assistant","content":[
	      {"type":"thinking","thinking":"secret","signature":"sig-abc-123456"},
	      {"type":"text","text":"ok"},
	      {"type":"tool_use","id":"1","name":"list_files"}
	    ]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"a"}]}
	  ],
	  "tools":[{"name":"list_files"}]
	}`)
	sum := SummarizeRequestForErrorSample(body)
	if sum.MessageCount != 3 || sum.Model != "claude-opus-5-5-high" {
		t.Fatalf("summary meta = %+v", sum)
	}
	if sum.ThinkingBlocks != 1 || len(sum.ThinkingSignatures) != 1 {
		t.Fatalf("thinking = %+v", sum)
	}
	if sum.ThinkingSignatures[0].Length == 0 || sum.ThinkingSignatures[0].Hash8 == "" {
		t.Fatalf("sig meta missing: %+v", sum.ThinkingSignatures[0])
	}
	if strings.Contains(sum.ThinkingSignatures[0].Hash8, "sig-abc") {
		t.Fatal("hash leaked signature text")
	}
	if len(sum.ToolCallNames) != 1 || sum.ToolCallNames[0] != "list_files" {
		t.Fatalf("tools = %v", sum.ToolCallNames)
	}
	if !sum.HasTools || sum.ApproxInputTokens <= 0 {
		t.Fatalf("flags = %+v", sum)
	}
}

func TestExtractAuthIndexFromErrorTexts(t *testing.T) {
	got := ExtractAuthIndexFromErrorTexts(`upstream execution failed: auth_file=f2353ea9-5123-4353-820d-2c4771098bb9 err={}`)
	if got != "f2353ea9-5123-4353-820d-2c4771098bb9" {
		t.Fatalf("got %q", got)
	}
}
