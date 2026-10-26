package management

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	log "github.com/sirupsen/logrus"
)

const (
	importJobMaxItems = 5000
	importJobChunk    = 50
	importJobStale    = 3 * time.Minute
)

// StartImportJob: POST /credentials/import/jobs {text, items, egress_id, allow_primary, reserve, health_check}
// — runs a refresh-token / minimal-JSON import in the background; progress and
// per-item results are persisted so the panel can re-attach after tab switch or reload.
func (h *Handler) StartImportJob(c *gin.Context) {
	var body struct {
		Text         string `json:"text"`
		Items        []any  `json:"items"`
		EgressID     string `json:"egress_id"`
		AllowPrimary *bool  `json:"allow_primary"`
		Reserve      bool   `json:"reserve"`
		HealthCheck  *bool  `json:"health_check"`
		Source       string `json:"source"`
	}
	if errBind := json.NewDecoder(io.LimitReader(c.Request.Body, 32<<20)).Decode(&body); errBind != nil {
		respondError(c, http.StatusBadRequest, "invalid_body", fmt.Errorf("body must be JSON: {text, items}"))
		return
	}
	entries := parseRefreshTokenInput(body.Text)
	for _, item := range body.Items {
		entries = append(entries, entryFromJSONValue(len(entries), item))
	}
	if len(entries) == 0 {
		respondError(c, http.StatusBadRequest, "empty", fmt.Errorf("no refresh tokens found in input"))
		return
	}
	if len(entries) > importJobMaxItems {
		respondError(c, http.StatusRequestEntityTooLarge, "too_many_items", fmt.Errorf("at most %d entries per job", importJobMaxItems))
		return
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	now := time.Now()
	source := strings.TrimSpace(body.Source)
	if source == "" {
		source = "refresh_token"
	}
	job := &cluster.CredentialImportJob{
		ID: "job-" + now.In(healthZone).Format("20060102-150405") + "-" + hex.EncodeToString(b), Source: source, Status: "running",
		Total: len(entries), Reserve: body.Reserve, BatchID: newHealthBatchID(), StartedAt: now, UpdatedAt: now,
	}
	if err := h.repo.SaveImportJob(c.Request.Context(), job); err != nil {
		respondError(c, http.StatusInternalServerError, "job_save_failed", err)
		return
	}
	opts := rtImportOptions{Egress: body.EgressID, AllowPrimary: body.AllowPrimary, Reserve: body.Reserve}
	go h.runImportJob(job, entries, opts, body.HealthCheck)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "job": job})
}

func (h *Handler) runImportJob(job *cluster.CredentialImportJob, entries []rtImportEntry, opts rtImportOptions, healthCheck *bool) {
	ctx := context.Background()
	all := make([]RTImportResult, 0, len(entries))
	defer func() {
		if rec := recover(); rec != nil {
			job.Status, job.Error = "failed", fmt.Sprint(rec)
			h.saveImportJob(ctx, job, all)
		}
	}()
	for i := 0; i < len(entries); i += importJobChunk {
		end := min(i+importJobChunk, len(entries))
		chunk := append([]rtImportEntry(nil), entries[i:end]...)
		for k := range chunk {
			chunk[k].Index = k
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		results, _, err := h.importAntigravityEntries(cctx, chunk, opts)
		cancel()
		if err != nil {
			job.Status, job.Error = "failed", shortReason(err.Error())
			h.saveImportJob(ctx, job, all)
			return
		}
		for _, r := range results {
			r.Index += i
			all = append(all, r)
		}
		resp := gin.H{}
		h.afterImport(ctx, resp, results, job.BatchID, healthCheck, job.Source)
		if v, ok := resp["health_batch_id"].(string); ok {
			job.HealthID = v
		}
		job.Done = len(all)
		h.saveImportJob(ctx, job, all)
	}
	job.Status = "done"
	h.saveImportJob(ctx, job, all)
}

func (h *Handler) saveImportJob(ctx context.Context, job *cluster.CredentialImportJob, results []RTImportResult) {
	job.Created, job.Updated, job.Failed = 0, 0, 0
	for _, r := range results {
		switch r.Status {
		case "created":
			job.Created++
		case "updated":
			job.Updated++
		case "failed", "skipped":
			job.Failed++
		}
	}
	raw, _ := json.Marshal(results)
	job.Results = string(raw)
	job.UpdatedAt = time.Now()
	if err := h.repo.SaveImportJob(ctx, job); err != nil {
		log.Warnf("import job %s: save: %v", job.ID, err)
	}
}

func markStaleImportJob(job *cluster.CredentialImportJob) {
	if job.Status == "running" && time.Since(job.UpdatedAt) > importJobStale+5*time.Minute {
		job.Status = "interrupted"
	}
}

// ListImportJobs: GET /credentials/import/jobs — recent jobs (no per-item results).
func (h *Handler) ListImportJobs(c *gin.Context) {
	jobs, err := h.repo.ListImportJobs(c.Request.Context(), 20)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "jobs_load_failed", err)
		return
	}
	for i := range jobs {
		markStaleImportJob(&jobs[i])
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// GetImportJob: GET /credentials/import/job?id= — one job with per-item results.
func (h *Handler) GetImportJob(c *gin.Context) {
	job, err := h.repo.GetImportJob(c.Request.Context(), strings.TrimSpace(c.Query("id")))
	if err != nil {
		respondError(c, http.StatusInternalServerError, "job_load_failed", err)
		return
	}
	if job == nil {
		respondError(c, http.StatusNotFound, "not_found", nil)
		return
	}
	markStaleImportJob(job)
	results := []RTImportResult{}
	_ = json.Unmarshal([]byte(job.Results), &results)
	c.JSON(http.StatusOK, gin.H{"job": job, "results": results})
}
