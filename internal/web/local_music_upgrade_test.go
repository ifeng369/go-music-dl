package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/guohuiyuan/go-music-dl/core"
	"github.com/guohuiyuan/music-lib/model"
)

func TestLocalQualityHelpers(t *testing.T) {
	if got := normalizeLocalAudioExt(".FLAC"); got != "flac" {
		t.Fatalf("normalizeLocalAudioExt(.FLAC) = %q, want flac", got)
	}
	if got := normalizeLocalAudioExt("exe"); got != "" {
		t.Fatalf("normalizeLocalAudioExt(exe) = %q, want empty", got)
	}

	cases := []struct {
		ext     string
		bitrate int
		tier    string
	}{
		{"flac", 1411, "lossless"},
		{"flac", 2304, "hires"},
		{"mp3", 320, "high"},
		{"mp3", 128, "standard"},
		{"mp3", 0, "unknown"},
	}
	for _, tc := range cases {
		if got := localQualityTier(tc.ext, tc.bitrate); got != tc.tier {
			t.Fatalf("localQualityTier(%q, %d) = %q, want %q", tc.ext, tc.bitrate, got, tc.tier)
		}
	}

	if got := localQualityTierText("hires"); got != "Hi-Res" {
		t.Fatalf("localQualityTierText(hires) = %q", got)
	}
	if got := localQualityText("flac", 1411); got != "FLAC · 1411 kbps" {
		t.Fatalf("localQualityText = %q", got)
	}
	if got := localQualityBitrateText(0); got != "-" {
		t.Fatalf("localQualityBitrateText(0) = %q", got)
	}

	if !isLocalQualitySource("netease") || isLocalQualitySource("soda") || isLocalQualitySource("local") {
		t.Fatalf("isLocalQualitySource 判断异常")
	}

	song := model.Song{Ext: "mp3"}
	if got := localQualitySongExt(song, "https://cdn.example.com/a/b/file.flac?token=1", ""); got != "mp3" {
		t.Fatalf("localQualitySongExt 应优先使用 song.Ext，got %q", got)
	}
	if got := localQualitySongExt(model.Song{}, "https://cdn.example.com/a/b/file.flac?token=1", ""); got != "flac" {
		t.Fatalf("localQualitySongExt 应从 URL 后缀识别，got %q", got)
	}
	if got := localQualitySongExt(model.Song{}, "https://cdn.example.com/stream", "audio/flac"); got != "flac" {
		t.Fatalf("localQualitySongExt 应从 Content-Type 识别，got %q", got)
	}
}

func TestWriteLocalMusicFileAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "song.flac")
	if err := writeLocalMusicFileAtomic(target, []byte("new-audio")); err != nil {
		t.Fatalf("writeLocalMusicFileAtomic: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new-audio" {
		t.Fatalf("写入内容不符: %q", string(data))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("临时文件未清理，目录内仍有 %d 个条目", len(entries))
	}
}

func TestLocalQualityRestoreMetadata(t *testing.T) {
	previous := &localMusicTrack{Name: "旧歌名", Artist: "旧歌手", Album: "旧专辑"}
	next := &localMusicTrack{
		Name:    "文件名",
		Artist:  "未知歌手",
		Missing: []string{"title", "artist", "album"},
	}
	localQualityRestoreMetadata(next, previous)
	if next.Name != "旧歌名" || next.Artist != "旧歌手" || next.Album != "旧专辑" {
		t.Fatalf("元数据回填失败: %+v", next)
	}
	if len(next.Missing) != 0 {
		t.Fatalf("Missing 应被清空: %v", next.Missing)
	}
}

func TestHandleLocalMusicUpgradeReplacesFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	oldDir := localMusicDownloadDirProvider
	localMusicDownloadDirProvider = func() string { return dir }
	t.Cleanup(func() { localMusicDownloadDirProvider = oldDir })

	fileName := "旧歌曲.mp3"
	oldPath := filepath.Join(dir, fileName)
	oldData := append([]byte("ID3"), bytes.Repeat([]byte{0x00}, 3000)...)
	if err := os.WriteFile(oldPath, oldData, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	oldDownload := localQualityDownloadSong
	var captured model.Song
	localQualityDownloadSong = func(song *model.Song, withCover bool, withLyrics bool, template string) (*core.DownloadedSong, error) {
		captured = *song
		data := append([]byte("fLaC"), bytes.Repeat([]byte{0x11}, 4096)...)
		return &core.DownloadedSong{Data: data, Ext: "flac"}, nil
	}
	t.Cleanup(func() { localQualityDownloadSong = oldDownload })

	id := encodeLocalMusicID(fileName)
	body, err := json.Marshal(localQualityUpgradeRequest{
		LocalID: id,
		Source:  "netease",
		Song: model.Song{
			ID:       "123456",
			Name:     "旧歌曲",
			Artist:   "未知歌手",
			Duration: 200,
		},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	engine := gin.New()
	registerLocalMusicUpgradeRoutes(engine.Group("/music"))
	req := httptest.NewRequest(http.MethodPost, "/music/local_music/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		Status   string          `json:"status"`
		OldID    string          `json:"old_id"`
		Track    localMusicTrack `json:"track"`
		Filename string          `json:"filename"`
		Warning  string          `json:"warning"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Status != "ok" || payload.OldID != id {
		t.Fatalf("payload 异常: %+v", payload)
	}
	if payload.Track.Ext != "flac" || payload.Filename != "旧歌曲.flac" {
		t.Fatalf("track 异常: %+v", payload.Track)
	}
	if captured.Source != "netease" || captured.ID != "123456" {
		t.Fatalf("下载参数异常: %+v", captured)
	}

	if _, err := os.Stat(filepath.Join(dir, "旧歌曲.flac")); err != nil {
		t.Fatalf("新文件不存在: %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("旧文件应被删除, err = %v", err)
	}
}

func TestHandleLocalMusicUpgradeRejectsUnknownSource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body, _ := json.Marshal(localQualityUpgradeRequest{LocalID: "x", Source: "local"})
	engine := gin.New()
	registerLocalMusicUpgradeRoutes(engine.Group("/music"))
	req := httptest.NewRequest(http.MethodPost, "/music/local_music/upgrade", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestLocalQualityIsSameSong(t *testing.T) {
	cases := []struct {
		label                  string
		name, artist           string
		otherName, otherArtist string
		durationA, durationB   int
		want                   bool
	}{
		{"完全一致", "悬溺", "葛东琪", "悬溺", "葛东琪", 197, 197, true},
		{"同名不同歌手", "悬溺", "葛东琪", "悬溺", "李末晚", 197, 197, false},
		{"改名版本", "悬溺", "葛东琪", "悬溺 (DJ添添版)", "葛东琪", 197, 197, false},
		{"本地歌手未知", "悬溺", "未知歌手", "悬溺", "其他人", 197, 197, true},
		{"时长差异过大", "悬溺", "葛东琪", "悬溺", "葛东琪", 197, 260, false},
		{"时长未知", "悬溺", "葛东琪", "悬溺", "葛东琪", 0, 0, true},
		{"候选为伴奏版", "Never Be the Same", "Camila Cabello", "Never Be The Same (伴奏)", "Camila Cabello", 247, 247, false},
		{"双方都是 Live 版", "悬溺 (Live)", "葛东琪", "悬溺 (Live)", "葛东琪", 197, 197, true},
		{"本地伴奏换原曲", "悬溺 (伴奏)", "葛东琪", "悬溺", "葛东琪", 197, 197, false},
	}
	for _, tc := range cases {
		if got := localQualityIsSameSong(tc.name, tc.artist, tc.otherName, tc.otherArtist, tc.durationA, tc.durationB); got != tc.want {
			t.Fatalf("%s: localQualityIsSameSong = %v, want %v", tc.label, got, tc.want)
		}
	}
}

func TestLocalQualityTargetIdentity(t *testing.T) {
	track := &localMusicTrack{
		Name:     "葛东琪 - 悬溺",
		Artist:   "未知歌手",
		Filename: "葛东琪 - 悬溺.mp3",
		Missing:  []string{"title", "artist"},
	}
	name, artist := localQualityTargetIdentity(track)
	if name != "悬溺" || artist != "葛东琪" {
		t.Fatalf("从文件名解析失败: name=%q artist=%q", name, artist)
	}

	track = &localMusicTrack{Name: "悬溺", Artist: "葛东琪", Filename: "a.mp3"}
	name, artist = localQualityTargetIdentity(track)
	if name != "悬溺" || artist != "葛东琪" {
		t.Fatalf("已有标签时不应改动: name=%q artist=%q", name, artist)
	}
}
