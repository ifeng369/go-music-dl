package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/guohuiyuan/go-music-dl/core"
	"github.com/guohuiyuan/music-lib/model"
)

// 本地音乐「音质替换」：列出各平台能匹配到的音源及其实测文件大小，
// 选中后下载对应版本并直接覆盖本地文件。
const (
	localQualityUpgradeBodyLimit = 128 << 10
	localQualityProbeTimeout     = 6 * time.Second
	localQualityMaxCandidates    = 3 // 每个平台最多尝试候选数（首选失败时顺延）
	localQualityProbeConcurrency = 4
	localUnknownArtist           = "未知歌手"

	// 直接覆盖本地文件属于破坏性操作，只接受几乎同名的版本，
	// 避免把 Live/Remix/翻唱当成原曲替换进来。
	localQualityMinNameSimilarity   = 0.85
	localQualityMinArtistSimilarity = 0.75
)

// localQualitySourceNames 是参与音质比对的平台，顺序即前端展示优先级。
// 汽水音乐需要本机客户端且依赖会员状态，不参与本地音质替换。
var localQualitySourceNames = []string{
	"netease", "qq", "kugou", "kuwo", "migu", "qianqian", "apple",
	"joox", "bilibili", "jamendo", "fivesing",
}

var localQualityHTTPClient = &http.Client{Timeout: localQualityProbeTimeout}

// 便于测试替换下载实现。
var localQualityDownloadSong = core.DownloadSongDataWithTemplate

type localQualityOption struct {
	Source      string     `json:"source"`
	SourceLabel string     `json:"source_label"`
	Quality     string     `json:"quality"`
	Tier        string     `json:"tier"`
	Ext         string     `json:"ext"`
	Bitrate     int        `json:"bitrate"`
	Size        int64      `json:"size"`
	SizeText    string     `json:"size_text"`
	Duration    int        `json:"duration"`
	Score       float64    `json:"score"`
	Improvement bool       `json:"improvement"`
	Song        model.Song `json:"song"`
}

type localQualityUpgradeRequest struct {
	LocalID string     `json:"local_id"`
	Source  string     `json:"source"`
	Song    model.Song `json:"song"`
}

func registerLocalMusicUpgradeRoutes(api *gin.RouterGroup) {
	api.GET("/local_music/quality_options", handleLocalQualityOptions)
	api.POST("/local_music/upgrade", handleLocalMusicUpgrade)
	api.POST("/local_music/upgrade_scan", handleLocalQualityScanStart)
	api.GET("/local_music/upgrade_scan/status", handleLocalQualityScanStatus)
	api.POST("/local_music/upgrade_scan/cancel", handleLocalQualityScanCancel)
	api.POST("/local_music/upgrade_batch", handleLocalQualityBatchStart)
	api.GET("/local_music/upgrade_batch/status", handleLocalQualityBatchStatus)
}

func handleLocalQualityOptions(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少本地音乐 ID"})
		return
	}

	track, err := localMusicTrackByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "本地音乐不存在或已不在下载目录内"})
		return
	}

	duration := localQualityTrackDuration(track, c.Query("duration"))
	currentBitrate, currentTier := localQualityTrackQuality(track, duration)

	sources := localQualitySources()
	sourceLabels := make(map[string]string, len(sources))
	for _, source := range sources {
		sourceLabels[source] = core.GetSourceDescription(source)
	}
	options := collectLocalQualityOptions(track, duration, currentBitrate)

	c.JSON(http.StatusOK, gin.H{
		"local": gin.H{
			"id":           track.ID,
			"name":         track.Name,
			"artist":       track.Artist,
			"album":        track.Album,
			"cover":        track.Cover,
			"ext":          track.Ext,
			"size":         track.Size,
			"size_text":    track.SizeText,
			"duration":     duration,
			"bitrate":      currentBitrate,
			"bitrate_text": localQualityBitrateText(currentBitrate),
			"tier":         currentTier,
			"tier_text":    localQualityTierText(currentTier),
			"rel_path":     track.RelPath,
		},
		"options":         options,
		"checked_sources": sources,
		"source_labels":   sourceLabels,
	})
}

// localQualitySources 返回当前可用的比对平台（缺少搜索或下载实现时跳过）。
func localQualitySources() []string {
	sources := make([]string, 0, len(localQualitySourceNames))
	for _, source := range localQualitySourceNames {
		if core.GetSearchFunc(source) == nil || core.GetDownloadFunc(source) == nil {
			continue
		}
		sources = append(sources, source)
	}
	return sources
}

// collectLocalQualityOptions 并发查询所有平台，按实测码率从高到低返回。
func collectLocalQualityOptions(track *localMusicTrack, duration int, currentBitrate int) []localQualityOption {
	sources := localQualitySources()
	sem := make(chan struct{}, localQualityProbeConcurrency)
	results := make(chan *localQualityOption, len(sources))
	var wg sync.WaitGroup
	for _, source := range sources {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if option := buildLocalQualityOption(src, track, duration); option != nil {
				results <- option
			}
		}(source)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	options := make([]localQualityOption, 0, len(sources))
	for option := range results {
		option.Improvement = currentBitrate > 0 && option.Bitrate > currentBitrate
		options = append(options, *option)
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Bitrate == options[j].Bitrate {
			return options[i].Size > options[j].Size
		}
		return options[i].Bitrate > options[j].Bitrate
	})
	return options
}

// buildLocalQualityOption 搜索指定平台并解析出可直接下载的真实地址，
// 再用 1 字节 Range 请求读出实际文件大小。
func buildLocalQualityOption(source string, track *localMusicTrack, duration int) *localQualityOption {
	searchFn := core.GetSearchFunc(source)
	downloadFn := core.GetDownloadFunc(source)
	if searchFn == nil || downloadFn == nil || track == nil {
		return nil
	}

	name, artist := localQualityTargetIdentity(track)
	if name == "" {
		return nil
	}
	keyword := name
	if artist != "" && artist != localUnknownArtist {
		keyword = name + " " + artist
	}

	candidates := searchSwitchSourceCandidates(source, searchFn, keyword, name, artist, duration)
	if len(candidates) == 0 {
		return nil
	}
	matched := candidates[:0]
	for _, candidate := range candidates {
		if !localQualityIsSameSong(name, artist, candidate.song.Name, candidate.song.Artist, duration, candidate.song.Duration) {
			continue
		}
		matched = append(matched, candidate)
	}
	if len(matched) == 0 {
		return nil
	}

	sortSwitchCandidates(matched)
	if len(matched) > localQualityMaxCandidates {
		matched = matched[:localQualityMaxCandidates]
	}
	for _, candidate := range matched {
		option := probeLocalQualityCandidate(source, downloadFn, candidate, duration)
		if option != nil {
			return option
		}
	}
	return nil
}

// localQualityTargetIdentity 在标签缺失时尝试从文件名「歌手 - 歌名」里补出检索词。
func localQualityTargetIdentity(track *localMusicTrack) (string, string) {
	name := strings.TrimSpace(track.Name)
	artist := strings.TrimSpace(track.Artist)
	titleMissing := containsString(track.Missing, "title") || name == ""
	artistMissing := artist == "" || artist == localUnknownArtist

	if titleMissing || artistMissing {
		base := strings.TrimSuffix(track.Filename, filepath.Ext(track.Filename))
		if left, right, ok := strings.Cut(base, " - "); ok {
			if artistMissing {
				artist = strings.TrimSpace(left)
			}
			if titleMissing {
				name = strings.TrimSpace(right)
			}
		}
	}
	if name == "" {
		name = strings.TrimSuffix(track.Filename, filepath.Ext(track.Filename))
	}
	if artist == "" {
		artist = localUnknownArtist
	}
	return name, artist
}

// localQualityVersionMarkers 用来识别「同一首歌的不同版本」。
// 覆盖本地文件属于破坏性操作，伴奏/现场/DJ 版一旦混进来就无法恢复，
// 因此要求本地文件与候选版本的版本标记必须一致。
var localQualityVersionMarkers = []string{
	"伴奏", "伴唱", "纯音乐", "instrumental", "karaoke", "remix", "混音", "翻唱",
	"cover", "live", "现场", "演唱会", "acoustic", "unplugged", "demo", "铃声",
	"片段", "加速", "降速", "慢速", "8d", "dj", "remaster", "重制", "合唱", "和声",
}

func localQualityHasVersionMarker(name string) bool {
	normalized := core.NormalizeText(name)
	if normalized == "" {
		return false
	}
	for _, marker := range localQualityVersionMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func localQualityIsSameSong(name, artist, otherName, otherArtist string, durationA, durationB int) bool {
	nameSim := core.SimilarityScore(core.NormalizeText(name), core.NormalizeText(otherName))
	if nameSim < localQualityMinNameSimilarity {
		return false
	}
	if localQualityHasVersionMarker(name) != localQualityHasVersionMarker(otherName) {
		return false
	}
	if artist != "" && artist != localUnknownArtist && strings.TrimSpace(otherArtist) != "" {
		if core.SimilarityScore(core.NormalizeText(artist), core.NormalizeText(otherArtist)) < localQualityMinArtistSimilarity {
			return false
		}
	}
	if durationA > 0 && durationB > 0 && !core.IsDurationClose(durationA, durationB) {
		return false
	}
	return true
}

func probeLocalQualityCandidate(source string, downloadFn func(*model.Song) (string, error), candidate switchCandidate, duration int) *localQualityOption {
	song := candidate.song
	song.Source = source
	if strings.TrimSpace(song.Name) == "" {
		return nil
	}

	urlStr, err := downloadFn(&song)
	if err != nil || strings.TrimSpace(urlStr) == "" {
		return nil
	}

	req, err := core.BuildSourceRequest("GET", urlStr, source, "bytes=0-1")
	if err != nil {
		return nil
	}
	resp, err := localQualityHTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil
	}

	size := localQualityResponseSize(resp)
	if size <= 0 {
		size = song.Size
	}
	if size <= 0 {
		return nil
	}

	ext := localQualitySongExt(song, urlStr, resp.Header.Get("Content-Type"))
	if ext == "" {
		return nil
	}

	bitrate := 0
	if duration > 0 {
		bitrate = int(size * 8 / int64(duration) / 1000)
	}
	if bitrate <= 0 {
		bitrate = song.Bitrate
	}
	tier := localQualityTier(ext, bitrate)

	return &localQualityOption{
		Source:      source,
		SourceLabel: core.GetSourceDescription(source),
		Quality:     localQualityText(ext, bitrate),
		Tier:        tier,
		Ext:         ext,
		Bitrate:     bitrate,
		Size:        size,
		SizeText:    core.FormatSize(size),
		Duration:    song.Duration,
		Score:       candidate.score,
		Song:        song,
	}
}

func localQualityResponseSize(resp *http.Response) int64 {
	if cr := strings.TrimSpace(resp.Header.Get("Content-Range")); cr != "" {
		if idx := strings.LastIndex(cr, "/"); idx >= 0 {
			if size, err := strconv.ParseInt(strings.TrimSpace(cr[idx+1:]), 10, 64); err == nil && size > 0 {
				return size
			}
		}
	}
	if resp.ContentLength > 0 {
		return resp.ContentLength
	}
	return 0
}

func localQualitySongExt(song model.Song, rawURL string, contentType string) string {
	if ext := normalizeLocalAudioExt(song.Ext); ext != "" {
		return ext
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		if ext := normalizeLocalAudioExt(filepath.Ext(parsed.Path)); ext != "" {
			return ext
		}
	}
	return normalizeLocalAudioExt(core.DetectAudioExtByContentType(contentType))
}

func normalizeLocalAudioExt(ext string) string {
	ext = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	if _, ok := localMusicAudioExts["."+ext]; !ok {
		return ""
	}
	return ext
}

func localQualityTrackDuration(track *localMusicTrack, queryDuration string) int {
	if track == nil {
		return 0
	}
	if track.Duration > 0 {
		return track.Duration
	}
	if seconds, _ := strconv.Atoi(strings.TrimSpace(track.Extra["duration"])); seconds > 0 {
		return seconds
	}
	if probe, err := probeLocalMusicTrack(track); err == nil && probe != nil {
		applyLocalProbeResult(track, probe)
		if track.Duration > 0 {
			return track.Duration
		}
		if probe.Duration > 0 {
			return probe.Duration
		}
	}
	seconds, _ := strconv.Atoi(strings.TrimSpace(queryDuration))
	return seconds
}

func localQualityTrackQuality(track *localMusicTrack, duration int) (int, string) {
	if track == nil {
		return 0, "unknown"
	}
	bitrate, _ := strconv.Atoi(strings.TrimSpace(track.Extra["bitrate"]))
	if bitrate <= 0 && duration > 0 && track.Size > 0 {
		bitrate = int(track.Size * 8 / int64(duration) / 1000)
	}
	return bitrate, localQualityTier(track.Ext, bitrate)
}

func localQualityTier(ext string, bitrate int) string {
	switch {
	case bitrate >= 1500:
		return "hires"
	case strings.EqualFold(ext, "flac"):
		return "lossless"
	case bitrate >= 256:
		return "high"
	case bitrate > 0:
		return "standard"
	default:
		return "unknown"
	}
}

func localQualityTierText(tier string) string {
	switch tier {
	case "hires":
		return "Hi-Res"
	case "lossless":
		return "无损"
	case "high":
		return "高品质"
	case "standard":
		return "标准"
	default:
		return "未知"
	}
}

func localQualityBitrateText(bitrate int) string {
	if bitrate <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d kbps", bitrate)
}

func localQualityText(ext string, bitrate int) string {
	name := strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(ext), "."))
	if name == "" {
		name = localQualityBitrateText(bitrate)
	}
	if bitrate <= 0 {
		return name
	}
	if name == "" || name == "-" {
		return localQualityBitrateText(bitrate)
	}
	return fmt.Sprintf("%s · %d kbps", name, bitrate)
}

func isLocalQualitySource(source string) bool {
	for _, candidate := range localQualitySourceNames {
		if candidate == source {
			return true
		}
	}
	return false
}

// localQualityStatusError 携带建议返回的 HTTP 状态码，便于批量任务直接取用错误文案。
type localQualityStatusError struct {
	Status  int
	Message string
}

func (e *localQualityStatusError) Error() string { return e.Message }

func localQualityFail(status int, message string) error {
	return &localQualityStatusError{Status: status, Message: message}
}

// performLocalQualityUpgrade 下载指定音源的版本并原地替换本地文件。
func performLocalQualityUpgrade(track *localMusicTrack, source string, song model.Song) (*localMusicTrack, string, error) {
	if !isLocalQualitySource(source) {
		return nil, "", localQualityFail(http.StatusBadRequest, "不支持的音乐来源")
	}

	song.Source = source
	song.ID = strings.TrimSpace(song.ID)
	if song.ID == "" {
		return nil, "", localQualityFail(http.StatusBadRequest, "缺少音源 ID")
	}
	if strings.TrimSpace(song.Name) == "" {
		song.Name = track.Name
	}
	if strings.TrimSpace(song.Artist) == "" {
		song.Artist = track.Artist
	}
	if song.Duration <= 0 {
		song.Duration = track.Duration
	}

	// 防止把完全不相干的歌曲写进本地文件。
	targetName, targetArtist := localQualityTargetIdentity(track)
	if !localQualityIsSameSong(targetName, targetArtist, song.Name, song.Artist, track.Duration, song.Duration) {
		return nil, "", localQualityFail(http.StatusBadRequest, "所选音源与本地歌曲不匹配")
	}

	result, err := localQualityDownloadSong(&song, true, true, core.DefaultDownloadFilenameTemplate)
	if err != nil {
		return nil, "", localQualityFail(http.StatusBadGateway, "下载失败: "+err.Error())
	}
	if result == nil || len(result.Data) < 1024 {
		return nil, "", localQualityFail(http.StatusBadGateway, "下载到的音频文件无效")
	}

	ext := normalizeLocalAudioExt(result.Ext)
	if ext == "" {
		ext = normalizeLocalAudioExt(track.Ext)
	}
	if ext == "" {
		ext = "mp3"
	}

	oldPath := track.absPath
	oldID := track.ID
	dir := filepath.Dir(oldPath)
	base := strings.TrimSuffix(track.Filename, filepath.Ext(track.Filename))
	filename := base + "." + ext
	targetPath := filepath.Join(dir, filename)
	if targetPath != oldPath {
		if _, statErr := os.Stat(targetPath); statErr == nil {
			targetPath = uniqueLocalMusicPath(dir, filename)
		}
	}

	if err := writeLocalMusicFileAtomic(targetPath, result.Data); err != nil {
		return nil, "", localQualityFail(http.StatusInternalServerError,
			"替换失败，歌曲可能正在播放，请暂停后重试（"+err.Error()+"）")
	}

	warning := ""
	if targetPath != oldPath {
		if err := os.Remove(oldPath); err != nil {
			warning = "新文件已写入，但旧文件删除失败，请手动清理：" + err.Error()
		}
	}

	rootAbs, err := filepath.Abs(localMusicDownloadDir())
	if err != nil {
		return nil, "", localQualityFail(http.StatusInternalServerError, "文件已替换，但下载目录不可用："+err.Error())
	}
	newTrack, buildErr := buildLocalMusicTrack(rootAbs, targetPath)
	if buildErr != nil {
		newTrack, buildErr = buildLocalMusicTrackFallback(rootAbs, targetPath)
	}
	if buildErr != nil {
		return nil, "", localQualityFail(http.StatusInternalServerError, "文件已替换，但重新读取曲目信息失败："+buildErr.Error())
	}
	localQualityRestoreMetadata(newTrack, track)
	if newTrack.Duration <= 0 && song.Duration > 0 {
		newTrack.Duration = song.Duration
		if newTrack.Extra == nil {
			newTrack.Extra = make(map[string]string)
		}
		newTrack.Extra["duration"] = strconv.Itoa(song.Duration)
	}
	if newTrack.Extra != nil && newTrack.Size > 0 && newTrack.Duration > 0 && newTrack.Extra["bitrate"] == "" {
		newTrack.Extra["bitrate"] = strconv.Itoa(int(newTrack.Size * 8 / int64(newTrack.Duration) / 1000))
	}

	upsertLocalMusicIndexRow(newTrack)
	if newTrack.ID != oldID {
		deleteLocalMusicIndexRow(oldID)
	}
	_ = core.ForgetDownloadedSong(track.Name, track.Artist, track.RelPath)
	_ = core.SaveDownloadDedupEntry(newTrack.Name, newTrack.Artist, newTrack.RelPath)
	if updateErr := localQualityUpdateSavedSongs(oldID, newTrack); updateErr != nil && warning == "" {
		warning = "收藏记录更新失败：" + updateErr.Error()
	}
	invalidateLocalMusicScanCache()
	cacheLocalMusicTrack(rootAbs, newTrack)
	return newTrack, warning, nil
}

func handleLocalMusicUpgrade(c *gin.Context) {
	if !allowSameOriginWrite(c) {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, localQualityUpgradeBodyLimit)
	var req localQualityUpgradeRequest
	if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}

	source := strings.TrimSpace(req.Source)
	if !isLocalQualitySource(source) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的音源"})
		return
	}

	track, err := localMusicTrackByID(strings.TrimSpace(req.LocalID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "本地音乐不存在或已不在下载目录内"})
		return
	}

	newTrack, warning, err := performLocalQualityUpgrade(track, source, req.Song)
	if err != nil {
		status := http.StatusInternalServerError
		var statusErr *localQualityStatusError
		if errors.As(err, &statusErr) {
			status = statusErr.Status
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":   "ok",
		"old_id":   track.ID,
		"track":    newTrack,
		"filename": newTrack.Filename,
		"warning":  warning,
	})
}

func writeLocalMusicFileAtomic(targetPath string, data []byte) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".local-quality-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, 0644)
	return os.Rename(tmpName, targetPath)
}

func localQualityRestoreMetadata(next *localMusicTrack, previous *localMusicTrack) {
	if next == nil || previous == nil {
		return
	}
	if containsString(next.Missing, "title") && strings.TrimSpace(previous.Name) != "" {
		next.Name = previous.Name
		next.Missing = removeString(next.Missing, "title")
	}
	if containsString(next.Missing, "artist") && strings.TrimSpace(previous.Artist) != "" {
		next.Artist = previous.Artist
		next.Missing = removeString(next.Missing, "artist")
	}
	if containsString(next.Missing, "album") && strings.TrimSpace(previous.Album) != "" {
		next.Album = previous.Album
		next.Missing = removeString(next.Missing, "album")
	}
}

// localQualityUpdateSavedSongs 同步收藏歌单中对该本地文件的引用，
// 换扩展名后 ID 会变化，需要一起改写，否则歌单里会显示成失效条目。
func localQualityUpdateSavedSongs(oldID string, track *localMusicTrack) error {
	if db == nil || track == nil {
		return nil
	}
	extraJSON, err := json.Marshal(track.Extra)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{
		"song_id":  track.ID,
		"extra":    string(extraJSON),
		"name":     track.Name,
		"artist":   track.Artist,
		"cover":    track.Cover,
		"duration": track.Duration,
	}
	sources := []string{localMusicSource, legacyLocalMusicSource}
	return db.Model(&SavedSong{}).
		Where("song_id = ? AND source IN ?", oldID, sources).
		Updates(updates).Error
}
