package push

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPIHome/internal/respserver/dispatch"
)

func TestHandleUsageBatchSkipsInvalidPayloads(t *testing.T) {
	t.Chdir(t.TempDir())
	env := dispatch.Env{NodeID: "node-1", ClientIP: "10.0.0.5"}
	reply := handleUsage(context.Background(), env, []string{"LPUSH", "usage",
		`{"provider":"a","usage_id":"u1"}`, `not json`, `{"provider":"b","usage_id":"u2"}`})
	if reply.Kind != dispatch.ReplyKindInteger || reply.Integer != 3 {
		t.Fatalf("reply = %+v, want integer 3", reply)
	}
	data, err := os.ReadFile(filepath.Join("logs", "usage.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(data)), "\n") + 1; n != 2 {
		t.Fatalf("stored %d payloads, want 2: %s", n, data)
	}

	// A single invalid payload still gets an error reply (old behaviour).
	if r := handleUsage(context.Background(), env, []string{"LPUSH", "usage", "not json"}); r.Kind == dispatch.ReplyKindInteger {
		t.Fatalf("single invalid payload accepted: %+v", r)
	}
}
