package cluster

import (
	"context"
	"hash/fnv"
	"net"
	"sort"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// HomeAssignmentRecord pins one proxy (by certificate node ID) to the Home it
// should connect to. The Home leader keeps the table balanced; proxies receive
// their row with CLUSTER NODES and follow it, falling back to rendezvous
// hashing when no row exists.
type HomeAssignmentRecord struct {
	NodeID    string    `gorm:"column:node_id;primaryKey;size:256"`
	HomeIP    string    `gorm:"column:home_ip;not null"`
	HomePort  int       `gorm:"column:home_port;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

// TableName returns the database table name.
func (HomeAssignmentRecord) TableName() string { return "home_assignment" }

// homeAssignmentAdvisoryLockKey serializes rebalances, so two Homes that both
// briefly believe they are leader cannot interleave writes.
const homeAssignmentAdvisoryLockKey int64 = 0x484f4d4541535347 // "HOMEASSG"

// homeAssignmentGrace keeps the row of a proxy that is briefly away (for
// example reconnecting to its assigned Home), so it returns to the same Home
// instead of being re-placed and bounced. Absent proxies do not count toward
// balance.
const homeAssignmentGrace = 10 * time.Minute

func ensureHomeAssignmentTable(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return nil
	}
	tx := db.WithContext(contextOrBackground(ctx))
	if tx.Migrator().HasTable(&HomeAssignmentRecord{}) {
		return nil
	}
	return tx.Migrator().CreateTable(&HomeAssignmentRecord{})
}

func homeKey(ip string, port int) string { return net.JoinHostPort(ip, strconv.Itoa(port)) }

// assignmentScore is the rendezvous weight of (proxy, home); it breaks ties so
// balancing is deterministic and stays close to the rendezvous fallback.
func assignmentScore(proxy, home string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(proxy))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(home))
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// balanceAssignments returns proxy -> home with per-home counts differing by at
// most one, moving as few proxies as possible away from current. Proxies on a
// dead home, new proxies and the surplus of over-full homes are (re)placed.
func balanceAssignments(current map[string]string, proxies, homes []string) map[string]string {
	out := make(map[string]string, len(proxies))
	if len(homes) == 0 || len(proxies) == 0 {
		return out
	}
	proxies = append([]string(nil), proxies...)
	homes = append([]string(nil), homes...)
	sort.Strings(proxies)
	sort.Strings(homes)
	live := make(map[string]bool, len(homes))
	for _, h := range homes {
		live[h] = true
	}
	load := make(map[string][]string, len(homes))
	var pending []string
	for _, p := range proxies {
		if h, ok := current[p]; ok && live[h] {
			load[h] = append(load[h], p)
		} else {
			pending = append(pending, p)
		}
	}
	capacity := (len(proxies) + len(homes) - 1) / len(homes)
	// Over-full homes give up the proxies that like them least.
	byPreference := func(h string) {
		list := load[h]
		sort.Slice(list, func(i, j int) bool {
			si, sj := assignmentScore(list[i], h), assignmentScore(list[j], h)
			if si != sj {
				return si > sj
			}
			return list[i] < list[j]
		})
	}
	for _, h := range homes {
		byPreference(h)
		for len(load[h]) > capacity {
			last := load[h][len(load[h])-1]
			load[h] = load[h][:len(load[h])-1]
			pending = append(pending, last)
		}
	}
	sort.Strings(pending)
	place := func(p string) {
		best := ""
		for _, h := range homes {
			if best == "" || len(load[h]) < len(load[best]) ||
				(len(load[h]) == len(load[best]) && assignmentScore(p, h) > assignmentScore(p, best)) {
				best = h
			}
		}
		load[best] = append(load[best], p)
	}
	for _, p := range pending {
		place(p)
	}
	// Even out what capping alone cannot (e.g. 2,2,0 -> 2,1,1).
	for {
		minH, maxH := homes[0], homes[0]
		for _, h := range homes {
			if len(load[h]) < len(load[minH]) {
				minH = h
			}
			if len(load[h]) > len(load[maxH]) {
				maxH = h
			}
		}
		if len(load[maxH])-len(load[minH]) <= 1 {
			break
		}
		byPreference(maxH)
		last := load[maxH][len(load[maxH])-1]
		load[maxH] = load[maxH][:len(load[maxH])-1]
		load[minH] = append(load[minH], last)
	}
	for h, list := range load {
		for _, p := range list {
			out[p] = h
		}
	}
	return out
}

// RebalanceHomeAssignments recomputes the proxy -> Home table from live Homes
// and active proxies. It changes only rows that must move. Returns the number
// of rows written or removed.
func (r *Repository) RebalanceHomeAssignments(ctx context.Context, homeTimeout time.Duration) (int, error) {
	db, errDB := r.database()
	if errDB != nil {
		return 0, errDB
	}
	if homeTimeout <= 0 {
		homeTimeout = defaultHeartbeatTimeout
	}
	changed := 0
	errTx := db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", homeAssignmentAdvisoryLockKey).Error; err != nil {
				return err
			}
		}
		now, errNow := DatabaseNow(ctx, tx)
		if errNow != nil {
			return errNow
		}
		var homeRows []ClusterNodeRecord
		if err := tx.Where("last_seen_at >= ?", now.Add(-homeTimeout)).Find(&homeRows).Error; err != nil {
			return err
		}
		draining, errDrain := drainingHomes(tx, now)
		if errDrain != nil {
			return errDrain
		}
		homes := make([]string, 0, len(homeRows))
		homeAddr := make(map[string]ClusterNodeRecord, len(homeRows))
		for _, h := range homeRows {
			if h.IP == "" || h.Port <= 0 {
				continue
			}
			k := homeKey(h.IP, h.Port)
			if draining[k] {
				continue // shutting down: move its proxies away
			}
			homes = append(homes, k)
			homeAddr[k] = h
		}
		var members []CPANodeMembershipRecord
		if err := tx.Where("last_seen_at >= ?", now.Add(-homeAssignmentGrace)).Find(&members).Error; err != nil {
			return err
		}
		seen := map[string]bool{}
		recent := map[string]bool{} // seen within the grace, any state
		proxies := make([]string, 0, len(members))
		for _, m := range members {
			if m.NodeID == "" {
				continue
			}
			recent[m.NodeID] = true
			timeout := m.CPAHeartbeatTimeout
			if timeout <= 0 {
				timeout = homeTimeout
			}
			if m.State != MembershipStateActive || seen[m.NodeID] || m.LastSeenAt.Before(now.Add(-2*timeout)) {
				continue
			}
			seen[m.NodeID] = true
			proxies = append(proxies, m.NodeID)
		}
		var rows []HomeAssignmentRecord
		if err := tx.Find(&rows).Error; err != nil {
			return err
		}
		current := make(map[string]string, len(rows))
		for _, row := range rows {
			current[row.NodeID] = homeKey(row.HomeIP, row.HomePort)
		}
		if len(homes) == 0 {
			return nil // keep the last assignment; proxies fall back on failures
		}
		next := balanceAssignments(current, proxies, homes)
		for node := range current {
			if _, keep := next[node]; !keep && !recent[node] {
				if err := tx.Where("node_id = ?", node).Delete(&HomeAssignmentRecord{}).Error; err != nil {
					return err
				}
				changed++
			}
		}
		for node, home := range next {
			if current[node] == home {
				continue
			}
			h := homeAddr[home]
			rec := HomeAssignmentRecord{NodeID: node, HomeIP: h.IP, HomePort: h.Port, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "node_id"}}, UpdateAll: true}).Create(&rec).Error; err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	return changed, errTx
}

// HomeAssignmentFor returns the Home a proxy is assigned to, if any.
func (r *Repository) HomeAssignmentFor(ctx context.Context, nodeID string) (string, int, bool, error) {
	if nodeID == "" {
		return "", 0, false, nil
	}
	db, errDB := r.database()
	if errDB != nil {
		return "", 0, false, errDB
	}
	var rows []HomeAssignmentRecord
	if err := db.WithContext(contextOrBackground(ctx)).Where("node_id = ?", nodeID).Limit(1).Find(&rows).Error; err != nil {
		return "", 0, false, err
	}
	if len(rows) == 0 {
		return "", 0, false, nil
	}
	return rows[0].HomeIP, rows[0].HomePort, true, nil
}

// ListHomeAssignments returns the whole table (panel Nodes page).
func (r *Repository) ListHomeAssignments(ctx context.Context) ([]HomeAssignmentRecord, error) {
	db, errDB := r.database()
	if errDB != nil {
		return nil, errDB
	}
	var rows []HomeAssignmentRecord
	err := db.WithContext(contextOrBackground(ctx)).Order("node_id").Find(&rows).Error
	return rows, err
}

func (c *Coordinator) runHomeAssignmentLoop(ctx context.Context) {
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !c.IsMaster() {
			continue
		}
		n, err := c.repo.RebalanceHomeAssignments(ctx, c.heartbeatTimeout)
		if err != nil {
			if ctx.Err() == nil {
				log.WithError(err).Warn("home assignment rebalance failed")
			}
			continue
		}
		if n > 0 {
			log.WithField("changed", n).Info("home assignment rebalanced")
		}
	}
}
