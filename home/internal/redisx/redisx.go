// Package redisx is the optional Redis layer shared by all Home replicas.
//
// Redis is never the source of truth: PostgreSQL keeps config, credentials and
// cooldown state. Redis carries hot, short-lived shared state (sticky-session
// bindings, RPM/rate-limit windows) and wake-up notifications. Every caller has
// a fallback, so when Redis is disabled, unreachable or failing over, Home
// keeps serving on PostgreSQL / in-memory paths.
package redisx

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

// ErrUnavailable means Redis is disabled or currently unhealthy; callers fall back.
var ErrUnavailable = errors.New("redis unavailable")

// Config is the `redis:` section of cluster.yaml.
type Config struct {
	Enabled       bool     `yaml:"enabled"`
	Addr          string   `yaml:"addr"`           // standalone host:port
	SentinelAddrs []string `yaml:"sentinel-addrs"` // Sentinel mode when set
	MasterName    string   `yaml:"master-name"`
	Password      string   `yaml:"password"`
	DB            int      `yaml:"db"`
	KeyPrefix     string   `yaml:"key-prefix"`
	// OpTimeout bounds every request-path call; a slow Redis must not slow dispatch.
	OpTimeout    time.Duration `yaml:"-"`
	RawOpTimeout string        `yaml:"op-timeout"`
}

// ApplyEnv lets k8s toggle Redis per Deployment (staged rollout) and keeps the
// password out of ConfigMaps.
func (c *Config) ApplyEnv() {
	if v := strings.TrimSpace(os.Getenv("HOME_REDIS_ENABLED")); v != "" {
		c.Enabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := strings.TrimSpace(os.Getenv("HOME_REDIS_ADDR")); v != "" {
		c.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv("HOME_REDIS_SENTINELS")); v != "" {
		c.SentinelAddrs = strings.Split(v, ",")
	}
	if v := strings.TrimSpace(os.Getenv("HOME_REDIS_MASTER")); v != "" {
		c.MasterName = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		c.Password = v
	}
	if d, err := time.ParseDuration(strings.TrimSpace(c.RawOpTimeout)); err == nil && d > 0 {
		c.OpTimeout = d
	}
	if c.OpTimeout <= 0 {
		c.OpTimeout = 100 * time.Millisecond
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "cpg:"
	}
	if c.MasterName == "" {
		c.MasterName = "cpg"
	}
}

// Client wraps a go-redis client with a health flag. While unhealthy, calls
// return ErrUnavailable immediately instead of waiting on timeouts.
type Client struct {
	rdb     redis.UniversalClient
	prefix  string
	timeout time.Duration
	healthy atomic.Bool
	fails   atomic.Int32
	stop    context.CancelFunc
}

// Open returns nil (Redis off) when disabled.
func Open(cfg Config) *Client {
	cfg.ApplyEnv()
	if !cfg.Enabled || (strings.TrimSpace(cfg.Addr) == "" && len(cfg.SentinelAddrs) == 0) {
		return nil
	}
	var rdb redis.UniversalClient
	if len(cfg.SentinelAddrs) > 0 {
		rdb = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:       cfg.MasterName,
			SentinelAddrs:    cfg.SentinelAddrs,
			Password:         cfg.Password,
			SentinelPassword: cfg.Password,
			DB:               cfg.DB,
			DialTimeout:      500 * time.Millisecond,
			ReadTimeout:      cfg.OpTimeout,
			WriteTimeout:     cfg.OpTimeout,
			PoolSize:         64,
			MinIdleConns:     4,
			MaxRetries:       0,
		})
	} else {
		rdb = redis.NewClient(&redis.Options{
			Addr:         cfg.Addr,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  500 * time.Millisecond,
			ReadTimeout:  cfg.OpTimeout,
			WriteTimeout: cfg.OpTimeout,
			PoolSize:     64,
			MinIdleConns: 4,
			MaxRetries:   0,
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{rdb: rdb, prefix: cfg.KeyPrefix, timeout: cfg.OpTimeout, stop: cancel}
	c.probe(ctx)
	go c.monitor(ctx)
	mode := "standalone " + cfg.Addr
	if len(cfg.SentinelAddrs) > 0 {
		mode = "sentinel " + strings.Join(cfg.SentinelAddrs, ",") + " master=" + cfg.MasterName
	}
	log.Infof("redis: enabled (%s), healthy=%v", mode, c.healthy.Load())
	return c
}

func (c *Client) monitor(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.probe(ctx)
		}
	}
}

func (c *Client) probe(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	// A replica answers PING too; a write proves we reached the primary.
	err := c.rdb.Set(pctx, c.prefix+"probe", time.Now().UnixMilli(), 10*time.Second).Err()
	was := c.healthy.Load()
	c.healthy.Store(err == nil)
	if err == nil {
		c.fails.Store(0)
	}
	if was != (err == nil) {
		if err == nil {
			log.Info("redis: healthy again; shared Redis paths resumed")
		} else {
			log.Warnf("redis: unhealthy (%v); falling back to PostgreSQL/in-memory", err)
		}
	}
}

// Healthy reports whether request paths should use Redis.
func (c *Client) Healthy() bool { return c != nil && c.healthy.Load() }

// Key returns a prefixed key.
func (c *Client) Key(parts ...string) string { return c.prefix + strings.Join(parts, ":") }

// Raw exposes the client for pub/sub.
func (c *Client) Raw() redis.UniversalClient { return c.rdb }

// Ctx bounds one request-path operation.
func (c *Client) Ctx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, c.timeout)
}

// Observe records an op result; three consecutive non-nil-reply errors mark
// Redis unhealthy until the next successful probe (≤1s).
func (c *Client) Observe(err error) {
	if c == nil {
		return
	}
	if err == nil || errors.Is(err, redis.Nil) {
		c.fails.Store(0)
		return
	}
	if c.fails.Add(1) >= 3 && c.healthy.Swap(false) {
		log.Warnf("redis: marking unhealthy after repeated errors (%v); falling back", err)
	}
}

// Close stops the monitor and closes connections.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.stop()
	return c.rdb.Close()
}

// Default is the process-wide client (nil when Redis is off).
var defaultClient atomic.Pointer[Client]

func SetDefault(c *Client) { defaultClient.Store(c) }
func Default() *Client     { return defaultClient.Load() }
