package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func TestImportJobPersistsProgressAndResults(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	h := NewHandler(repo, nil, "127.0.0.1", 0)
	engine := gin.New()
	engine.POST("/jobs", h.StartImportJob)
	engine.GET("/jobs", h.ListImportJobs)
	engine.GET("/job", h.GetImportJob)

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(`{"text":"not a token\nalso bad line","reserve":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var start struct{ Job cluster.CredentialImportJob }
	_ = json.Unmarshal(w.Body.Bytes(), &start)
	if start.Job.ID == "" || start.Job.Total != 2 {
		t.Fatalf("bad job: %+v", start.Job)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, _ := repo.GetImportJob(context.Background(), start.Job.ID)
		if job != nil && job.Status != "running" {
			if job.Status != "done" || job.Done != 2 || job.Failed != 2 || !job.Reserve {
				t.Fatalf("final job: %+v", job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/job?id="+start.Job.ID, nil))
	var got struct {
		Results []RTImportResult
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Results) != 2 || got.Results[1].Index != 1 || got.Results[0].Status != "failed" {
		t.Fatalf("results: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/jobs", nil))
	if !strings.Contains(w.Body.String(), start.Job.ID) || strings.Contains(w.Body.String(), "token_hint") {
		t.Fatalf("list: %s", w.Body.String())
	}
}
