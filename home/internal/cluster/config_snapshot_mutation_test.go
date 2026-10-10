package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestMutateConfigSnapshotConcurrentRepositories(t *testing.T) {
	repo := newCredentialFoundationTestRepository(t)
	ctx := context.Background()
	if errSeed := repo.ReplaceConfigSnapshot(ctx, map[string]any{"logs-max-total-size-mb": 0}); errSeed != nil {
		t.Fatal(errSeed)
	}
	var database struct{ File string }
	if errFile := repo.db.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&database).Error; errFile != nil {
		t.Fatal(errFile)
	}
	otherDB, errOpen := OpenSQLite(ctx, database.File)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	otherSQLDB, errDB := otherDB.DB()
	if errDB != nil {
		t.Fatal(errDB)
	}
	t.Cleanup(func() {
		if errClose := otherSQLDB.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	repos := []*Repository{repo, NewRepository(otherDB)}
	const count = 12
	start := make(chan struct{})
	results := make(chan error, count)
	var workers sync.WaitGroup
	for index := range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- repos[index%len(repos)].MutateConfigSnapshot(ctx, DefaultHeartbeatTimeout(), func(root map[string]any) (map[string]any, error) {
				root["logs-max-total-size-mb"] = int(root["logs-max-total-size-mb"].(float64)) + 1
				return root, nil
			})
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for errMutation := range results {
		if errMutation != nil {
			t.Error(errMutation)
		}
	}
	snapshot, errSnapshot := repo.LoadConfigSnapshot(ctx)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	var got int
	if errDecode := json.Unmarshal(snapshot["logs-max-total-size-mb"], &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	if got != count {
		t.Fatalf("committed edits=%d, want %d", got, count)
	}
}

func TestMutateConfigSnapshotRollsBackRejectedEdit(t *testing.T) {
	repo := newCredentialFoundationTestRepository(t)
	ctx := context.Background()
	if errSeed := repo.ReplaceConfigSnapshot(ctx, map[string]any{"debug": false}); errSeed != nil {
		t.Fatal(errSeed)
	}
	before, errBefore := repo.LoadConfigSnapshot(ctx)
	if errBefore != nil {
		t.Fatal(errBefore)
	}
	rejected := errors.New("rejected fixture edit")
	err := repo.MutateConfigSnapshot(ctx, DefaultHeartbeatTimeout(), func(root map[string]any) (map[string]any, error) {
		root["debug"] = true
		return root, rejected
	})
	if !errors.Is(err, rejected) {
		t.Fatalf("rejected mutation returned %v", err)
	}
	after, errAfter := repo.LoadConfigSnapshot(ctx)
	if errAfter != nil {
		t.Fatal(errAfter)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected edit changed persisted configuration")
	}
}

func TestOAuthConfigUpsertsPreserveConcurrentScopes(t *testing.T) {
	repo := newCredentialFoundationTestRepository(t)
	ctx, cancelCtx := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCtx()
	if errSeed := repo.ReplaceConfigSnapshot(ctx, map[string]any{"oauth": map[string]any{"providers": map[string]any{
		"codex":       map[string]any{"disable-codex-cloaking": false},
		"antigravity": map[string]any{"sensitive-words": []string{"old"}},
		"xai":         map[string]any{"inject-x-search": false},
		"devin":       map[string]any{"sensitive-words": []string{"old"}},
	}}}); errSeed != nil {
		t.Fatal(errSeed)
	}
	var database struct{ File string }
	if errFile := repo.db.Raw("SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&database).Error; errFile != nil {
		t.Fatal(errFile)
	}
	otherDB, errOpen := OpenSQLite(ctx, database.File)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	otherSQLDB, errDB := otherDB.DB()
	if errDB != nil {
		t.Fatal(errDB)
	}
	t.Cleanup(func() {
		if errClose := otherSQLDB.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	repos := []*Repository{repo, NewRepository(otherDB)}
	edits := []struct {
		key   string
		value map[string]any
	}{
		{key: "codex", value: map[string]any{"disable-codex-cloaking": true}},
		{key: "antigravity", value: map[string]any{"sensitive-words": []string{"new"}}},
		{key: "xai", value: map[string]any{"inject-x-search": true}},
		{key: "devin", value: map[string]any{"sensitive-words": []string{"new"}}},
	}
	start := make(chan struct{})
	results := make(chan error, len(edits))
	for index, edit := range edits {
		go func() {
			<-start
			results <- repos[index%len(repos)].UpsertConfigValue(ctx, edit.key, edit.value)
		}()
	}
	close(start)
	for range edits {
		if errUpdate := <-results; errUpdate != nil {
			t.Error(errUpdate)
		}
	}
	cfg, _, errConfig := repo.LoadConfigAsRuntimeConfig(ctx)
	if errConfig != nil {
		t.Fatal(errConfig)
	}
	if !cfg.Codex.DisableCodexCloaking || !cfg.XAI.InjectXSearch ||
		!reflect.DeepEqual(cfg.Antigravity.SensitiveWords, []string{"new"}) || !reflect.DeepEqual(cfg.Devin.SensitiveWords, []string{"new"}) {
		t.Fatal("a concurrent edit replaced another provider's OAuth settings")
	}
}
