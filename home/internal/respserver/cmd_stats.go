package respserver

import (
	"expvar"
	"strings"
)

// respCommands counts RESP commands by name and first-key prefix (capacity
// profiling; read via /debug/vars on the opt-in pprof listener).
var respCommands = expvar.NewMap("resp_commands")

func countRESPCommand(cmd string, args []string) {
	key := cmd
	if len(args) > 1 {
		k := args[1]
		if i := strings.IndexAny(k, ":/{"); i > 0 {
			k = k[:i]
		}
		if len(k) > 32 {
			k = k[:32]
		}
		key += " " + k
	}
	respCommands.Add(key, 1)
}
