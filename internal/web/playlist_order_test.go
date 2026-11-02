package web

import (
	"testing"

	"github.com/guohuiyuan/music-lib/model"
)

func TestLoadPlaylistSourceTabsSortsByTrackCount(t *testing.T) {
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
	want := []string{"b", "c", "d", "a"}
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

func ids(playlists []model.Playlist) []string {
	out := make([]string, 0, len(playlists))
	for _, playlist := range playlists {
		out = append(out, playlist.ID)
	}
	return out
}
