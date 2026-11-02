package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/guohuiyuan/go-music-dl/core"
)

// 本地音乐「一键音质提升」：后台扫描整库，列出所有存在更高音质版本的歌曲，
// 再按用户选择批量下载替换。
const (
	localQualityScanWorkers    = 3
	localQualityBatchBodyLimit = 4 << 20
	localQualityBatchMaxItems  = 500
)

type localQualityScanItem struct {
	ID              string               `json:"id"`
	Name            string               `json:"name"`
	Artist          string               `json:"artist"`
	CurrentQuality  string               `json:"current_quality"`
	CurrentSizeText string               `json:"current_size_text"`
	NextQuality     string               `json:"next_quality"`
	NextSource      string               `json:"next_source"`
	NextSizeText    string               `json:"next_size_text"`
	Option          localQualityOption   `json:"option"`
	Options         []localQualityOption `json:"options"`
}

type localQualityScanState struct {
	mu         sync.Mutex
	running    bool
	cancelled  bool
	total      int
	scanned    int
	items      []localQualityScanItem
	errMsg     string
	startedAt  time.Time
	finishedAt time.Time
}

var localQualityScanJob localQualityScanState

type localQualityBatchItem struct {
	LocalID     string `json:"local_id"`
	Name        string `json:"name"`
	Artist      string `json:"artist"`
	Source      string `json:"source"`
	SourceLabel string `json:"source_label"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Warning     string `json:"warning,omitempty"`
	NewQuality  string `json:"new_quality,omitempty"`
	OldID       string `json:"old_id,omitempty"`
	NewID       string `json:"new_id,omitempty"`
}

type localQualityBatchState struct {
	mu         sync.Mutex
	running    bool
	total      int
	done       int
	failed     int
	items      []localQualityBatchItem
	startedAt  time.Time
	finishedAt time.Time
}

var localQualityBatchJob localQualityBatchState

type localQualityBatchRequest struct {
	Items []localQualityUpgradeRequest `json:"items"`
}

func handleLocalQualityScanStart(c *gin.Context) {
	if !allowSameOriginWrite(c) {
		return
	}

	job := &localQualityScanJob
	force := c.Query("refresh") == "1"
	job.mu.Lock()
	if job.running {
		job.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "running"})
		return
	}
	// 十分钟内刚扫完的结果直接复用，避免每次打开弹窗都要重扫一遍曲库。
	if !force && !job.finishedAt.IsZero() && time.Since(job.finishedAt) < 10*time.Minute {
		job.mu.Unlock()
		c.JSON(http.StatusOK, gin.H{"status": "reused"})
		return
	}
	job.running = true
	job.cancelled = false
	job.total = 0
	job.scanned = 0
	job.items = nil
	job.errMsg = ""
	job.startedAt = time.Now()
	job.finishedAt = time.Time{}
	job.mu.Unlock()

	go runLocalQualityScan()
	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

func handleLocalQualityScanStatus(c *gin.Context) {
	job := &localQualityScanJob
	job.mu.Lock()
	defer job.mu.Unlock()

	items := append([]localQualityScanItem(nil), job.items...)
	c.JSON(http.StatusOK, gin.H{
		"running":     job.running,
		"cancelled":   job.cancelled,
		"total":       job.total,
		"scanned":     job.scanned,
		"found":       len(items),
		"items":       items,
		"error":       job.errMsg,
		"started_at":  job.startedAt,
		"finished_at": job.finishedAt,
	})
}

func handleLocalQualityScanCancel(c *gin.Context) {
	if !allowSameOriginWrite(c) {
		return
	}

	job := &localQualityScanJob
	job.mu.Lock()
	if job.running {
		job.cancelled = true
	}
	job.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (job *localQualityScanState) isCancelled() bool {
	job.mu.Lock()
	defer job.mu.Unlock()
	return job.cancelled
}

func (job *localQualityScanState) addResult(track *localMusicTrack, currentBitrate int, options []localQualityOption) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.cancelled {
		return
	}
	job.scanned++
	if len(options) == 0 {
		return
	}
	best := options[0]
	if best.Bitrate <= 0 || best.Bitrate <= currentBitrate {
		return
	}
	for i := range options {
		options[i].Improvement = options[i].Bitrate > currentBitrate
	}
	best.Improvement = true
	job.items = append(job.items, localQualityScanItem{
		ID:              track.ID,
		Name:            track.Name,
		Artist:          track.Artist,
		CurrentQuality:  localQualityText(track.Ext, currentBitrate),
		CurrentSizeText: track.SizeText,
		NextQuality:     best.Quality,
		NextSource:      best.SourceLabel,
		NextSizeText:    best.SizeText,
		Option:          best,
		Options:         options,
	})
}

func (job *localQualityScanState) skipTrack() {
	job.mu.Lock()
	defer job.mu.Unlock()
	if !job.cancelled {
		job.scanned++
	}
}

func runLocalQualityScan() {
	job := &localQualityScanJob
	tracks, _, _, err, _, _ := scanLocalMusicTracksCached(false)
	if err != nil {
		job.mu.Lock()
		job.running = false
		job.errMsg = err.Error()
		job.finishedAt = time.Now()
		job.mu.Unlock()
		return
	}

	job.mu.Lock()
	job.total = len(tracks)
	job.mu.Unlock()

	jobs := make(chan *localMusicTrack)
	var wg sync.WaitGroup
	for i := 0; i < localQualityScanWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for track := range jobs {
				if job.isCancelled() {
					continue
				}
				duration := localQualityTrackDuration(track, "")
				currentBitrate, _ := localQualityTrackQuality(track, duration)
				options := collectLocalQualityOptions(track, duration, currentBitrate)
				if len(options) == 0 {
					job.skipTrack()
					continue
				}
				job.addResult(track, currentBitrate, options)
			}
		}()
	}

	for _, track := range tracks {
		jobs <- track
	}
	close(jobs)
	wg.Wait()

	job.mu.Lock()
	job.running = false
	job.finishedAt = time.Now()
	job.mu.Unlock()
}

func handleLocalQualityBatchStart(c *gin.Context) {
	if !allowSameOriginWrite(c) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, localQualityBatchBodyLimit)
	var req localQualityBatchRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有需要升级的歌曲"})
		return
	}
	if len(req.Items) > localQualityBatchMaxItems {
		req.Items = req.Items[:localQualityBatchMaxItems]
	}

	job := &localQualityBatchJob
	job.mu.Lock()
	if job.running {
		job.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "已有升级任务正在执行"})
		return
	}

	items := make([]localQualityBatchItem, 0, len(req.Items))
	for _, item := range req.Items {
		source := strings.TrimSpace(item.Source)
		items = append(items, localQualityBatchItem{
			LocalID:     strings.TrimSpace(item.LocalID),
			Source:      source,
			SourceLabel: localQualitySourceLabel(source),
			Status:      "pending",
		})
	}

	job.running = true
	job.total = len(items)
	job.done = 0
	job.failed = 0
	job.items = items
	job.startedAt = time.Now()
	job.finishedAt = time.Time{}
	job.mu.Unlock()

	go runLocalQualityBatch(req.Items)
	c.JSON(http.StatusOK, gin.H{"status": "started", "total": len(items)})
}

func handleLocalQualityBatchStatus(c *gin.Context) {
	job := &localQualityBatchJob
	job.mu.Lock()
	defer job.mu.Unlock()

	items := append([]localQualityBatchItem(nil), job.items...)
	c.JSON(http.StatusOK, gin.H{
		"running":     job.running,
		"total":       job.total,
		"done":        job.done,
		"failed":      job.failed,
		"items":       items,
		"started_at":  job.startedAt,
		"finished_at": job.finishedAt,
	})
}

func localQualitySourceLabel(source string) string {
	if label := strings.TrimSpace(core.GetSourceDescription(source)); label != "" {
		return label
	}
	return source
}

func runLocalQualityBatch(requests []localQualityUpgradeRequest) {
	job := &localQualityBatchJob
	for i, req := range requests {
		job.mu.Lock()
		if i < len(job.items) {
			job.items[i].Source = strings.TrimSpace(req.Source)
			job.items[i].SourceLabel = localQualitySourceLabel(req.Source)
		}
		job.mu.Unlock()

		track, err := localMusicTrackByID(strings.TrimSpace(req.LocalID))
		if err != nil {
			job.finishItem(i, "", "", "failed", "本地音乐不存在或已不在下载目录内", "")
			continue
		}
		job.mu.Lock()
		if i < len(job.items) {
			job.items[i].Name = track.Name
			job.items[i].Artist = track.Artist
		}
		job.mu.Unlock()

		newTrack, warning, err := performLocalQualityUpgrade(track, strings.TrimSpace(req.Source), req.Song)
		if err != nil {
			job.finishItem(i, "", "", "failed", err.Error(), "")
			continue
		}

		newQuality := ""
		if newTrack != nil {
			bitrate := 0
			if newTrack.Extra != nil {
				bitrate, _ = strconv.Atoi(strings.TrimSpace(newTrack.Extra["bitrate"]))
			}
			if bitrate <= 0 && newTrack.Duration > 0 && newTrack.Size > 0 {
				bitrate = int(newTrack.Size * 8 / int64(newTrack.Duration) / 1000)
			}
			newQuality = localQualityText(newTrack.Ext, bitrate)
		}
		job.finishItem(i, track.ID, newTrack.ID, "ok", "", warning, newQuality)
	}
}

func (job *localQualityBatchState) finishItem(index int, oldID, newID, status, errMsg, warning string, qualities ...string) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if index >= 0 && index < len(job.items) {
		item := &job.items[index]
		item.Status = status
		item.Error = errMsg
		item.Warning = warning
		item.OldID = oldID
		item.NewID = newID
		if len(qualities) > 0 {
			item.NewQuality = qualities[0]
		}
	}
	job.done++
	if status != "ok" {
		job.failed++
	}
	if job.done >= job.total {
		job.running = false
		job.finishedAt = time.Now()
	}
}
