package management

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"github.com/router-for-me/CLIProxyAPIHome/internal/home"
	log "github.com/sirupsen/logrus"
)

// Import health check (导入体检) orchestration: runs after every import batch (async, so
// big batches never hit HTTP timeouts; the import dialog polls the batch summary) and
// on demand. Concurrency is bounded per Home by the configured limit.

const (
	healthQuotaWait     = 25 * time.Second
	healthCheckDeadline = 3 * time.Minute
)

var healthZone = time.FixedZone("UTC+8", 8*3600)

var (
	healthSemMu sync.Mutex
	healthSem   chan struct{}
)

func healthSlots(n int) chan struct{} {
	healthSemMu.Lock()
	defer healthSemMu.Unlock()
	if n < 1 {
		n = 1
	}
	if healthSem == nil || cap(healthSem) != n {
		healthSem = make(chan struct{}, n)
	}
	return healthSem
}

func newHealthBatchID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().In(healthZone).Format("20060102-1504") + "-" + hex.EncodeToString(b)
}

// startHealthChecks runs checks for ids in the background.
func (h *Handler) startHealthChecks(ids []string) {
	if h.runtime == nil || len(ids) == 0 {
		return
	}
	go h.runHealthChecks(context.Background(), ids)
}

func (h *Handler) runHealthChecks(ctx context.Context, ids []string) {
	cfg := h.runtime.ImportCheckSettings()
	slots := healthSlots(cfg.Concurrency)
	imports, _ := h.repo.ListCredentialImports(ctx)
	prev, _ := h.repo.ListHealthChecks(ctx)
	var wg sync.WaitGroup
	for _, id := range ids {
		id := strings.TrimSpace(id)
		if id == "" {
			continue
		}
		rec := cluster.CredentialHealthCheck{AuthUUID: id, Status: "running", CheckedAt: time.Now().UTC(), Categories: "", Reasons: ""}
		if imp, ok := imports[id]; ok {
			rec.BatchID = imp.BatchID
			t := imp.ImportedAt
			rec.ImportedAt = &t
		}
		if p, ok := prev[id]; ok && p.Status == "fail" {
			rec.RecheckCount = p.RecheckCount + 1 // consecutive failure if this one fails too
		}
		_ = h.repo.SaveHealthCheck(ctx, rec)
		wg.Add(1)
		go func(rec cluster.CredentialHealthCheck) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			h.checkOne(ctx, rec)
		}(rec)
	}
	wg.Wait()
}

func (h *Handler) checkOne(parent context.Context, rec cluster.CredentialHealthCheck) {
	ctx, cancel := context.WithTimeout(parent, healthCheckDeadline)
	defer cancel()
	id := rec.AuthUUID
	cfg := h.runtime.ImportCheckSettings()
	started := time.Now()
	if h.quotaRecollect != nil {
		if _, err := h.quotaRecollect.TriggerCollection(ctx, map[string]struct{}{id: {}}, nil); err != nil {
			log.Debugf("health check %s: quota trigger: %v", id, err)
		}
	}
	h.runtime.RefreshAntigravityCatalogs(ctx, id)
	probe := h.runtime.HealthProbe(ctx, id, cfg.Model)
	var in home.HealthInputs
	deadline := time.Now().Add(healthQuotaWait)
	for {
		got, fresh, err := h.repo.HealthInputs(ctx, id, started.Add(-2*time.Second))
		if err == nil {
			in = got
		}
		if fresh > 0 || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	v := home.ClassifyHealth(probe, in, cfg)
	raw, _ := json.Marshal(struct {
		home.HealthProbeResult
		Inputs home.HealthInputs `json:"inputs"`
	}{probe, in})
	rec.Status = v.Status
	if v.Status != "fail" {
		rec.RecheckCount = 0
	}
	rec.Categories = strings.Join(v.Categories, ",")
	rec.Reasons = strings.Join(v.Reasons, "\n")
	rec.Evidence = v.Evidence
	rec.Probe = string(raw)
	rec.Tier = probe.Tier
	rec.Model = cfg.Model
	rec.VerifyURL = ""
	for _, c := range v.Categories {
		if c == home.HealthNeedsVerify {
			rec.VerifyURL = probe.VerifyURL
		}
	}
	rec.CheckedAt = time.Now().UTC()
	if err := h.repo.SaveHealthCheck(context.WithoutCancel(ctx), rec); err != nil {
		log.Warnf("health check %s: save: %v", id, err)
		return
	}
	log.Infof("health check %s: %s %s", id, v.Status, rec.Categories)
	if cfg.BlockFailed {
		h.runtime.ReserveChanged(context.WithoutCancel(ctx)) // refresh scheduling exclusions
	}
}

func healthItem(rec cluster.CredentialHealthCheck, email string, seq int64) gin.H {
	item := gin.H{"auth_id": rec.AuthUUID, "status": rec.Status, "categories": rec.CategoryList(), "reasons": rec.ReasonList(),
		"checked_at": rec.CheckedAt, "tier": rec.Tier, "model": rec.Model}
	if rec.Evidence != "" {
		item["evidence"] = rec.Evidence
	}
	if rec.VerifyURL != "" {
		item["verify_url"] = rec.VerifyURL
	}
	if rec.BatchID != "" {
		item["batch_id"] = rec.BatchID
	}
	if rec.ImportedAt != nil {
		item["imported_at"] = rec.ImportedAt
	}
	if email != "" {
		item["email"] = email
	}
	if seq > 0 {
		item["seq"] = seq
	}
	return item
}

func healthSummary(recs []cluster.CredentialHealthCheck) gin.H {
	by := map[string]int{}
	pass, fail, running := 0, 0, 0
	for _, r := range recs {
		switch r.Status {
		case "pass":
			pass++
		case "fail":
			fail++
			for _, c := range r.CategoryList() {
				by[c]++
			}
		default:
			running++
		}
	}
	return gin.H{"total": len(recs), "pass": pass, "fail": fail, "running": running, "by_category": by}
}

// StartHealthCheck: POST /credentials/health-check {ids} — run checks in background.
func (h *Handler) StartHealthCheck(c *gin.Context) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 2000 {
		respondError(c, http.StatusBadRequest, "invalid body", err)
		return
	}
	if h.runtime == nil {
		respondError(c, http.StatusServiceUnavailable, "runtime_unavailable", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	ids := make([]string, 0, len(req.IDs))
	var failed []string
	for _, id := range req.IDs {
		a, err := h.findOAuthAuth(ctx, authIdentifier{ID: id, Name: id})
		if err != nil || a == nil {
			failed = append(failed, id)
			continue
		}
		ids = append(ids, a.ID)
	}
	h.startHealthChecks(ids)
	c.JSON(http.StatusAccepted, gin.H{"status": "ok", "accepted": len(ids), "failed": nonNilStrings(failed)})
}

func (h *Handler) healthRecords(ctx context.Context, batchID string, ids map[string]bool) ([]cluster.CredentialHealthCheck, map[string]string, map[string]int64) {
	all, _ := h.repo.ListHealthChecks(ctx)
	emails, _ := h.repo.CredentialEmails(ctx)
	seqs, _ := h.repo.AuthSeqs(ctx)
	out := make([]cluster.CredentialHealthCheck, 0, len(all))
	for id, r := range all {
		if _, live := emails[id]; !live {
			continue
		}
		if batchID != "" && r.BatchID != batchID {
			continue
		}
		if ids != nil && !ids[id] {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return seqs[out[i].AuthUUID] < seqs[out[j].AuthUUID] })
	return out, emails, seqs
}

// HealthCheckBatch: GET /credentials/health-check/batch?batch_id= — progress + summary.
func (h *Handler) HealthCheckBatch(c *gin.Context) {
	batch := strings.TrimSpace(c.Query("batch_id"))
	if batch == "" {
		respondError(c, http.StatusBadRequest, "batch_id is required", nil)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	recs, emails, seqs := h.healthRecords(ctx, batch, nil)
	all := parseBoolFlag(c.Query("all"))
	var argon map[string]bool
	if all {
		ids := make([]string, 0, len(recs))
		for _, r := range recs {
			ids = append(ids, r.AuthUUID)
		}
		argon, _ = h.repo.AntigravityCatalogContains(ctx, ids, "gemini-4-argon")
	}
	verdicts, _ := h.repo.ListImportVerdicts(ctx)
	items := make([]gin.H, 0)
	for _, r := range recs {
		if all || r.Status != "pass" {
			item := healthItem(r, emails[r.AuthUUID], seqs[r.AuthUUID])
			if v, ok := verdicts[r.AuthUUID]; ok {
				item["import"] = importVerdictJSON(v)
			}
			if has, ok := argon[r.AuthUUID]; ok {
				item["argon"] = has
			}
			items = append(items, item)
		}
	}
	imports, _ := h.repo.ListCredentialImports(ctx)
	imported := 0
	for _, imp := range imports {
		if imp.BatchID == batch {
			imported++
		}
	}
	c.JSON(http.StatusOK, gin.H{"batch_id": batch, "imported": imported, "summary": healthSummary(recs), "items": items})
}

var healthCategoryZh = map[string]string{
	home.HealthNeedsVerify: "需验证", home.HealthTier: "订阅不符", home.HealthQuotaUsed: "额度非新", home.HealthQuotaNone: "额度未知",
	home.HealthNoCatalog: "无模型目录", home.HealthTOS: "403停用/违反条款", home.HealthPermission: "403无权限",
	home.HealthRateLimited: "429限流", home.HealthRegion: "地区限制", home.HealthRegionWarn: "地区提示", home.HealthAuth: "令牌无效", home.HealthNoEgress: "无出口", home.HealthError: "检查失败",
}

func fmtHealthTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.In(healthZone).Format("2006-01-02 15:04:05") + " UTC+8"
}

// ExportHealthCheck: GET /credentials/health-check/export?status=fail|all&batch_id=&ids=&format=csv|text
// — evidence list for supplier warranty claims.
func (h *Handler) ExportHealthCheck(c *gin.Context) {
	ctx, cancel := h.requestContext(c)
	defer cancel()
	var ids map[string]bool
	if raw := strings.TrimSpace(c.Query("ids")); raw != "" {
		ids = map[string]bool{}
		for _, id := range strings.Split(raw, ",") {
			ids[strings.TrimSpace(id)] = true
		}
	}
	recs, emails, seqs := h.healthRecords(ctx, strings.TrimSpace(c.Query("batch_id")), ids)
	onlyFail := c.DefaultQuery("status", "fail") != "all"
	imports, _ := h.repo.ListCredentialImports(ctx)
	var buf bytes.Buffer
	text := c.Query("format") == "text"
	w := csv.NewWriter(&buf)
	if !text {
		_ = w.Write([]string{"seq", "email", "status", "reasons", "categories", "raw_error", "checked_at", "import_batch", "imported_at"})
	}
	n := 0
	for _, r := range recs {
		if onlyFail && r.Status != "fail" {
			continue
		}
		cats := make([]string, 0)
		for _, k := range r.CategoryList() {
			if zh := healthCategoryZh[k]; zh != "" {
				cats = append(cats, zh)
			} else {
				cats = append(cats, k)
			}
		}
		batch, imported := r.BatchID, r.ImportedAt
		if imp, ok := imports[r.AuthUUID]; ok {
			batch = imp.BatchID
			t := imp.ImportedAt
			imported = &t
		}
		checked := r.CheckedAt
		reasons := strings.Join(r.ReasonList(), "；")
		evidence := strings.Join(strings.Fields(r.Evidence), " ")
		if text {
			buf.WriteString(emails[r.AuthUUID] + "\t" + reasons + "\t" + fmtHealthTime(&checked) + "\t" + evidence + "\n")
		} else {
			_ = w.Write([]string{strconv.FormatInt(seqs[r.AuthUUID], 10), emails[r.AuthUUID], r.Status, reasons, strings.Join(cats, "/"),
				evidence, fmtHealthTime(&checked), batch, fmtHealthTime(imported)})
		}
		n++
	}
	w.Flush()
	c.Header("X-Rows", strconv.Itoa(n))
	if text {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", buf.Bytes())
		return
	}
	c.Header("Content-Disposition", `attachment; filename="health-check-`+time.Now().In(healthZone).Format("20060102-1504")+`.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", append([]byte("\xEF\xBB\xBF"), buf.Bytes()...))
}

// healthAnnotations returns credential -> panel health object for search results.
func (h *Handler) healthAnnotations(ctx context.Context) map[string]gin.H {
	all, err := h.repo.ListHealthChecks(ctx)
	if err != nil {
		return nil
	}
	verdicts, _ := h.repo.ListImportVerdicts(ctx)
	out := make(map[string]gin.H, len(all))
	for id, r := range all {
		item := gin.H{"status": r.Status, "categories": r.CategoryList(), "reasons": r.ReasonList(), "checked_at": r.CheckedAt, "tier": r.Tier, "evidence": clipString(r.Evidence, 600)}
		if r.VerifyURL != "" {
			item["verify_url"] = r.VerifyURL
		}
		if v, ok := verdicts[id]; ok {
			item["import"] = importVerdictJSON(v)
		}
		out[id] = item
	}
	return out
}

func clipString(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func importVerdictJSON(v cluster.CredentialImportVerdict) gin.H {
	out := gin.H{"status": v.Status, "categories": v.CategoryList(), "reasons": v.ReasonList(), "checked_at": v.CheckedAt, "tier": v.Tier, "sticky": v.Sticky}
	if v.Argon != nil {
		out["argon"] = *v.Argon
	}
	if v.ClearedAt != nil {
		out["cleared_at"] = v.ClearedAt
	}
	if v.Note != "" {
		out["note"] = v.Note
	}
	return out
}

// ClearImportVerdict: POST /credentials/import-verdict/clear {ids} — clears the sticky import flag (snapshot is kept).
func (h *Handler) ClearImportVerdict(c *gin.Context) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 {
		respondError(c, http.StatusBadRequest, "ids is required", err)
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	n, err := h.repo.ClearImportVerdictSticky(ctx, body.IDs)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "clear_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "cleared": n})
}
