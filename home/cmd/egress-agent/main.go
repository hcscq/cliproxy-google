// Command egress-agent runs the per-node egress proxy (see internal/egressagent).
package main

import (
	"context"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/router-for-me/CLIProxyAPIHome/internal/egressagent"
	log "github.com/sirupsen/logrus"
)

func main() {
	listen := os.Getenv("EGRESS_LISTEN")
	if listen == "" {
		listen = os.Getenv("HOST_IP") + ":3141"
	}
	cfg := egressagent.Config{Node: os.Getenv("NODE_NAME"), Listen: listen, Exclude: map[string]bool{}, ProbeURL: os.Getenv("EGRESS_PROBE_URL"), ExcludeFile: os.Getenv("EGRESS_EXCLUDE_FILE")}
	for _, ip := range strings.Split(os.Getenv("EGRESS_EXCLUDE_IPS"), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			cfg.Exclude[ip] = true
		}
	}
	allow := os.Getenv("EGRESS_ALLOW_CIDRS")
	if allow == "" {
		allow = "10.42.0.0/16,172.16.0.0/16,127.0.0.0/8"
	}
	for _, c := range strings.Split(allow, ",") {
		if p, err := netip.ParsePrefix(strings.TrimSpace(c)); err == nil {
			cfg.AllowSources = append(cfg.AllowSources, p)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := egressagent.New(cfg).Run(ctx); err != nil {
		log.Fatal(err)
	}
}
