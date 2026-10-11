package main

import (
	"net/http"
	_ "net/http/pprof" // registered only on the opt-in listener below
	"os"
	"runtime"
	"strings"

	log "github.com/sirupsen/logrus"
)

// HOME_PPROF_ADDR (e.g. 127.0.0.1:6060) enables a pprof listener with mutex
// and block profiling, for capacity testing. Off by default.
func init() {
	addr := strings.TrimSpace(os.Getenv("HOME_PPROF_ADDR"))
	if addr == "" {
		return
	}
	runtime.SetMutexProfileFraction(20)
	runtime.SetBlockProfileRate(100000)
	go func() {
		log.Infof("pprof listening on %s", addr)
		if err := http.ListenAndServe(addr, http.DefaultServeMux); err != nil {
			log.WithError(err).Warn("pprof listener stopped")
		}
	}()
}
