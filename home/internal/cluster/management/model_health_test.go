package management

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
)

func TestAggregateProbe(t *testing.T) {
	got := aggregateProbe([]home.ProbeResult{{Outcome: home.ProbeQuota, HTTPStatus: 429}, {Outcome: home.ProbeOK, HTTPStatus: 200}})
	if got.Outcome != home.ProbeOK {
		t.Fatalf("any OK must win: %+v", got)
	}
	got = aggregateProbe([]home.ProbeResult{{Outcome: home.ProbeCapacity}, {Outcome: home.ProbeRejected, HTTPStatus: 400}})
	if got.Outcome != home.ProbeRejected {
		t.Fatalf("rejected outranks capacity: %+v", got)
	}
}

func TestModelCapabilityNotesAndExcluded(t *testing.T) {
	notes := modelCapabilityNotes(modelHealthEntry{ID: "gemini-3.6-flash-high", TagTitle: "Leaving Soon", TagDescription: "Use X"})
	if len(notes) != 2 || !strings.Contains(notes[0], "流式思考") || !strings.Contains(notes[1], "Use X") {
		t.Fatalf("notes: %v", notes)
	}
	if len(modelCapabilityNotes(modelHealthEntry{ID: "gemini-3.1-flash-image"})) != 0 {
		t.Fatal("image model has no streaming-thinking note")
	}
	if !matchesExcluded([]string{"tab_*"}, "tab_flash_lite_preview") || matchesExcluded([]string{"tab_*"}, "gemini-3-flash") {
		t.Fatal("wildcard exclusion")
	}
	m := switchTargetPattern.FindStringSubmatch("Gemini 3.5 Flash is no longer available. Please switch to Gemini 3.7 Flash in the model picker.")
	if len(m) != 2 || m[1] != "Gemini 3.7 Flash" {
		t.Fatalf("switch target: %v", m)
	}
}
