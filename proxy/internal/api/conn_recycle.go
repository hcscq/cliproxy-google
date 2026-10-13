package api

import (
	"context"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// Connection recycling spreads keep-alive clients (e.g. new-api through a
// ClusterIP Service, which balances per TCP connection) across proxy replicas:
// after a connection served CLIPROXY_CONN_MAX_REQUESTS requests or is older
// than CLIPROXY_CONN_MAX_AGE, the next response carries "Connection: close" so
// the client reconnects and kube-proxy picks a replica again. 0 disables.

type connRecycleKey struct{}

type connRecycleState struct {
	start time.Time
	n     atomic.Int64
}

type connRecycleConfig struct {
	maxRequests int64
	maxAge      time.Duration
}

func loadConnRecycleConfig() connRecycleConfig {
	cfg := connRecycleConfig{maxRequests: 200, maxAge: 2 * time.Minute}
	if v, err := strconv.ParseInt(os.Getenv("CLIPROXY_CONN_MAX_REQUESTS"), 10, 64); err == nil && v >= 0 {
		cfg.maxRequests = v
	}
	if v, err := time.ParseDuration(os.Getenv("CLIPROXY_CONN_MAX_AGE")); err == nil && v >= 0 {
		cfg.maxAge = v
	}
	return cfg
}

func connRecycleContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connRecycleKey{}, &connRecycleState{start: time.Now()})
}

func withConnRecycle(next http.Handler, cfg connRecycleConfig) http.Handler {
	if cfg.maxRequests == 0 && cfg.maxAge == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st, ok := r.Context().Value(connRecycleKey{}).(*connRecycleState); ok && r.ProtoMajor == 1 {
			n := st.n.Add(1)
			if (cfg.maxRequests > 0 && n >= cfg.maxRequests) || (cfg.maxAge > 0 && time.Since(st.start) >= cfg.maxAge) {
				w.Header().Set("Connection", "close")
			}
		}
		next.ServeHTTP(w, r)
	})
}
