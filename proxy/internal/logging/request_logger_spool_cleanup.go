package logging

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// processStart marks when this process started. Body-spool directories older than this
// were left behind by a previous container (e.g. after an OOM kill) and are never cleaned
// by their owner; on an emptyDir they survive restarts and can get the pod evicted.
var processStart = time.Now()

// purgeStaleBodySpools removes request-log-parts-* directories created before this process started.
func purgeStaleBodySpools(logsDir string) {
	logsDir = strings.TrimSpace(logsDir)
	if logsDir == "" {
		return
	}
	entries, errRead := os.ReadDir(logsDir)
	if errRead != nil {
		return
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "request-log-parts-") {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil || !info.ModTime().Before(processStart) {
			continue
		}
		if errRemove := os.RemoveAll(filepath.Join(logsDir, entry.Name())); errRemove == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Infof("request logger: removed %d stale body spool directories left by a previous process", removed)
	}
}
