package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Check every lifecycle identity, not merely the final snapshot or event counts.
func assertApplyPatchIdentityLifecycle(t *testing.T, events [][]byte, id, call, input string, index int, fragments []string) {
	t.Helper()
	counts := map[string]int{}
	var actual []string
	for _, event := range events {
		root := gjson.ParseBytes(event)
		kind := root.Get("type").String()
		identity, idKey := root, "item_id"
		switch kind {
		case "response.output_item.added", "response.output_item.done":
			if root.Get("item.type").String() != "custom_tool_call" {
				continue
			}
			identity, idKey = root.Get("item"), "id"
			if root.Get("output_index").Int() != int64(index) {
				t.Fatalf("wrong output index: %s", event)
			}
			if kind == "response.output_item.added" && identity.Get("input").String() != "" {
				t.Fatalf("nonempty added input: %s", event)
			}
			if kind == "response.output_item.done" && identity.Get("input").String() != input {
				t.Fatalf("item input: %s", event)
			}
		case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
			if root.Get("output_index").Int() != int64(index) {
				t.Fatalf("wrong input index: %s", event)
			}
			if kind == "response.custom_tool_call_input.delta" {
				actual = append(actual, root.Get("delta").String())
			}
			if kind == "response.custom_tool_call_input.done" && root.Get("input").String() != input {
				t.Fatalf("done input: %s", event)
			}
		case "response.completed":
			identity, idKey = root.Get("response.output.0"), "id"
			if identity.Get("input").String() != input {
				t.Fatalf("final input: %s", event)
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.failed":
			t.Fatalf("unsafe patch event: %s", event)
		default:
			continue
		}
		counts[kind]++
		if identity.Get(idKey).String() != id || identity.Get("call_id").String() != call {
			t.Fatalf("unstable identity: %s", event)
		}
	}
	for _, kind := range []string{"response.output_item.added", "response.custom_tool_call_input.done", "response.output_item.done", "response.completed"} {
		if counts[kind] != 1 {
			t.Fatalf("missing/duplicate lifecycle: %v", counts)
		}
	}
	if strings.Join(actual, "|") != strings.Join(fragments, "|") || len(actual) != len(fragments) {
		t.Fatalf("source fragment replay: got %q want %q", actual, fragments)
	}
}

func applyPatchIdentitySource(interactions bool, first, boundary string, snapshot bool) [][]byte {
	id, call := "", ""
	if first == "item" {
		id = "a"
	}
	if first == "call" {
		call = "c"
	}
	args := `{"input":"pq"}`
	var events [][]byte
	if interactions {
		events = append(events, []byte(fmt.Sprintf(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":%q,"call_id":%q,"name":"apply_patch"}}`, id, call)))
		if !snapshot {
			for _, fragment := range []string{`{"input":"p`, `q"}`} {
				events = append(events, []byte(fmt.Sprintf(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":%q}}`, fragment)))
			}
		}
		if strings.HasPrefix(first, "neither-") {
			key, value := "id", "a"
			if first == "neither-call-first" {
				key, value = "call_id", "c"
			}
			events = append(events, []byte(fmt.Sprintf(`{"event_type":"step.start","index":0,"step":{"type":"function_call","name":"apply_patch",%q:%q}}`, key, value)))
		}
		if boundary == "delta" {
			events = append(events, []byte(`{"event_type":"step.delta","index":0,"step":{"id":"a","call_id":"c"},"delta":{"type":"arguments_delta","arguments":""}}`))
		}
		step := fmt.Sprintf(`{"index":0,"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":%s}`, args)
		if boundary == "item" || boundary == "delta" {
			events = append(events, []byte(`{"event_type":"step.stop","index":0,"step":`+step+`}`))
		} else {
			events = append(events, []byte(`{"event_type":"step.stop","index":0}`))
		}
		events = append(events, []byte(`{"event_type":"interaction.completed","interaction":{"steps":[`+step+`]}}`))
	} else {
		events = append(events, []byte(fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":%q,"call_id":%q,"name":"apply_patch","arguments":""}}`, id, call)))
		if !snapshot {
			for _, fragment := range []string{`{"input":"p`, `q"}`} {
				events = append(events, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%q}`, fragment)))
			}
		}
		if strings.HasPrefix(first, "neither-") {
			key, value := "item_id", "a"
			if first == "neither-call-first" {
				key, value = "call_id", "c"
			}
			events = append(events, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,%q:%q,"delta":""}`, key, value)))
		}
		if boundary == "delta" {
			events = append(events, []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"a","call_id":"c","delta":""}`))
		}
		events = append(events, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.done","output_index":0,"arguments":%q}`, args)))
		item := fmt.Sprintf(`{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":%q}`, args)
		if boundary == "item" || boundary == "delta" {
			events = append(events, []byte(`{"type":"response.output_item.done","output_index":0,"item":`+item+`}`))
		}
		events = append(events, []byte(`{"type":"response.completed","response":{"output":[`+item+`]}}`))
	}
	return events
}
