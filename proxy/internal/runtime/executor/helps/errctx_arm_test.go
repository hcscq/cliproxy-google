package helps

import "testing"

// With ONLY the capture switch on (sampler off), a reporter that is armed and
// then sees a failed 429 must record into the error-context capture.
func TestCaptureRecordsWithoutSampler(t *testing.T) {
	t.Setenv("CPA_ERRCTX_CAPTURE", "1")
	t.Setenv("CPA_ERRCTX_TYPE", "429:quota")
	// deliberately do NOT set CPA_SYSPROMPT_SAMPLE
	ReloadErrorContextCaptureFromEnvForTest()
	defer DisableErrorContextCaptureForTest()

	if SysPromptSampleEnabled() {
		t.Fatal("sampler must be off for this test")
	}
	if !ErrorContextCaptureEnabled() {
		t.Fatal("capture must be on")
	}

	r := &UsageReporter{}
	r.ArmSysPromptSample("Global chat rules (higher priority than character instructions): do X", 0)
	r.recordSysSample(429, true, 0, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`)

	snap := ErrorContextCaptureSnapshot()
	if snap["captured"].(int) != 1 {
		t.Fatalf("captured=%v, want 1 (capture must fire without sampler)", snap["captured"])
	}
}

// When neither switch is on, an armed reporter must not panic and must not
// capture anything.
func TestNoCaptureWhenBothOff(t *testing.T) {
	DisableErrorContextCaptureForTest()
	r := &UsageReporter{}
	r.ArmSysPromptSample("Global chat rules: y", 0)
	r.recordSysSample(429, true, 0, "RESOURCE_EXHAUSTED")
	if ErrorContextCaptureSnapshot()["enabled"].(bool) {
		t.Fatal("must stay disabled")
	}
}
