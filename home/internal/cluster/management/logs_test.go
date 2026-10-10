package management

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
	"gorm.io/gorm"
)

func TestGetLogsReturnsDatabaseAppLogs(t *testing.T) {
	t.Parallel()

	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	now := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	requestID := "018f3a5b-1234-7abc-def0-12345678abcd"
	records := []cluster.AppLogRecord{
		{Timestamp: now.Add(-time.Minute), ClientIP: "10.0.0.6", RequestID: "req-other", HomeIP: "192.0.2.10", Level: "info", Line: "ignored", CreatedAt: now.Add(-time.Minute)},
		{Timestamp: now, ClientIP: "10.0.0.5", RequestID: requestID, HomeIP: "192.0.2.10", Level: "warn", Line: "wanted", CreatedAt: now},
	}
	if errCreate := db.Create(&records).Error; errCreate != nil {
		t.Fatalf("create logs: %v", errCreate)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/logs", handler.GetLogs)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logs?home_ip=192.0.2.10&request_id="+requestID+"&limit=10", nil)
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}

	var body struct {
		Logs []struct {
			ClientIP  string `json:"client_ip"`
			RequestID string `json:"request_id"`
			HomeIP    string `json:"home_ip"`
			Level     string `json:"level"`
			Line      string `json:"line"`
		} `json:"logs"`
		Total  int64 `json:"total"`
		Limit  int   `json:"limit"`
		Offset int   `json:"offset"`
	}
	if errDecode := json.Unmarshal(resp.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if body.Total != 1 || len(body.Logs) != 1 {
		t.Fatalf("logs total=%d len=%d, want 1", body.Total, len(body.Logs))
	}
	if body.Logs[0].ClientIP != "10.0.0.5" || body.Logs[0].RequestID != "5678abcd" || body.Logs[0].HomeIP != "192.0.2.10" || body.Logs[0].Level != "warn" || body.Logs[0].Line != "wanted" {
		t.Fatalf("unexpected log record: %+v", body.Logs[0])
	}
	if body.Limit != 10 || body.Offset != 0 {
		t.Fatalf("pagination = limit %d offset %d, want 10/0", body.Limit, body.Offset)
	}
	var stored cluster.AppLogRecord
	if errRead := db.First(&stored, records[1].ID).Error; errRead != nil {
		t.Fatalf("read stored app log: %v", errRead)
	}
	if stored.RequestID != requestID {
		t.Fatalf("stored request_id = %q, want full ID %q", stored.RequestID, requestID)
	}
}

func TestDeleteLogsClearsDatabaseAppLogs(t *testing.T) {
	t.Parallel()

	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	now := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	records := []cluster.AppLogRecord{
		{Timestamp: now, ClientIP: "10.0.0.5", RequestID: "req-1", HomeIP: "192.0.2.10", Level: "info", Line: "first", CreatedAt: now},
		{Timestamp: now.Add(time.Second), ClientIP: "10.0.0.6", RequestID: "req-2", HomeIP: "192.0.2.11", Level: "warn", Line: "second", CreatedAt: now.Add(time.Second)},
	}
	if errCreate := db.Create(&records).Error; errCreate != nil {
		t.Fatalf("create logs: %v", errCreate)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/logs", handler.GetLogs)
	engine.DELETE("/logs", handler.DeleteLogs)

	type deleteLogsResponse struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Removed int64  `json:"removed"`
	}
	deleteLogs := func() deleteLogsResponse {
		t.Helper()
		resp := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, "/logs", nil)
		engine.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("DELETE status = %d, body = %s", resp.Code, resp.Body.String())
		}
		var body deleteLogsResponse
		if errDecode := json.Unmarshal(resp.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode DELETE response: %v", errDecode)
		}
		return body
	}

	first := deleteLogs()
	if !first.Success || first.Message != "Logs cleared successfully" || first.Removed != 2 {
		t.Fatalf("first DELETE response = %+v, want success with 2 removed", first)
	}

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/logs", nil)
	engine.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET after DELETE status = %d, body = %s", resp.Code, resp.Body.String())
	}
	var listed struct {
		Logs  []json.RawMessage `json:"logs"`
		Total int64             `json:"total"`
	}
	if errDecode := json.Unmarshal(resp.Body.Bytes(), &listed); errDecode != nil {
		t.Fatalf("decode GET response: %v", errDecode)
	}
	if listed.Total != 0 || len(listed.Logs) != 0 {
		t.Fatalf("GET after DELETE = total %d logs %d, want empty", listed.Total, len(listed.Logs))
	}

	second := deleteLogs()
	if !second.Success || second.Removed != 0 {
		t.Fatalf("second DELETE response = %+v, want idempotent success", second)
	}
}

func TestRequestIDSearchReturnsDistinctCandidates(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)
	requestIDs := []string{
		"018f3a5b-1234-7abc-def0-12345678abcd",
		"018f3a5b-5678-7abc-def0-99995678abcd",
		"prefix-5678abcd-not-a-suffix",
		"literal-a_%!tail",
		"req-1",
	}
	now := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	for index, requestID := range requestIDs {
		timestamp := now.Add(time.Duration(index) * time.Second)
		record := cluster.AppLogRecord{
			Timestamp: timestamp,
			ClientIP:  "10.0.0.5",
			RequestID: requestID,
			HomeIP:    "192.0.2.10",
			Level:     "info",
			Line:      "request log",
			CreatedAt: timestamp,
		}
		if errCreate := db.Create(&record).Error; errCreate != nil {
			t.Fatalf("create app log: %v", errCreate)
		}
		payload := fmt.Sprintf(`{"timestamp":%q,"request_id":%q,"provider":"openai","model":"gpt-4.1-mini","tokens":{"total_tokens":1}}`, timestamp.Format(time.RFC3339), requestID)
		if _, errUsage := repo.AppendUsageWithRuntime(t.Context(), payload, cluster.UsageRuntimeMetadata{HomeIP: "192.0.2.10"}); errUsage != nil {
			t.Fatalf("append usage: %v", errUsage)
		}
	}
	handler := NewHandler(repo, nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/logs", handler.GetLogs)
	engine.GET("/usage/records", handler.ListUsageRecords)
	engine.GET("/request-logs", handler.ListRequestLogs)
	engine.GET("/request-events", handler.ListRequestEvents)
	for _, endpoint := range []string{"/logs", "/usage/records", "/request-logs", "/request-events"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, tt := range []struct {
				name      string
				query     string
				wantIDs   []string
				wantTotal int64
			}{
				{name: "full ID stays exact", query: "request_id=" + requestIDs[0], wantIDs: requestIDs[:1], wantTotal: 1},
				{name: "eight characters return both candidates", query: "request_id=5678abcd", wantIDs: requestIDs[:2], wantTotal: 2},
				{name: "shorter ID stays exact", query: "request_id=req-1", wantIDs: requestIDs[4:], wantTotal: 1},
				{name: "longer custom ID stays exact", query: "request_id=" + requestIDs[2], wantIDs: requestIDs[2:3], wantTotal: 1},
				{name: "suffix is literal", query: "request_id=" + url.QueryEscape("a_%!tail"), wantIDs: requestIDs[3:4], wantTotal: 1},
				{name: "pagination keeps candidate count", query: "request_id=5678abcd&limit=1&offset=1", wantIDs: requestIDs[:1], wantTotal: 2},
			} {
				t.Run(tt.name, func(t *testing.T) {
					resp := httptest.NewRecorder()
					engine.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, endpoint+"?"+tt.query, nil))
					if resp.Code != http.StatusOK {
						t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
					}
					var response struct {
						Logs []struct {
							ID        json.RawMessage `json:"id"`
							RequestID string          `json:"request_id"`
						} `json:"logs"`
						Items []struct {
							ID        json.RawMessage `json:"id"`
							RequestID string          `json:"request_id"`
						} `json:"items"`
						Total int64 `json:"total"`
					}
					if errDecode := json.Unmarshal(resp.Body.Bytes(), &response); errDecode != nil {
						t.Fatalf("decode response: %v", errDecode)
					}
					items := response.Items
					if endpoint == "/logs" {
						items = response.Logs
					}
					if response.Total != tt.wantTotal || len(items) != len(tt.wantIDs) {
						t.Fatalf("total=%d items=%d, want total=%d items=%d", response.Total, len(items), tt.wantTotal, len(tt.wantIDs))
					}
					remaining := make(map[string]int, len(tt.wantIDs))
					for _, requestID := range tt.wantIDs {
						if endpoint == "/logs" && len(requestID) > 8 {
							requestID = requestID[len(requestID)-8:]
						}
						remaining[requestID]++
					}
					seen := make(map[string]bool, len(items))
					for _, item := range items {
						if remaining[item.RequestID] == 0 {
							t.Fatalf("unexpected request_id %q", item.RequestID)
						}
						remaining[item.RequestID]--
						if len(item.ID) == 0 || seen[string(item.ID)] {
							t.Fatalf("missing or duplicate record id %s", item.ID)
						}
						seen[string(item.ID)] = true
					}
				})
			}
		})
	}
}

func TestDownloadRequestLogByIDUsesRequestIDOnlyForFileMatch(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	dir := t.TempDir()
	t.Chdir(dir)
	if errMkdir := os.Mkdir("logs", 0o755); errMkdir != nil {
		t.Fatalf("mkdir logs: %v", errMkdir)
	}
	content := "request log content\n"
	requestID := "018f3a5b-1234-7abc-def0-12345678abcd"
	fileName := "10.0.0.9-v1-responses-2026-05-29T010203-" + requestID + ".log"
	if errWrite := os.WriteFile(filepath.Join("logs", fileName), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write request log: %v", errWrite)
	}
	collidingFileName := "10.0.0.9-v1-responses-2026-05-29T010204-018f3a5b-5678-7abc-def0-99995678abcd.log"
	if errWrite := os.WriteFile(filepath.Join("logs", collidingFileName), []byte("another request\n"), 0o644); errWrite != nil {
		t.Fatalf("write colliding request log: %v", errWrite)
	}
	uniqueFileName := "10.0.0.9-v1-responses-2026-05-29T010205-018f3a5b-1234-7abc-def0-1234f0baf00d.log"
	if errWrite := os.WriteFile(filepath.Join("logs", uniqueFileName), []byte("unique request\n"), 0o644); errWrite != nil {
		t.Fatalf("write unique request log: %v", errWrite)
	}

	now := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	// The later filename has an older modification time.
	if errChtimes := os.Chtimes(filepath.Join("logs", fileName), now, now); errChtimes != nil {
		t.Fatalf("set request log modification time: %v", errChtimes)
	}
	older := now.Add(-time.Minute)
	if errChtimes := os.Chtimes(filepath.Join("logs", collidingFileName), older, older); errChtimes != nil {
		t.Fatalf("set colliding log modification time: %v", errChtimes)
	}
	record := cluster.AppLogRecord{
		Timestamp: now,
		ClientIP:  "10.0.0.5",
		RequestID: requestID,
		HomeIP:    "192.0.2.10",
		Level:     "info",
		Line:      "line",
		CreatedAt: now,
	}
	if errCreate := db.Create(&record).Error; errCreate != nil {
		t.Fatalf("create log: %v", errCreate)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/request-log-by-id/:id", handler.DownloadRequestLogByID)

	for _, tt := range []struct {
		name      string
		requestID string
		status    int
		body      string
	}{
		{name: "full UUID", requestID: requestID, status: http.StatusOK, body: content},
		{name: "other full UUID with same suffix", requestID: "018f3a5b-5678-7abc-def0-99995678abcd", status: http.StatusOK, body: "another request\n"},
		{name: "short ID selects most recently modified file", requestID: "5678abcd", status: http.StatusOK, body: content},
		{name: "unique short ID", requestID: "f0baf00d", status: http.StatusOK, body: "unique request\n"},
		{name: "missing short ID", requestID: "deadbeef", status: http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/request-log-by-id/"+tt.requestID+"?home_ip=192.0.2.10", nil)
			engine.ServeHTTP(resp, req)

			if resp.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", resp.Code, tt.status, resp.Body.String())
			}
			if tt.status != http.StatusOK {
				return
			}
			if got := resp.Body.String(); got != tt.body {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
			if got := resp.Header().Get("Content-Disposition"); got == "" {
				t.Fatal("Content-Disposition is empty")
			}
		})
	}
}

func TestDownloadRequestLogByIDWithoutHomeIPUsesLocalNode(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	dir := t.TempDir()
	t.Chdir(dir)
	if errMkdir := os.Mkdir("logs", 0o755); errMkdir != nil {
		t.Fatalf("mkdir logs: %v", errMkdir)
	}
	content := "local request log without home ip\n"
	fileName := "10.0.0.9-v1-responses-2026-05-29T010203-req-local.log"
	if errWrite := os.WriteFile(filepath.Join("logs", fileName), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write request log: %v", errWrite)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/request-log-by-id/:id", handler.DownloadRequestLogByID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-log-by-id/req-local", nil)
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Body.String(); got != content {
		t.Fatalf("body = %q, want %q", got, content)
	}
}

func TestDownloadRequestLogByIDFallsBackToFilesystemWithoutDatabaseRecord(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	dir := t.TempDir()
	t.Chdir(dir)
	if errMkdir := os.Mkdir("logs", 0o755); errMkdir != nil {
		t.Fatalf("mkdir logs: %v", errMkdir)
	}
	content := "filesystem only request log\n"
	fileName := "10.0.0.5-v1-responses-2026-05-29T010203-req-2.log"
	if errWrite := os.WriteFile(filepath.Join("logs", fileName), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write request log: %v", errWrite)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/request-log-by-id/:id", handler.DownloadRequestLogByID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-log-by-id/req-2?home_ip=192.0.2.10", nil)
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Body.String(); got != content {
		t.Fatalf("body = %q, want %q", got, content)
	}
}

func TestDownloadRequestLogByIDReturnsNotFoundWhenDatabaseRecordExistsButFileMissing(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	dir := t.TempDir()
	t.Chdir(dir)
	if errMkdir := os.Mkdir("logs", 0o755); errMkdir != nil {
		t.Fatalf("mkdir logs: %v", errMkdir)
	}

	now := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	record := cluster.AppLogRecord{
		Timestamp: now,
		ClientIP:  "10.0.0.5",
		RequestID: "req-deleted",
		HomeIP:    "192.0.2.10",
		Level:     "info",
		Line:      "line",
		CreatedAt: now,
	}
	if errCreate := db.Create(&record).Error; errCreate != nil {
		t.Fatalf("create log: %v", errCreate)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "192.0.2.10", 0)
	engine := gin.New()
	engine.GET("/request-log-by-id/:id", handler.DownloadRequestLogByID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-log-by-id/req-deleted?home_ip=192.0.2.10", nil)
	engine.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestDownloadRequestLogByIDForwardsToTargetHomeOverMTLS(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()
	repo := cluster.NewRepository(db)

	targetTLSConfig, errTargetTLS := repo.EnsureClusterCertificates(context.Background(), "127.0.0.1")
	if errTargetTLS != nil {
		t.Fatalf("EnsureClusterCertificates target: %v", errTargetTLS)
	}
	currentTLSConfig, errCurrentTLS := repo.EnsureClusterCertificates(context.Background(), "127.0.0.2")
	if errCurrentTLS != nil {
		t.Fatalf("EnsureClusterCertificates current: %v", errCurrentTLS)
	}

	dir := t.TempDir()
	t.Chdir(dir)
	if errMkdir := os.Mkdir("logs", 0o755); errMkdir != nil {
		t.Fatalf("mkdir logs: %v", errMkdir)
	}
	content := "remote request log\n"
	fileName := "10.0.0.5-v1-responses-2026-05-29T010203-req-remote.log"
	if errWrite := os.WriteFile(filepath.Join("logs", fileName), []byte(content), 0o644); errWrite != nil {
		t.Fatalf("write request log: %v", errWrite)
	}

	now := time.Date(2026, 5, 29, 1, 2, 3, 0, time.UTC)
	record := cluster.AppLogRecord{
		Timestamp: now,
		ClientIP:  "10.0.0.5",
		RequestID: "req-remote",
		HomeIP:    "127.0.0.1",
		Level:     "info",
		Line:      "line",
		CreatedAt: now,
	}
	if errCreate := db.Create(&record).Error; errCreate != nil {
		t.Fatalf("create log: %v", errCreate)
	}

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen target: %v", errListen)
	}
	targetPort := listener.Addr().(*net.TCPAddr).Port
	heartbeatAt := time.Now().UTC()
	if errCreateNode := db.Create(&cluster.ClusterNodeRecord{
		IP:         "127.0.0.1",
		Port:       targetPort,
		StartedAt:  heartbeatAt,
		LastSeenAt: heartbeatAt,
	}).Error; errCreateNode != nil {
		t.Fatalf("create target node: %v", errCreateNode)
	}

	targetHandler := NewHandler(repo, nil, "127.0.0.1", targetPort)
	targetEngine := gin.New()
	targetHeadersCh := make(chan http.Header, 1)
	targetEngine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Accept-Ranges", "bytes")
		c.Header("ETag", `"remote-log-etag"`)
		c.Header("X-Internal-Node", "target-home")
		targetHeadersCh <- c.Request.Header.Clone()
		c.Next()
	})
	targetEngine.GET("/v0/cluster/request-log-by-id/:id", targetHandler.DownloadLocalRequestLogByID)
	targetServer := &http.Server{Handler: targetEngine}
	t.Cleanup(func() {
		_ = targetServer.Close()
	})
	go func() {
		_ = targetServer.Serve(tls.NewListener(listener, targetTLSConfig))
	}()

	currentHandler := NewHandler(repo, nil, "127.0.0.2", 0)
	currentHandler.SetForwardTLSConfig(currentTLSConfig)
	currentEngine := gin.New()
	currentEngine.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Next()
	})
	currentEngine.GET("/request-log-by-id/:id", currentHandler.DownloadRequestLogByID)

	resp := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/request-log-by-id/req-remote?home_ip=127.0.0.1", nil)
	req.Header.Set("Authorization", "Bearer management-secret")
	req.Header.Set("Cookie", "management_session=secret")
	req.Header.Set("X-Management-Key", "secret")
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req.Header.Set("If-None-Match", `"request-log-etag"`)
	currentEngine.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Body.String(); got != content {
		t.Fatalf("body = %q, want %q", got, content)
	}
	assertSingleHeaderValue(t, resp, "Access-Control-Allow-Origin", "*")
	assertSingleHeaderValue(t, resp, "Access-Control-Allow-Methods", "GET, OPTIONS")
	assertSingleHeaderValue(t, resp, "Access-Control-Allow-Headers", "*")
	assertSingleHeaderValue(t, resp, "Accept-Ranges", "bytes")
	assertSingleHeaderValue(t, resp, "ETag", `"remote-log-etag"`)
	if got := resp.Header().Get("X-Internal-Node"); got != "" {
		t.Fatalf("forwarded X-Internal-Node response header = %q, want empty", got)
	}

	var targetHeaders http.Header
	select {
	case targetHeaders = <-targetHeadersCh:
	case <-time.After(time.Second):
		t.Fatal("target home did not receive forwarded request")
	}
	if got := targetHeaders.Get("Authorization"); got != "" {
		t.Fatalf("forwarded Authorization header = %q, want empty", got)
	}
	if got := targetHeaders.Get("Cookie"); got != "" {
		t.Fatalf("forwarded Cookie header = %q, want empty", got)
	}
	if got := targetHeaders.Get("X-Management-Key"); got != "" {
		t.Fatalf("forwarded X-Management-Key header = %q, want empty", got)
	}
	if got := targetHeaders.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("forwarded X-Forwarded-For header = %q, want empty", got)
	}
	if got := targetHeaders.Get("If-None-Match"); got != `"request-log-etag"` {
		t.Fatalf("forwarded If-None-Match header = %q, want request-log-etag", got)
	}
}

func TestRequestLogTargetNodeUsesHomePortWhenProvided(t *testing.T) {
	db, cleanup := openManagementLogTestDB(t)
	defer cleanup()

	now := time.Now().UTC()
	records := []cluster.ClusterNodeRecord{
		{IP: "127.0.0.1", Port: 18327, StartedAt: now, LastSeenAt: now},
		{IP: "127.0.0.1", Port: 18328, StartedAt: now, LastSeenAt: now},
	}
	if errCreate := db.Create(&records).Error; errCreate != nil {
		t.Fatalf("create cluster nodes: %v", errCreate)
	}

	handler := NewHandler(cluster.NewRepository(db), nil, "127.0.0.1", 18327)
	target, errTarget := handler.requestLogTargetNode(context.Background(), "127.0.0.1", 18328)
	if errTarget != nil {
		t.Fatalf("requestLogTargetNode error = %v", errTarget)
	}
	if target == nil || target.Port != 18328 {
		t.Fatalf("target = %#v, want port 18328", target)
	}
}

func TestRequestLogForwardHeaderAllowlists(t *testing.T) {
	t.Parallel()

	requestAllowed := []string{
		"Range",
		"If-Range",
		"If-Match",
		"If-None-Match",
		"If-Modified-Since",
		"If-Unmodified-Since",
	}
	if len(requestLogForwardRequestHeaderAllowlist) != len(requestAllowed) {
		t.Fatalf("request allowlist size = %d, want %d", len(requestLogForwardRequestHeaderAllowlist), len(requestAllowed))
	}
	for _, key := range requestAllowed {
		if !shouldForwardRequestLogRequestHeader(key) {
			t.Fatalf("request header %s is not allowed", key)
		}
	}
	for _, key := range []string{"Authorization", "Cookie", "X-Forwarded-For", "Cache-Control", "Accept-Encoding"} {
		if shouldForwardRequestLogRequestHeader(key) {
			t.Fatalf("request header %s is unexpectedly allowed", key)
		}
	}

	responseAllowed := []string{
		"Content-Disposition",
		"Content-Type",
		"Content-Length",
		"Last-Modified",
		"ETag",
		"Accept-Ranges",
		"Content-Range",
	}
	if len(requestLogForwardResponseHeaderAllowlist) != len(responseAllowed) {
		t.Fatalf("response allowlist size = %d, want %d", len(requestLogForwardResponseHeaderAllowlist), len(responseAllowed))
	}
	for _, key := range responseAllowed {
		if !shouldForwardRequestLogResponseHeader(key) {
			t.Fatalf("response header %s is not allowed", key)
		}
	}
	for _, key := range []string{"Access-Control-Allow-Origin", "Set-Cookie", "X-Internal-Node", "Content-Encoding", "Cache-Control", "Server"} {
		if shouldForwardRequestLogResponseHeader(key) {
			t.Fatalf("response header %s is unexpectedly allowed", key)
		}
	}
}

func assertSingleHeaderValue(t *testing.T, resp *httptest.ResponseRecorder, key string, want string) {
	t.Helper()

	values := resp.Header().Values(key)
	if len(values) != 1 || values[0] != want {
		t.Fatalf("%s values = %#v, want [%q]", key, values, want)
	}
}

func openManagementLogTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()

	db, errOpen := cluster.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "home.db"))
	if errOpen != nil {
		t.Fatalf("OpenSQLite: %v", errOpen)
	}
	sqlDB, errDB := db.DB()
	if errDB != nil {
		t.Fatalf("DB: %v", errDB)
	}
	if errMigrate := cluster.AutoMigrate(db); errMigrate != nil {
		_ = sqlDB.Close()
		t.Fatalf("AutoMigrate: %v", errMigrate)
	}
	cleanup := func() {
		if errClose := sqlDB.Close(); errClose != nil {
			t.Errorf("close db: %v", errClose)
		}
	}
	return db, cleanup
}
