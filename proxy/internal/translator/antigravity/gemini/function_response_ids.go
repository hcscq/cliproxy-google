package gemini

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/sjson"
)

// fillGeminiFunctionResponseIDs pairs functionResponse parts that carry no id
// with the preceding functionCall parts (same name, in order) and copies the
// call id over. The public Gemini API accepts responses without ids (the
// google-genai SDK's Part.from_function_response sends none), but Antigravity
// validates the pairing and Claude upstreams require tool_use_id. When
// synthesizeCallIDs is set (Claude models), calls without an id get a
// deterministic synthetic one so both sides can be linked.
func fillGeminiFunctionResponseIDs(rawJSON []byte, synthesizeCallIDs bool) []byte {
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if !contents.IsArray() || !strings.Contains(contents.Raw, "functionResponse") {
		return rawJSON
	}
	type call struct{ id, name string }
	type edit struct{ path, value string }
	var edits []edit
	var pending []call
	for ci, content := range contents.Array() {
		var calls []call
		responses := 0
		for pi, part := range content.Get("parts").Array() {
			if fc := part.Get("functionCall"); fc.Exists() {
				id := fc.Get("id").String()
				if id == "" && synthesizeCallIDs {
					id = fmt.Sprintf("toolu_gemini_%d_%d", ci, pi)
					edits = append(edits, edit{fmt.Sprintf("request.contents.%d.parts.%d.functionCall.id", ci, pi), id})
				}
				calls = append(calls, call{id: id, name: fc.Get("name").String()})
			}
			if part.Get("functionResponse").Exists() {
				responses++
			}
		}
		if len(calls) > 0 {
			pending = calls
			continue
		}
		if responses == 0 || len(pending) == 0 {
			continue
		}
		used := make([]bool, len(pending))
		for pi, part := range content.Get("parts").Array() {
			fr := part.Get("functionResponse")
			if !fr.Exists() || fr.Get("id").String() != "" {
				continue
			}
			name := fr.Get("name").String()
			match := -1
			for k, c := range pending {
				if !used[k] && c.id != "" && (name == "" || c.name == name) {
					match = k
					break
				}
			}
			if match < 0 {
				continue
			}
			used[match] = true
			edits = append(edits, edit{fmt.Sprintf("request.contents.%d.parts.%d.functionResponse.id", ci, pi), pending[match].id})
		}
		pending = nil
	}
	// Apply after the scan: contents was parsed without copying rawJSON.
	for _, e := range edits {
		rawJSON, _ = sjson.SetBytes(rawJSON, e.path, e.value)
	}
	return rawJSON
}
