package executor

import (
	"testing"
)

func TestEnsureColonSpacedJSONLeavesInvalidPayloadUnchanged(t *testing.T) {
	input := []byte(`{"text":"unterminated}`)
	output := ensureColonSpacedJSON(input)
	if &output[0] != &input[0] || string(output) != string(input) {
		t.Fatal("invalid JSON payload changed")
	}
}

var benchmarkExecutorPayloadOutput []byte
