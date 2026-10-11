package redisx

import (
	"context"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Status is shown on the panel.
type Status struct {
	Enabled         bool             `json:"enabled"`
	Healthy         bool             `json:"healthy"`
	Mode            string           `json:"mode"` // "sentinel" | "standalone" | ""
	Master          string           `json:"master,omitempty"`
	Role            string           `json:"role,omitempty"`
	ConnectedSlaves string           `json:"connected_slaves,omitempty"`
	UsedMemory      string           `json:"used_memory,omitempty"`
	PingMS          float64          `json:"ping_ms"`
	Sentinels       []SentinelStatus `json:"sentinels,omitempty"`
	Error           string           `json:"error,omitempty"`
}

// SentinelStatus is one sentinel's view of the master.
type SentinelStatus struct {
	Addr   string `json:"addr"`
	Master string `json:"master,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Status reports Redis state (nil client: disabled).
func (c *Client) Status(ctx context.Context) Status {
	if c == nil {
		return Status{}
	}
	st := Status{Enabled: true, Healthy: c.Healthy(), Mode: "standalone", Master: c.cfg.Addr}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := c.bulk.Ping(cctx).Err(); err != nil {
		st.Error = err.Error()
	} else {
		st.PingMS = float64(time.Since(start).Microseconds()) / 1000
		if info, err := c.bulk.Info(cctx, "replication", "memory").Result(); err == nil {
			for _, line := range strings.Split(info, "\n") {
				k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
				if !ok {
					continue
				}
				switch k {
				case "role":
					st.Role = v
				case "connected_slaves":
					st.ConnectedSlaves = v
				case "used_memory_human":
					st.UsedMemory = v
				}
			}
		}
	}
	if len(c.cfg.SentinelAddrs) > 0 {
		st.Mode, st.Master = "sentinel", ""
		for _, addr := range c.cfg.SentinelAddrs {
			ss := SentinelStatus{Addr: addr}
			sc := redis.NewSentinelClient(&redis.Options{Addr: addr, Password: c.cfg.Password, DialTimeout: 500 * time.Millisecond, ReadTimeout: time.Second})
			if m, err := sc.GetMasterAddrByName(cctx, c.cfg.MasterName).Result(); err != nil {
				ss.Error = err.Error()
			} else if len(m) == 2 {
				ss.Master = m[0] + ":" + m[1]
				if st.Master == "" {
					st.Master = ss.Master
				}
			}
			_ = sc.Close()
			st.Sentinels = append(st.Sentinels, ss)
		}
	}
	return st
}
