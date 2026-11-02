package core

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guohuiyuan/music-lib/model"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// music-lib 里的酷我 GetUserPlaylists 目前直接返回 ErrUserPlaylistsUnsupported，
// 这里直接调酷我 App 端接口补齐「我收藏的歌单」。
//
// 该接口只认 Cookie 中的 userid，不校验其它登录态，一次返回该用户全部歌单
// （自建的 GENERAL 和「我喜欢」MYFAVORITE），分页在本地完成。
const (
	kuwoUserPlaylistEndpoint = "http://nplserver.kuwo.cn/pl.svc"
	// kuwoUserPlaylistUserAgent 是酷我 App 端的 UA，接口按 UA 返回 App 数据。
	kuwoUserPlaylistUserAgent = "okhttp/3.10.0"
)

var kuwoUserPlaylistHTTPClient = &http.Client{Timeout: 15 * time.Second}

// kuwoUserPlaylistItem 对应 plist 里的单个歌单。id / musicnum / playnum 在不同
// 版本接口里可能是数字也可能是字符串，统一用 interface{} 接收后再转换。
type kuwoUserPlaylistItem struct {
	ID       interface{} `json:"id"`
	Title    string      `json:"title"`
	Picture  string      `json:"pic"`
	Info     string      `json:"info"`
	MusicNum interface{} `json:"musicnum"`
	PlayNum  interface{} `json:"playnum"`
	Type     string      `json:"type"`
}

type kuwoUserPlaylistResponse struct {
	Result string                 `json:"result"`
	Total  int                    `json:"total"`
	List   []kuwoUserPlaylistItem `json:"plist"`
}

// fetchKuwoUserPlaylists 按 Cookie 中的 userid 拉取酷我个人歌单并本地分页。
func fetchKuwoUserPlaylists(cookie string, page, limit int) ([]model.Playlist, error) {
	uid := kuwoCookieValue(cookie, "userid", "user_id", "uid", "t3kwid")
	if uid == "" {
		return nil, fmt.Errorf("酷我 Cookie 中缺少 userid，请在酷我网页登录后复制完整 Cookie")
	}
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}

	values := url.Values{}
	values.Set("op", "getlistbyuid")
	values.Set("bigid", "1")
	values.Set("uid", uid)

	req, err := http.NewRequest(http.MethodGet, kuwoUserPlaylistEndpoint+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", kuwoUserPlaylistUserAgent)
	if cookie = strings.TrimSpace(cookie); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := kuwoUserPlaylistHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求酷我歌单失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取酷我歌单响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("酷我歌单接口返回 HTTP %d", resp.StatusCode)
	}

	body, err = decodeKuwoUserPlaylistBody(body)
	if err != nil {
		return nil, fmt.Errorf("解码酷我歌单响应失败: %w", err)
	}

	var payload kuwoUserPlaylistResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析酷我歌单响应失败: %w", err)
	}
	if payload.Result != "" && !strings.EqualFold(payload.Result, "ok") {
		return nil, fmt.Errorf("酷我歌单接口返回异常: %s", payload.Result)
	}

	seen := make(map[string]bool, len(payload.List))
	playlists := make([]model.Playlist, 0, len(payload.List))
	for _, item := range payload.List {
		id := kuwoNumberText(item.ID)
		title := strings.TrimSpace(item.Title)
		if id == "" || title == "" || seen[id] {
			continue
		}
		seen[id] = true
		extra := map[string]string{}
		if kind := strings.TrimSpace(item.Type); kind != "" {
			extra["kuwo_type"] = kind
		}
		playlists = append(playlists, model.Playlist{
			ID:          id,
			Source:      "kuwo",
			Name:        title,
			Cover:       strings.TrimSpace(item.Picture),
			Description: strings.TrimSpace(item.Info),
			TrackCount:  kuwoNumberInt(item.MusicNum),
			PlayCount:   kuwoNumberInt(item.PlayNum),
			Link:        "http://www.kuwo.cn/playlist_detail/" + id,
			Extra:       extra,
		})
	}

	start := (page - 1) * limit
	if start >= len(playlists) {
		return []model.Playlist{}, nil
	}
	end := start + limit
	if end > len(playlists) {
		end = len(playlists)
	}
	return playlists[start:end], nil
}

// kuwoCookieValue 从 Cookie 字符串中取指定字段（字段名大小写不敏感）。
func kuwoCookieValue(cookie string, names ...string) string {
	for _, part := range strings.Split(cookie, ";") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for _, name := range names {
			if strings.EqualFold(key, name) {
				return strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
	}
	return ""
}

// decodeKuwoUserPlaylistBody 兼容酷我接口返回的 GB18030 编码（旧版 App 数据）。
func decodeKuwoUserPlaylistBody(body []byte) ([]byte, error) {
	if utf8.Valid(body) {
		return body, nil
	}
	return simplifiedchinese.GB18030.NewDecoder().Bytes(body)
}

// kuwoNumberText 把 JSON 里的数字或字符串统一转成十进制文本，避免 float64 出现科学计数法。
func kuwoNumberText(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return ""
		}
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// kuwoNumberInt 把 JSON 里的数字或字符串统一转成整数，无法解析时按 0 处理。
func kuwoNumberInt(value interface{}) int {
	text := kuwoNumberText(value)
	if text == "" {
		return 0
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	return int(n)
}
