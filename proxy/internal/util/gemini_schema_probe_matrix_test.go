package util

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravitySchema_KeepEmptyEnum_DropLoaderExpression(t *testing.T) {
	SetAntigravitySchemaSanitizeMode("on")
	in := `{"type":"object","properties":{"q":{"type":"string","enum":[],"loaderExpression":"x","x-foo":true,"const":"only"}}}`
	out := CleanJSONSchemaForAntigravity(in)
	if gjson.Get(out, "properties.q.loaderExpression").Exists() {
		t.Fatalf("loaderExpression should be dropped: %s", out)
	}
	if gjson.Get(out, "properties.q.x-foo").Exists() {
		t.Fatalf("x-foo should be dropped: %s", out)
	}
	// const should become enum (semantic rewrite) rather than vanish without replacement when possible
	if !gjson.Get(out, "properties.q.enum").Exists() && gjson.Get(out, "properties.q.const").Exists() {
		t.Fatalf("const should be converted or removed: %s", out)
	}
}

func TestAntigravitySchema_ObserveDoesNotModify(t *testing.T) {
	SetAntigravitySchemaSanitizeMode("observe")
	defer SetAntigravitySchemaSanitizeMode("on")
	in := `{"type":"object","properties":{"q":{"type":"string","loaderExpression":"x"}}}`
	out := CleanJSONSchemaForAntigravity(in)
	if out != in {
		t.Fatalf("observe must return original, got %s", out)
	}
}

func TestAntigravitySchema_OffDoesNotModify(t *testing.T) {
	SetAntigravitySchemaSanitizeMode("off")
	defer SetAntigravitySchemaSanitizeMode("on")
	in := `{"type":"object","properties":{"q":{"type":"string","loaderExpression":"x"}}}`
	out := CleanJSONSchemaForAntigravity(in)
	if !strings.Contains(out, "loaderExpression") {
		t.Fatalf("off must keep loaderExpression: %s", out)
	}
}
