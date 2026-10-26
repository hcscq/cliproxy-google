package management

import "testing"

func TestParseBoolFlag(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, " TRUE ": true, "": false, "0": false, "false": false, "yes": false} {
		if got := parseBoolFlag(v); got != want {
			t.Fatalf("parseBoolFlag(%q)=%v want %v", v, got, want)
		}
	}
}
