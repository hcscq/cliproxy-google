package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type terminalCodexHomeDispatcher struct {
	auth  cliproxyauth.Auth
	calls atomic.Int32
}

func (*terminalCodexHomeDispatcher) HeartbeatOK() bool { return true }
func (d *terminalCodexHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(d.auth)
}
func (*terminalCodexHomeDispatcher) AbortAmbiguousDispatch() {}
