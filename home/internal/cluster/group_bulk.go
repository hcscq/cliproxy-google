package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// BulkResult counts the effect of a bulk operation.
type BulkResult struct {
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

func uniqueTrimmed(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// BulkChannelGroupMembers adds/removes static members of a credential group in
// one transaction. Existing members are skipped.
func (r *Repository) BulkChannelGroupMembers(ctx context.Context, groupID uint, add, remove []string) (BulkResult, error) {
	var res BulkResult
	db, err := r.database()
	if err != nil {
		return res, err
	}
	add, remove = uniqueTrimmed(add), uniqueTrimmed(remove)
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		var g ChannelGroupRecord
		if err := tx.Where("id = ?", groupID).First(&g).Error; err != nil {
			return fmt.Errorf("channel group %d: %w", groupID, err)
		}
		if len(remove) > 0 {
			del := tx.Where("channel_group_id = ? AND auth_id IN ?", groupID, remove).Delete(&ChannelGroupDetailRecord{})
			if del.Error != nil {
				return del.Error
			}
			res.Removed = int(del.RowsAffected)
		}
		if len(add) == 0 {
			return nil
		}
		var existing []string
		if err := tx.Model(&ChannelGroupDetailRecord{}).Where("channel_group_id = ? AND auth_id IN ?", groupID, add).Pluck("auth_id", &existing).Error; err != nil {
			return err
		}
		have := map[string]bool{}
		for _, e := range existing {
			have[e] = true
		}
		now := time.Now().UTC()
		var rows []ChannelGroupDetailRecord
		for _, a := range add {
			if have[a] {
				res.Unchanged++
				continue
			}
			rows = append(rows, ChannelGroupDetailRecord{ChannelGroupID: groupID, AuthID: a, CreatedAt: now, UpdatedAt: now})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 500).Error; err != nil {
				return err
			}
		}
		res.Added = len(rows)
		return tx.Model(&ChannelGroupRecord{}).Where("id = ?", groupID).Update("updated_at", now).Error
	})
	if err == nil {
		InvalidateScopeCaches()
	}
	return res, err
}

// BulkModelGroupMembers adds/removes models of a model group in one
// transaction. channels (optional) applies to newly added models.
func (r *Repository) BulkModelGroupMembers(ctx context.Context, groupID uint, add, remove []string, channels []uint) (BulkResult, error) {
	var res BulkResult
	db, err := r.database()
	if err != nil {
		return res, err
	}
	canon := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, m := range uniqueTrimmed(in) {
			if c := canonicalModelGroupModelID(m); c != "" {
				out = append(out, c)
			}
		}
		return uniqueTrimmed(out)
	}
	add, remove = canon(add), canon(remove)
	chJSON, _ := json.Marshal(normalizeChannelGroupIDs(channels))
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := ensureModelGroupExists(ctx, tx, groupID); err != nil {
			return err
		}
		if len(remove) > 0 {
			del := tx.Where("model_group_id = ? AND model_id IN ?", groupID, remove).Delete(&ModelGroupDetailRecord{})
			if del.Error != nil {
				return del.Error
			}
			res.Removed = int(del.RowsAffected)
		}
		if len(add) == 0 {
			return nil
		}
		var existing []string
		if err := tx.Model(&ModelGroupDetailRecord{}).Where("model_group_id = ?", groupID).Pluck("model_id", &existing).Error; err != nil {
			return err
		}
		have := map[string]bool{}
		for _, e := range existing {
			have[strings.ToLower(canonicalModelGroupModelID(e))] = true
		}
		now := time.Now().UTC()
		var rows []ModelGroupDetailRecord
		for _, m := range add {
			if have[strings.ToLower(m)] {
				res.Unchanged++
				continue
			}
			rows = append(rows, ModelGroupDetailRecord{ModelGroupID: groupID, ModelID: m, Channels: JSONB(chJSON), CreatedAt: now, UpdatedAt: now})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 500).Error; err != nil {
				return err
			}
		}
		res.Added = len(rows)
		return nil
	})
	if err == nil {
		InvalidateScopeCaches()
	}
	return res, err
}

// KeyScopeChange is a bulk change of API key bindings.
type KeyScopeChange struct {
	IDs               []uint `json:"ids"`
	AddChannels       []uint `json:"add_channels"`
	RemoveChannels    []uint `json:"remove_channels"`
	AddModelGroups    []uint `json:"add_model_groups"`
	RemoveModelGroups []uint `json:"remove_model_groups"`
	ClearChannels     bool   `json:"clear_channels"`
	ClearModelGroups  bool   `json:"clear_model_groups"`
}

func applyIDSet(cur, add, remove []uint, clear bool) []uint {
	set := map[uint]bool{}
	if !clear {
		for _, v := range cur {
			set[v] = true
		}
	}
	for _, v := range add {
		set[v] = true
	}
	for _, v := range remove {
		delete(set, v)
	}
	out := make([]uint, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	return normalizeChannelGroupIDs(out)
}

func sameIDs(a, b []uint) bool {
	a, b = normalizeChannelGroupIDs(a), normalizeChannelGroupIDs(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// BulkUpdateAPIKeyScopes changes credential/model group bindings of many keys
// in one transaction.
func (r *Repository) BulkUpdateAPIKeyScopes(ctx context.Context, ch KeyScopeChange) (BulkResult, error) {
	var res BulkResult
	db, err := r.database()
	if err != nil {
		return res, err
	}
	if len(ch.IDs) == 0 {
		return res, errors.New("ids are required")
	}
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		var keys []APIKeyRecord
		if err := tx.Where("id IN ?", ch.IDs).Find(&keys).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		for i := range keys {
			curC, _ := apiKeyChannelsFromJSON(keys[i].Channels)
			curM, _ := apiKeyModelGroupsFromJSON(keys[i].ModelGroups)
			nc := applyIDSet(curC, ch.AddChannels, ch.RemoveChannels, ch.ClearChannels)
			nm := applyIDSet(curM, ch.AddModelGroups, ch.RemoveModelGroups, ch.ClearModelGroups)
			if sameIDs(curC, nc) && sameIDs(curM, nm) {
				res.Unchanged++
				continue
			}
			cj, _ := json.Marshal(nc)
			mj, _ := json.Marshal(nm)
			if err := tx.Model(&APIKeyRecord{}).Where("id = ?", keys[i].ID).Updates(map[string]any{"channels": JSONB(cj), "model_groups": JSONB(mj), "updated_at": now}).Error; err != nil {
				return err
			}
			res.Added++
		}
		return nil
	})
	if err == nil {
		InvalidateScopeCaches()
	}
	return res, err
}

// ---- import / export ----

// GroupExport is the portable membership document.
type GroupExport struct {
	Version          int                     `json:"version"`
	ExportedAt       time.Time               `json:"exported_at"`
	CredentialGroups []CredentialGroupExport `json:"credential_groups"`
	ModelGroups      []ModelGroupExport      `json:"model_groups"`
}

// CredentialGroupExport describes one credential group.
type CredentialGroupExport struct {
	Name    string     `json:"name"`
	Enabled bool       `json:"enabled"`
	Members []string   `json:"members"` // auth ids (import also accepts e-mail / label)
	Rule    *GroupRule `json:"rule,omitempty"`
}

// ModelGroupExport describes one model group.
type ModelGroupExport struct {
	Name    string             `json:"name"`
	Enabled bool               `json:"enabled"`
	Models  []ModelEntryExport `json:"models"`
}

// ModelEntryExport is one model with optional credential-group restriction (by name).
type ModelEntryExport struct {
	Model            string   `json:"model"`
	CredentialGroups []string `json:"credential_groups,omitempty"`
}

// ExportGroups builds the membership document.
func (r *Repository) ExportGroups(ctx context.Context) (*GroupExport, error) {
	s, err := r.Scope(ctx)
	if err != nil {
		return nil, err
	}
	out := &GroupExport{Version: 1, ExportedAt: time.Now().UTC(), CredentialGroups: []CredentialGroupExport{}, ModelGroups: []ModelGroupExport{}}
	names := map[uint]string{}
	for _, id := range sortedKeys(s.ChannelGroups) {
		g := s.ChannelGroups[id]
		names[id] = g.ChannelName
		e := CredentialGroupExport{Name: g.ChannelName, Enabled: !g.Disabled, Members: append([]string{}, s.StaticMembers[id]...)}
		if rec, ok := s.Rules[id]; ok && rec.Enabled {
			if rule, err := rec.ParsedRule(); err == nil {
				e.Rule = &rule
			}
		}
		out.CredentialGroups = append(out.CredentialGroups, e)
	}
	for _, id := range sortedKeys(s.ModelGroups) {
		g := s.ModelGroups[id]
		e := ModelGroupExport{Name: g.GroupName, Enabled: !g.Disabled, Models: []ModelEntryExport{}}
		for _, d := range s.modelDetails[id] {
			me := ModelEntryExport{Model: d.ModelID}
			for _, c := range d.Channels {
				if n, ok := names[c]; ok {
					me.CredentialGroups = append(me.CredentialGroups, n)
				}
			}
			e.Models = append(e.Models, me)
		}
		out.ModelGroups = append(out.ModelGroups, e)
	}
	return out, nil
}

func sortedKeys[V any](m map[uint]V) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ImportReport summarises an import.
type ImportReport struct {
	GroupsCreated  int      `json:"groups_created"`
	MembersAdded   int      `json:"members_added"`
	MembersRemoved int      `json:"members_removed"`
	ModelsAdded    int      `json:"models_added"`
	ModelsRemoved  int      `json:"models_removed"`
	RulesSet       int      `json:"rules_set"`
	Unresolved     []string `json:"unresolved"`
	DryRun         bool     `json:"dry_run"`
}

// ImportGroups applies a membership document in one transaction. Groups are
// matched by name (created when missing). Members may be auth ids, e-mails or
// labels. replace=true makes each listed group's static members / models
// exactly the document's; otherwise they are merged. dryRun rolls back.
func (r *Repository) ImportGroups(ctx context.Context, doc GroupExport, resolve func(string) (string, bool), replace, dryRun bool) (ImportReport, error) {
	rep := ImportReport{Unresolved: []string{}, DryRun: dryRun}
	db, err := r.database()
	if err != nil {
		return rep, err
	}
	errRollback := errors.New("dry run")
	err = db.WithContext(contextOrBackground(ctx)).Transaction(func(tx *gorm.DB) error {
		if err := ensureChannelGroupRuleTable(ctx, tx); err != nil {
			return err
		}
		now := time.Now().UTC()
		cgByName := map[string]uint{}
		var cgs []ChannelGroupRecord
		if err := tx.Find(&cgs).Error; err != nil {
			return err
		}
		for _, g := range cgs {
			cgByName[g.ChannelName] = g.ID
		}
		for _, g := range doc.CredentialGroups {
			name := strings.TrimSpace(g.Name)
			if name == "" {
				continue
			}
			id, ok := cgByName[name]
			if !ok {
				rec := ChannelGroupRecord{ChannelName: name, Disabled: !g.Enabled, CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&rec).Error; err != nil {
					return err
				}
				id = rec.ID
				cgByName[name] = id
				rep.GroupsCreated++
			}
			want := map[string]bool{}
			for _, m := range g.Members {
				if auth, ok := resolve(m); ok {
					want[auth] = true
				} else {
					rep.Unresolved = append(rep.Unresolved, name+": "+m)
				}
			}
			var have []string
			if err := tx.Model(&ChannelGroupDetailRecord{}).Where("channel_group_id = ?", id).Pluck("auth_id", &have).Error; err != nil {
				return err
			}
			haveSet := map[string]bool{}
			for _, h := range have {
				haveSet[h] = true
			}
			var rows []ChannelGroupDetailRecord
			for a := range want {
				if !haveSet[a] {
					rows = append(rows, ChannelGroupDetailRecord{ChannelGroupID: id, AuthID: a, CreatedAt: now, UpdatedAt: now})
				}
			}
			if len(rows) > 0 {
				if err := tx.CreateInBatches(rows, 500).Error; err != nil {
					return err
				}
			}
			rep.MembersAdded += len(rows)
			if replace {
				var drop []string
				for _, h := range have {
					if !want[h] {
						drop = append(drop, h)
					}
				}
				if len(drop) > 0 {
					del := tx.Where("channel_group_id = ? AND auth_id IN ?", id, drop).Delete(&ChannelGroupDetailRecord{})
					if del.Error != nil {
						return del.Error
					}
					rep.MembersRemoved += int(del.RowsAffected)
				}
			}
			if g.Rule != nil {
				if err := g.Rule.Validate(); err != nil {
					return fmt.Errorf("group %s: %w", name, err)
				}
				raw, _ := json.Marshal(g.Rule)
				if err := tx.Save(&ChannelGroupRuleRecord{ChannelGroupID: id, Enabled: true, Rule: string(raw), UpdatedAt: now}).Error; err != nil {
					return err
				}
				rep.RulesSet++
			}
		}
		mgByName := map[string]uint{}
		var mgs []ModelGroupRecord
		if err := tx.Find(&mgs).Error; err != nil {
			return err
		}
		for _, g := range mgs {
			mgByName[g.GroupName] = g.ID
		}
		for _, g := range doc.ModelGroups {
			name := strings.TrimSpace(g.Name)
			if name == "" {
				continue
			}
			id, ok := mgByName[name]
			if !ok {
				rec := ModelGroupRecord{GroupName: name, Disabled: !g.Enabled, CreatedAt: now, UpdatedAt: now}
				if err := tx.Create(&rec).Error; err != nil {
					return err
				}
				id = rec.ID
				rep.GroupsCreated++
			}
			var have []ModelGroupDetailRecord
			if err := tx.Where("model_group_id = ?", id).Find(&have).Error; err != nil {
				return err
			}
			haveSet := map[string]bool{}
			for _, h := range have {
				haveSet[strings.ToLower(canonicalModelGroupModelID(h.ModelID))] = true
			}
			want := map[string]bool{}
			var rows []ModelGroupDetailRecord
			for _, m := range g.Models {
				mid := canonicalModelGroupModelID(strings.TrimSpace(m.Model))
				if mid == "" {
					continue
				}
				want[strings.ToLower(mid)] = true
				if haveSet[strings.ToLower(mid)] {
					continue
				}
				var chans []uint
				for _, cn := range m.CredentialGroups {
					if cid, ok := cgByName[strings.TrimSpace(cn)]; ok {
						chans = append(chans, cid)
					} else {
						rep.Unresolved = append(rep.Unresolved, name+"/"+mid+": credential group "+cn)
					}
				}
				cj, _ := json.Marshal(normalizeChannelGroupIDs(chans))
				rows = append(rows, ModelGroupDetailRecord{ModelGroupID: id, ModelID: mid, Channels: JSONB(cj), CreatedAt: now, UpdatedAt: now})
				haveSet[strings.ToLower(mid)] = true
			}
			if len(rows) > 0 {
				if err := tx.CreateInBatches(rows, 500).Error; err != nil {
					return err
				}
			}
			rep.ModelsAdded += len(rows)
			if replace {
				var drop []uint
				for _, h := range have {
					if !want[strings.ToLower(canonicalModelGroupModelID(h.ModelID))] {
						drop = append(drop, h.ID)
					}
				}
				if len(drop) > 0 {
					del := tx.Where("id IN ?", drop).Delete(&ModelGroupDetailRecord{})
					if del.Error != nil {
						return del.Error
					}
					rep.ModelsRemoved += int(del.RowsAffected)
				}
			}
		}
		if dryRun {
			return errRollback
		}
		return nil
	})
	if errors.Is(err, errRollback) {
		err = nil
	}
	if err == nil && !dryRun {
		InvalidateScopeCaches()
	}
	return rep, err
}
