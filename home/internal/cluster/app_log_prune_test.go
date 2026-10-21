package cluster

import (
	"context"
	"testing"
	"time"
)

func TestPruneAppLogsKeepsRecentRows(t *testing.T) {
	repo := newRefreshTestRepository(t)
	db, err := repo.database()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rows := []AppLogRecord{
		{Line: "old", Level: "info", Timestamp: now.AddDate(0, 0, -5), CreatedAt: now.AddDate(0, 0, -5)},
		{Line: "old2", Level: "info", Timestamp: now.AddDate(0, 0, -4), CreatedAt: now.AddDate(0, 0, -4)},
		{Line: "new", Level: "info", Timestamp: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	n, err := repo.PruneAppLogs(context.Background(), 3)
	if err != nil || n != 2 {
		t.Fatalf("PruneAppLogs = %d, %v; want 2", n, err)
	}
	var left int64
	db.Model(&AppLogRecord{}).Count(&left)
	if left != 1 {
		t.Fatalf("rows left = %d, want 1", left)
	}
	if n, _ := repo.PruneAppLogs(context.Background(), 0); n != 0 {
		t.Fatal("retention 0 must keep everything")
	}
}
