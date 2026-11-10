package helps

import (
	"context"
	"net/http"
	"testing"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestContentBlockUsageRecordStaysHTTP200(t *testing.T) {
	raw := []byte(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"The prompt is blocked due to safety"},"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":0,"totalTokenCount":11}}}`)
	block, ok := translatorcommon.ReadContentBlock(raw)
	if !ok {
		t.Fatalf("expected content block: %s", raw)
	}
	detail := ParseAntigravityUsage(raw)
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d", detail.InputTokens)
	}
	reporter := &UsageReporter{provider: "antigravity", model: "gemini-test"}
	record := reporter.buildRecord(detail, true, usage.Failure{
		StatusCode: http.StatusOK,
		Body:       block.UsageBody(),
		Reason:     block.UsageReason(),
	})
	if !record.Failed || record.Fail.StatusCode != http.StatusOK || record.Fail.Reason != "PROHIBITED_CONTENT" {
		t.Fatalf("record = failed:%v status:%d reason:%q body:%s", record.Failed, record.Fail.StatusCode, record.Fail.Reason, record.Fail.Body)
	}
	if gjson.Get(record.Fail.Body, "type").String() != "content_block" {
		t.Fatalf("body = %s", record.Fail.Body)
	}
	if record.Detail.TotalTokens != 11 {
		t.Fatalf("total tokens = %d", record.Detail.TotalTokens)
	}
}

func TestStreamUsageBufferKeepsSafetyBlockAcrossEmptyFinish(t *testing.T) {
	var buf StreamUsageBuffer
	line := []byte(`data: {"response":{"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":0,"totalTokenCount":4}}}`)
	detail, ok := ParseAntigravityStreamUsage(line)
	if !ok {
		t.Fatal("expected usage")
	}
	buf.Observe(detail, true)
	buf.ObserveContentBlock(line)
	if !buf.blocked || buf.block.FinishReason != "SAFETY" {
		t.Fatalf("buffer block = %+v blocked=%v", buf.block, buf.blocked)
	}
	buf.ObserveContentBlock([]byte(`{"response":{"candidates":[{"finishReason":""}]}}`))
	if buf.block.FinishReason != "SAFETY" || buf.block.UsageReason() != "SAFETY" {
		t.Fatalf("empty finish cleared the block: %+v", buf.block)
	}
	reporter := &UsageReporter{provider: "antigravity", model: "gemini-test"}
	if !buf.Publish(context.Background(), reporter) {
		t.Fatal("expected the block to publish")
	}
}

func TestPublishUsageIgnoresOrdinaryStop(t *testing.T) {
	raw := []byte(`{"response":{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
	if _, ok := translatorcommon.ReadContentBlock(raw); ok {
		t.Fatal("STOP was classified as a content block")
	}
}
