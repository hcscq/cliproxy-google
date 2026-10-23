package auth

import (
	"errors"
	"testing"
)

func TestFinalFirstByteTimeoutErr(t *testing.T) {
	if !isFinalFirstByteTimeoutErr(errors.New(`{"error":{"code":504,"message":"4: no first byte from upstream within 4m0s","cpa_ttfb_final":true}}`)) {
		t.Fatal("expected final")
	}
	if isFinalFirstByteTimeoutErr(errors.New(`{"error":{"code":504,"message":"no first byte from upstream within 1m15s"}}`)) {
		t.Fatal("not final")
	}
}
