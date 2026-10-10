package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestFillsMissingFunctionResponseIDs(t *testing.T) {
	in := []byte(`{"contents":[
	 {"role":"user","parts":[{"text":"weather in Paris?"}]},
	 {"role":"model","parts":[{"thought":true,"text":""},{"functionCall":{"id":"call-1","name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"c2ln"}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"temp_c":21}}}]}]}`)
	out, err := ConvertGeminiRequestToAntigravity("gemini-3-flash", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String(); got != "call-1" {
		t.Fatalf("functionResponse.id = %q", got)
	}
}

func TestConvertGeminiRequestSynthesizesIDsForClaude(t *testing.T) {
	in := []byte(`{"contents":[
	 {"role":"user","parts":[{"text":"weather?"}]},
	 {"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Paris"}}}]},
	 {"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{"temp_c":21}}}]}]}`)
	out, err := ConvertGeminiRequestToAntigravity("claude-sonnet-4-6", in, false)
	if err != nil {
		t.Fatal(err)
	}
	callID := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.id").String()
	respID := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String()
	if callID == "" || callID != respID {
		t.Fatalf("call=%q response=%q", callID, respID)
	}
}
