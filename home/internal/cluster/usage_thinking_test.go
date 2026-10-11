package cluster

import "testing"

func TestUsageRecordKeepsExactThinkingParams(t *testing.T) {
	payload := `{"timestamp":"2026-10-11T14:00:00Z","provider":"antigravity","model":"gemini-3.8-flash-tiered",
		"reasoning_effort":"medium","thinking_params":"budget=8192,include_thoughts=true",
		"thinking_note":"thinking cannot be fully disabled on this model; sent budget=0 (upstream minimum, still thinks briefly)",
		"tokens":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	rec, err := UsageRecordFromPayloadWithRuntime(payload, UsageRuntimeMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Effort != "medium" || rec.ThinkingParams != "budget=8192,include_thoughts=true" || rec.ThinkingNote == "" {
		t.Fatalf("got effort=%q params=%q note=%q", rec.Effort, rec.ThinkingParams, rec.ThinkingNote)
	}
}
