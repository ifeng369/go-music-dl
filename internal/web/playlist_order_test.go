package web

import (
	"testing"

	"github.com/guohuiyuan/music-lib/model"
)

func TestLoadPlaylistSourceTabsKeepsPlaylistOrder(t *testing.T) {
	tabs, errMsg := loadPlaylistSourceTabs([]string{"kuwo"}, func(string) ([]model.Playlist, error) {
		return []model.Playlist{
			{ID: "a", Name: "小歌单", TrackCount: 3},
			{ID: "b", Name: "大歌单", TrackCount: 30},
			{ID: "c", Name: "中歌单", TrackCount: 12},
			{ID: "d", Name: "同名A", TrackCount: 12},
		}, nil
	})
	if errMsg != "" {
		t.Fatalf("unexpected error message: %s", errMsg)
	}
	if len(tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(tabs))
	}
	got := tabs[0].Playlists
	// 歌单卡片保持平台返回的原始顺序，不按曲目数重排。
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("playlists = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("playlist[%d] = %s, want %s (order: %v)", i, got[i].ID, want[i], ids(got))
		}
	}
	if tabs[0].Count != 4 {
		t.Fatalf("tab count = %d, want 4", tabs[0].Count)
	}
}

func TestLoadPlaylistSourceTabsSortsTabsByCount(t *testing.T) {
	counts := map[string]int{"netease": 0, "qq": 0, "kugou": 2, "kuwo": 4}
	tabs, errMsg := loadPlaylistSourceTabs([]string{"netease", "qq", "kugou", "kuwo"}, func(src string) ([]model.Playlist, error) {
		playlists := make([]model.Playlist, 0, counts[src])
		for i := 0; i < counts[src]; i++ {
			playlists = append(playlists, model.Playlist{ID: src, Name: src, TrackCount: 1})
		}
		return playlists, nil
	})
	if errMsg != "" {
		t.Fatalf("unexpected error message: %s", errMsg)
	}
	want := []string{"kuwo", "kugou", "netease", "qq"}
	if len(tabs) != len(want) {
		t.Fatalf("tabs = %d, want %d", len(tabs), len(want))
	}
	for i, source := range want {
		if tabs[i].Source != source {
			t.Fatalf("tab[%d] = %s, want %s", i, tabs[i].Source, source)
		}
	}
}

func ids(playlists []model.Playlist) []string {
	out := make([]string, 0, len(playlists))
	for _, playlist := range playlists {
		out = append(out, playlist.ID)
	}
	return out
}
