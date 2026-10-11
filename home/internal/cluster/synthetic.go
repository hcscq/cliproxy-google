package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

// SyntheticScale describes generated test data (benchmarks / local demo only;
// never run against production).
type SyntheticScale struct {
	Credentials   int
	Models        int
	Keys          int
	ChannelGroups int
	ModelGroups   int
	Seed          int64
}

// SyntheticResult lists generated identifiers.
type SyntheticResult struct {
	AuthIDs  []string
	Models   []string
	APIKeys  []string
	Channels []uint
	MGroups  []uint
}

var syntheticDomains = []string{"corp-a.example", "corp-b.example", "lab.example", "gai.example", "team-x.example"}
var syntheticTags = []string{"prod", "batch", "trial", "eu", "us", "vip", "backup"}

// SeedSynthetic writes synthetic credentials, groups, model groups and keys.
func (r *Repository) SeedSynthetic(ctx context.Context, sc SyntheticScale) (*SyntheticResult, error) {
	db, err := r.database()
	if err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(sc.Seed))
	res := &SyntheticResult{}
	providers := []string{"antigravity", "antigravity", "antigravity", "gemini", "vertex"}
	for i := 0; i < sc.Credentials; i++ {
		id := fmt.Sprintf("synthetic-%04d", i)
		domain := syntheticDomains[rng.Intn(len(syntheticDomains))]
		email := fmt.Sprintf("user%04d@%s", i, domain)
		var tags []any
		for _, t := range syntheticTags {
			if rng.Intn(4) == 0 {
				tags = append(tags, t)
			}
		}
		a := &coreauth.Auth{
			ID: id, Index: id, Provider: providers[rng.Intn(len(providers))], Label: email, Status: coreauth.StatusActive,
			Disabled: rng.Intn(20) == 0,
			Metadata: map[string]any{"email": email, "tags": tags, "priority": float64(rng.Intn(5)), "type": "synthetic",
				"access_token": "synthetic", "expired": "2099-01-01T00:00:00Z"},
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if _, err := r.UpsertAuth(ctx, a, "create"); err != nil {
			return nil, fmt.Errorf("seed auth %d: %w", i, err)
		}
		res.AuthIDs = append(res.AuthIDs, id)
	}
	for i := 0; i < sc.Models; i++ {
		res.Models = append(res.Models, fmt.Sprintf("synthetic-model-%03d", i))
	}
	tx := db.WithContext(contextOrBackground(ctx))
	for g := 0; g < sc.ChannelGroups; g++ {
		rec := ChannelGroupRecord{ChannelName: fmt.Sprintf("cg-%02d", g), Disabled: g%17 == 16}
		if err := tx.Create(&rec).Error; err != nil {
			return nil, err
		}
		res.Channels = append(res.Channels, rec.ID)
		n := 5 + rng.Intn(30)
		var rows []ChannelGroupDetailRecord
		for _, idx := range rng.Perm(len(res.AuthIDs))[:min(n, len(res.AuthIDs))] {
			rows = append(rows, ChannelGroupDetailRecord{ChannelGroupID: rec.ID, AuthID: res.AuthIDs[idx]})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return nil, err
			}
		}
	}
	for g := 0; g < sc.ModelGroups; g++ {
		rec := ModelGroupRecord{GroupName: fmt.Sprintf("mg-%02d", g)}
		if err := tx.Create(&rec).Error; err != nil {
			return nil, err
		}
		res.MGroups = append(res.MGroups, rec.ID)
		var rows []ModelGroupDetailRecord
		for _, idx := range rng.Perm(len(res.Models))[:min(10+rng.Intn(40), len(res.Models))] {
			ch := JSONB("[]")
			if rng.Intn(8) == 0 && len(res.Channels) > 0 {
				b, _ := json.Marshal([]uint{res.Channels[rng.Intn(len(res.Channels))]})
				ch = JSONB(b)
			}
			rows = append(rows, ModelGroupDetailRecord{ModelGroupID: rec.ID, ModelID: res.Models[idx], Channels: ch})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return nil, err
			}
		}
	}
	var keys []APIKeyRecord
	for k := 0; k < sc.Keys; k++ {
		var ch, mg []uint
		if len(res.Channels) > 0 && rng.Intn(3) > 0 {
			for j := 0; j < 1+rng.Intn(3); j++ {
				ch = append(ch, res.Channels[rng.Intn(len(res.Channels))])
			}
		}
		if len(res.MGroups) > 0 && rng.Intn(2) == 0 {
			for j := 0; j < 1+rng.Intn(2); j++ {
				mg = append(mg, res.MGroups[rng.Intn(len(res.MGroups))])
			}
		}
		chJ, _ := json.Marshal(normalizeChannelGroupIDs(ch))
		mgJ, _ := json.Marshal(normalizeChannelGroupIDs(mg))
		name := fmt.Sprintf("svc-%04d", k)
		key := fmt.Sprintf("sk-synthetic-%06d-%08x", k, rng.Uint32())
		keys = append(keys, APIKeyRecord{APIKey: key, DisplayName: &name, Channels: JSONB(chJ), ModelGroups: JSONB(mgJ)})
		res.APIKeys = append(res.APIKeys, key)
	}
	if len(keys) > 0 {
		if err := tx.CreateInBatches(keys, 200).Error; err != nil {
			return nil, err
		}
	}
	// a few rule-based groups
	minP := 3
	rules := []GroupRule{{EmailPattern: "*@corp-a.example"}, {TagsAny: []string{"vip"}}, {Providers: []string{"vertex"}, MinPriority: &minP}}
	for i, rule := range rules {
		if i >= len(res.Channels) {
			break
		}
		rr := rule
		if err := r.PutChannelGroupRule(ctx, res.Channels[i], &rr, true); err != nil {
			return nil, err
		}
	}
	return res, nil
}
