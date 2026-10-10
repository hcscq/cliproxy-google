package executor

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var issue6478Calls = []struct {
	id   string
	name string
}{
	{id: "Write:1#e1f0da81143649f3a378d24af6fc3ec9", name: "Write"},
	{id: "PowerShell:66#cd50350578c34275a26a09a477a81ffb", name: "PowerShell"},
	// A lossy replacement would collide with the first call's already legal ID.
	{id: "Write_1_e1f0da81143649f3a378d24af6fc3ec9", name: "Read"},
	{id: "call_8jAZYBK5veMdr1DD2DqObARK", name: "Bash"},
}

func issue6478History(ids []string) []byte {
	var uses, results, tools []string
	for i, call := range issue6478Calls {
		uses = append(uses, fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":{"literal_id":%q}}`, ids[i], call.name, call.id))
		results = append(results, fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"content":%q}`, ids[i], "result for "+call.id))
		tools = append(tools, fmt.Sprintf(`{"name":%q,"input_schema":{"type":"object","properties":{"literal_id":{"type":"string"}}}}`, call.name))
	}
	return []byte(fmt.Sprintf(`{"model":"claude-opus-5","max_tokens":64,"tools":[%s],"messages":[{"role":"user","content":"run tools"},{"role":"assistant","content":[%s]},{"role":"user","content":[%s]}]}`, strings.Join(tools, ","), strings.Join(uses, ","), strings.Join(results, ",")))
}

func issue6478AssertPortableIDs(t *testing.T, ids []string) {
	t.Helper()
	if len(ids) != len(issue6478Calls) {
		t.Fatalf("tool IDs = %q, want %d calls", ids, len(issue6478Calls))
	}
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	seen := make(map[string]bool)
	for i, id := range ids {
		if !valid.MatchString(id) {
			t.Errorf("tool_use.id[%d] = %q violates Anthropic pattern ^[a-zA-Z0-9_-]+$", i, id)
		}
		if seen[id] {
			t.Errorf("distinct upstream calls collided at tool_use.id[%d] = %q", i, id)
		}
		seen[id] = true
		if i >= 2 && id != issue6478Calls[i].id {
			t.Errorf("already legal tool_use.id[%d] = %q, want unchanged %q", i, id, issue6478Calls[i].id)
		}
	}
}
