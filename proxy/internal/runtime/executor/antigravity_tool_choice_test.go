package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRestrictGeminiToolsToAllowed(t *testing.T) {
	in := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"get_weather"},{"name":"get_time"}]},{"googleSearch":{}}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_time"]}}}}`)
	out := restrictGeminiToolsToAllowed(in)
	names := gjson.GetBytes(out, "request.tools.0.functionDeclarations.#.name").Array()
	if len(names) != 1 || names[0].String() != "get_time" {
		t.Fatalf("got %s", out)
	}
	if !gjson.GetBytes(out, "request.tools.1.googleSearch").Exists() {
		t.Fatalf("other tool kinds must stay: %s", out)
	}
	if got := restrictGeminiToolsToAllowed([]byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"a"}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}}`)); gjson.GetBytes(got, "request.tools.0.functionDeclarations.#").Int() != 1 {
		t.Fatalf("no allow-list must be untouched: %s", got)
	}
	unknown := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"a"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["zzz"]}}}}`)
	if string(restrictGeminiToolsToAllowed(unknown)) != string(unknown) {
		t.Fatal("unmatched names must leave the request untouched")
	}
}
