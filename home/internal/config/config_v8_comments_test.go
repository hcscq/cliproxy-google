package config

import (
	"strings"
	"testing"
)

func TestV8MigrationKeepsProviderComments(t *testing.T) {
	raw := []byte("# DOCUMENT HEAD\noauth: # OAUTH INLINE\n  providers: # PROVIDERS INLINE\n    xai: # PROVIDER INLINE\n      # FIELD HEAD\n      inject-x-search: true # FIELD INLINE\n\n      # FIELD FOOT\n\n    # PROVIDER FOOT\nserver: {port: 8327}\n# DOCUMENT FOOT\n")
	for i := 0; i < 3; i++ {
		data, _, err := NormalizeConfigLayout(raw, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = ValidateV8Config(data); err != nil {
			t.Fatal(err)
		}
		for _, marker := range []string{"DOCUMENT HEAD", "OAUTH INLINE", "PROVIDERS INLINE", "PROVIDER INLINE", "FIELD HEAD", "FIELD INLINE", "FIELD FOOT", "PROVIDER FOOT", "DOCUMENT FOOT"} {
			if count := strings.Count(string(data), marker); count != 1 {
				t.Fatalf("migration changed comment %s (%d):\n%s", marker, count, data)
			}
		}
		raw = data
	}
}
