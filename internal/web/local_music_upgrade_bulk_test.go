package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestLocalQualityScanStateTracking(t *testing.T) {
	job := &localQualityScanState{running: true, total: 3}
	track := &localMusicTrack{ID: "abc", Name: "悬溺", Artist: "葛东琪", Ext: "mp3", SizeText: "2.3 MB"}

	job.addResult(track, 128, []localQualityOption{{
		Bitrate:     1411,
		Quality:     "FLAC · 1411 kbps",
		SourceLabel: "酷狗音乐",
		SizeText:    "32.1 MB",
	}})
	// 音质没有提升的候选不应出现在结果里，但仍然计入已扫描数量。
	job.addResult(track, 1411, []localQualityOption{{Bitrate: 1411, Quality: "FLAC · 1411 kbps"}})
	job.skipTrack()

	if job.scanned != 3 {
		t.Fatalf("scanned = %d, want 3", job.scanned)
	}
	if len(job.items) != 1 {
		t.Fatalf("items = %d, want 1", len(job.items))
	}
	item := job.items[0]
	if item.ID != "abc" || item.NextSource != "酷狗音乐" || item.NextQuality != "FLAC · 1411 kbps" {
		t.Fatalf("unexpected item: %+v", item)
	}
	if !item.Option.Improvement {
		t.Fatalf("expected improvement flag to be set")
	}
	if len(item.Options) != 1 || item.Options[0].Bitrate != 1411 {
		t.Fatalf("expected options to be stored: %+v", item.Options)
	}
	if item.CurrentQuality != "MP3 · 128 kbps" || item.CurrentSizeText != "2.3 MB" {
		t.Fatalf("unexpected current quality: %+v", item)
	}

	job.cancelled = true
	job.addResult(track, 128, []localQualityOption{{Bitrate: 3200}})
	job.skipTrack()
	if job.scanned != 3 || len(job.items) != 1 {
		t.Fatalf("cancelled job still changed state: scanned=%d items=%d", job.scanned, len(job.items))
	}
}

func TestLocalQualityBatchFinishItem(t *testing.T) {
	job := &localQualityBatchState{
		running: true,
		total:   2,
		items: []localQualityBatchItem{
			{LocalID: "a", Status: "pending"},
			{LocalID: "b", Status: "pending"},
		},
	}

	job.finishItem(0, "a", "a", "ok", "", "旧文件删除失败", "FLAC · 1411 kbps")
	if job.done != 1 || job.failed != 0 || !job.running {
		t.Fatalf("unexpected counters after first item: done=%d failed=%d running=%v", job.done, job.failed, job.running)
	}
	if job.items[0].NewQuality != "FLAC · 1411 kbps" || job.items[0].Warning != "旧文件删除失败" {
		t.Fatalf("unexpected first item: %+v", job.items[0])
	}

	job.finishItem(1, "b", "", "failed", "下载失败", "")
	if job.done != 2 || job.failed != 1 {
		t.Fatalf("unexpected counters after second item: done=%d failed=%d", job.done, job.failed)
	}
	if job.running || job.finishedAt.IsZero() {
		t.Fatalf("job should be finished: running=%v finishedAt=%v", job.running, job.finishedAt)
	}
}

func newLocalQualityTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/local_music/upgrade_batch", handleLocalQualityBatchStart)
	router.GET("/local_music/upgrade_batch/status", handleLocalQualityBatchStatus)
	router.GET("/local_music/upgrade_scan/status", handleLocalQualityScanStatus)
	router.POST("/local_music/upgrade_scan/cancel", handleLocalQualityScanCancel)
	return router
}

func performLocalQualityTestRequest(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHandleLocalQualityBatchStartValidation(t *testing.T) {
	router := newLocalQualityTestRouter()
	defer func() {
		localQualityBatchJob.mu.Lock()
		localQualityBatchJob.running = false
		localQualityBatchJob.total = 0
		localQualityBatchJob.done = 0
		localQualityBatchJob.failed = 0
		localQualityBatchJob.items = nil
		localQualityBatchJob.mu.Unlock()
	}()

	if rec := performLocalQualityTestRequest(router, http.MethodPost, "/local_music/upgrade_batch", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", rec.Code)
	}
	if rec := performLocalQualityTestRequest(router, http.MethodPost, "/local_music/upgrade_batch", `{"items":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty items status = %d, want 400", rec.Code)
	}

	localQualityBatchJob.mu.Lock()
	localQualityBatchJob.running = true
	localQualityBatchJob.mu.Unlock()

	rec := performLocalQualityTestRequest(router, http.MethodPost, "/local_music/upgrade_batch", `{"items":[{"local_id":"x","source":"netease"}]}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("running job status = %d, want 409", rec.Code)
	}
	localQualityBatchJob.mu.Lock()
	localQualityBatchJob.running = false
	localQualityBatchJob.mu.Unlock()

	rec = performLocalQualityTestRequest(router, http.MethodPost, "/local_music/upgrade_batch", `{"items":[{"local_id":"not-exists","source":"netease"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	statusPath := "/local_music/upgrade_batch/status"
	deadline := time.Now().Add(3 * time.Second)
	for {
		rec = performLocalQualityTestRequest(router, http.MethodGet, statusPath, "")
		var payload struct {
			Running bool                    `json:"running"`
			Total   int                     `json:"total"`
			Done    int                     `json:"done"`
			Failed  int                     `json:"failed"`
			Items   []localQualityBatchItem `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if !payload.Running {
			if payload.Total != 1 || payload.Done != 1 || payload.Failed != 1 {
				t.Fatalf("unexpected final state: %+v", payload)
			}
			if len(payload.Items) != 1 || payload.Items[0].Status != "failed" {
				t.Fatalf("unexpected item state: %+v", payload.Items)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch job did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleLocalQualityScanStatusAndCancel(t *testing.T) {
	router := newLocalQualityTestRouter()

	rec := performLocalQualityTestRequest(router, http.MethodGet, "/local_music/upgrade_scan/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	for _, key := range []string{"running", "total", "scanned", "found", "items"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("status payload missing %q: %v", key, payload)
		}
	}

	if rec := performLocalQualityTestRequest(router, http.MethodPost, "/local_music/upgrade_scan/cancel", ""); rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", rec.Code)
	}
}
