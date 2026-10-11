// Command scopebench measures API-key dispatch scope lookups (database path vs
// in-memory scope cache) on synthetic data. Local test databases only — it
// writes synthetic credentials, groups and keys.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

func main() {
	backend := flag.String("backend", "sqlite", "sqlite|postgres")
	dsn := flag.String("dsn", "", "sqlite path or postgres URI")
	creds := flag.Int("credentials", 500, "")
	models := flag.Int("models", 200, "")
	keys := flag.Int("keys", 1000, "")
	cgroups := flag.Int("credential-groups", 50, "")
	mgroups := flag.Int("model-groups", 50, "")
	dbN := flag.Int("db-iterations", 3000, "")
	cacheN := flag.Int("cache-iterations", 200000, "")
	seedOnly := flag.Bool("seed-only", false, "only seed, no benchmark")
	flag.Parse()
	log.SetLevel(log.WarnLevel)
	ctx := context.Background()
	var db *gorm.DB
	var err error
	if *backend == "postgres" {
		db, err = cluster.Open(ctx, cluster.PGSQLConfig{PostgresURI: *dsn})
	} else {
		if *dsn == "" {
			*dsn = filepath.Join(os.TempDir(), fmt.Sprintf("scopebench-%d.db", time.Now().UnixNano()))
		}
		db, err = cluster.OpenSQLite(ctx, *dsn)
	}
	if err != nil {
		panic(err)
	}
	if err := cluster.AutoMigrate(db); err != nil {
		panic(err)
	}
	repo := cluster.NewRepository(db)
	t0 := time.Now()
	res, err := repo.SeedSynthetic(ctx, cluster.SyntheticScale{Credentials: *creds, Models: *models, Keys: *keys, ChannelGroups: *cgroups, ModelGroups: *mgroups, Seed: 42})
	if err != nil {
		panic(err)
	}
	fmt.Printf("backend=%s seeded %d credentials, %d models, %d keys, %d credential groups, %d model groups in %v\n",
		*backend, *creds, *models, *keys, *cgroups, *mgroups, time.Since(t0).Round(time.Millisecond))
	if *seedOnly {
		return
	}
	var fps []time.Duration
	for i := 0; i < 20; i++ {
		s := time.Now()
		if err := repo.PollScopeOnce(ctx); err != nil {
			panic(err)
		}
		fps = append(fps, time.Since(s))
	}
	rng := rand.New(rand.NewSource(1))
	pick := func() (string, string) {
		return res.APIKeys[rng.Intn(len(res.APIKeys))], res.Models[rng.Intn(len(res.Models))]
	}
	// database path (pre-change behaviour)
	var dbd []time.Duration
	for i := 0; i < *dbN; i++ {
		k, m := pick()
		s := time.Now()
		if _, _, err := repo.AllowedDispatchIDsForAPIKeyModel(ctx, k, m); err != nil {
			panic(err)
		}
		dbd = append(dbd, time.Since(s))
	}
	// snapshot build
	var builds []time.Duration
	for i := 0; i < 10; i++ {
		cluster.InvalidateScopeCaches()
		s := time.Now()
		if _, err := repo.Scope(ctx); err != nil {
			panic(err)
		}
		builds = append(builds, time.Since(s))
	}
	// cached path
	var cd []time.Duration
	for i := 0; i < *cacheN; i++ {
		k, m := pick()
		s := time.Now()
		if _, _, err := repo.CachedAllowedDispatchIDsForAPIKeyModel(ctx, k, m); err != nil {
			panic(err)
		}
		cd = append(cd, time.Since(s))
	}
	snap, _ := repo.Scope(ctx)
	fmt.Printf("dispatch scope lookup, database path (%d): p50=%v p99=%v max=%v\n", *dbN, pct(dbd, .5), pct(dbd, .99), pct(dbd, 1))
	fmt.Printf("dispatch scope lookup, cached      (%d): p50=%v p99=%v p99.9=%v max=%v\n", *cacheN, pct(cd, .5), pct(cd, .99), pct(cd, .999), pct(cd, 1))
	fmt.Printf("background change poll (fingerprint, no change): p50=%v max=%v\n", pct(fps, .5), pct(fps, 1))
	fmt.Printf("scope snapshot rebuild (%d keys, rules=%d): p50=%v max=%v\n", snap.KeyCount, len(snap.Rules), pct(builds, .5), pct(builds, 1))
}
