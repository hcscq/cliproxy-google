package management

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

// Sort keys for GET /credentials/search?sort=&order=. Default: newest created first.
const (
	sortCreated  = "created"
	sortName     = "name"
	sortStatus   = "status"
	sortQuota    = "quota"
	sortLastUsed = "last_used"
	sortEgress   = "egress"
	sortSuccess  = "success"
	sortSeq      = "seq"
	sortTier     = "tier"
)

const (
	credentialStatsTTL      = 30 * time.Second
	credentialLastUsedSince = 3 * 24 * time.Hour
)

func credentialSortParams(c *gin.Context) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(c.Query("sort")))
	switch key {
	case sortSeq, sortTier, sortName, sortStatus, sortQuota, sortLastUsed, sortEgress, sortCreated, sortSuccess:
	default:
		key = sortCreated
	}
	order := strings.ToLower(strings.TrimSpace(c.Query("order")))
	if order == "" {
		// newest / most recent / least quota first by default
		return key, key == sortCreated || key == sortLastUsed || key == sortSeq
	}
	return key, order == "desc"
}

var credStatsCache struct {
	sync.Mutex
	at    time.Time
	stats cluster.CredentialSortStats
}

// credentialSortStats is cached for credentialStatsTTL, so list requests never fan
// out to the usage table per request.
func (h *Handler) credentialSortStats(ctx context.Context, all []*coreauth.Auth) cluster.CredentialSortStats {
	credStatsCache.Lock()
	defer credStatsCache.Unlock()
	if time.Since(credStatsCache.at) < credentialStatsTTL && credStatsCache.stats.LastUsed != nil {
		return credStatsCache.stats
	}
	ids := make([]string, 0, len(all))
	for _, a := range all {
		ids = append(ids, a.ID)
	}
	stats, err := h.repo.LoadCredentialSortStats(ctx, ids, time.Now().Add(-credentialLastUsedSince))
	if err != nil {
		if credStatsCache.stats.LastUsed != nil {
			return credStatsCache.stats
		}
		return cluster.CredentialSortStats{LastUsed: map[string]time.Time{}, MinRemaining: map[string]float64{}}
	}
	credStatsCache.at, credStatsCache.stats = time.Now(), stats
	return stats
}

func credentialName(a *coreauth.Auth) string {
	if e := cluster.CredentialEmail(a); e != "" {
		return strings.ToLower(e)
	}
	return strings.ToLower(a.Label)
}

func credentialEgress(a *coreauth.Auth) string {
	if a.Metadata != nil {
		if ip, _ := a.Metadata["egress_ip"].(string); ip != "" && strings.TrimSpace(a.ProxyURL) != "" {
			return ip
		}
	}
	return strings.TrimSpace(a.ProxyURL)
}

// sortCredentials orders list in place (stable, ties by id). Missing values
// (no quota / never used / no egress) always sort last.
func sortCredentials(list []*coreauth.Auth, key string, desc bool, statusOf map[string]string, stats cluster.CredentialSortStats, calls map[string]*cluster.CredentialCallStats, seqs map[string]int64) {
	// cmp returns <0, 0, >0 for a vs b ascending; missing reports whether a value is absent.
	cmp := func(a, b *coreauth.Auth) (int, bool, bool) {
		switch key {
		case sortName:
			return strings.Compare(credentialName(a), credentialName(b)), false, false
		case sortStatus:
			return strings.Compare(statusOf[a.ID], statusOf[b.ID]), false, false
		case sortQuota:
			qa, okA := stats.MinRemaining[a.ID]
			qb, okB := stats.MinRemaining[b.ID]
			return cmpFloat(qa, qb), !okA, !okB
		case sortLastUsed:
			ta, okA := stats.LastUsed[a.ID]
			tb, okB := stats.LastUsed[b.ID]
			return ta.Compare(tb), !okA, !okB
		case sortSuccess:
			ra, okA := successRate(calls[a.ID])
			rb, okB := successRate(calls[b.ID])
			if c := cmpFloat(ra, rb); c != 0 || !okA || !okB {
				return c, !okA, !okB
			}
			return cmpFloat(float64(calls[a.ID].Calls), float64(calls[b.ID].Calls)), false, false
		case sortSeq:
			sa, sb := seqs[a.ID], seqs[b.ID]
			return cmpFloat(float64(sa), float64(sb)), sa == 0, sb == 0
		case sortTier:
			ta, tb := credentialTierRank(a), credentialTierRank(b)
			return ta - tb, ta == 0, tb == 0
		case sortEgress:
			ea, eb := credentialEgress(a), credentialEgress(b)
			return strings.Compare(ea, eb), ea == "", eb == ""
		default:
			return a.CreatedAt.Compare(b.CreatedAt), a.CreatedAt.IsZero(), b.CreatedAt.IsZero()
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		c, missI, missJ := cmp(list[i], list[j])
		if missI != missJ {
			return missJ
		}
		if c != 0 && !missI {
			if desc {
				return c > 0
			}
			return c < 0
		}
		return list[i].ID < list[j].ID
	})
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// credentialTierRank orders subscription tiers (0 = unknown, sorts last).
func credentialTierRank(a *coreauth.Auth) int {
	if a.Metadata == nil {
		return 0
	}
	t, _ := a.Metadata["subscription_tier"].(string)
	t = strings.ToLower(t)
	switch {
	case t == "":
		return 0
	case strings.Contains(t, "ultra"):
		return 4
	case strings.Contains(t, "pro"):
		return 3
	case t == "free-tier":
		return 1
	default:
		return 2
	}
}
