package core

import (
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestKuwoCookieValue(t *testing.T) {
	cookie := `kw_token=ABC123; userid=332437870; uname3=%u6C34; t3kwid=332437870`
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"userid"}, "332437870"},
		{[]string{"USERID"}, "332437870"},
		{[]string{"t3kwid"}, "332437870"},
		{[]string{"missing"}, ""},
	}
	for _, tc := range cases {
		if got := kuwoCookieValue(cookie, tc.names...); got != tc.want {
			t.Fatalf("kuwoCookieValue(%v) = %q, want %q", tc.names, got, tc.want)
		}
	}

	if got := kuwoCookieValue(`userid="12345678"`, "userid"); got != "12345678" {
		t.Fatalf("quoted cookie value = %q, want %q", got, "12345678")
	}
	if got := kuwoCookieValue("", "userid"); got != "" {
		t.Fatalf("empty cookie value = %q, want empty", got)
	}
}

func TestKuwoNumberHelpers(t *testing.T) {
	if got := kuwoNumberText(float64(435082330)); got != "435082330" {
		t.Fatalf("kuwoNumberText(float64) = %q", got)
	}
	if got := kuwoNumberText(" 465 "); got != "465" {
		t.Fatalf("kuwoNumberText(string) = %q", got)
	}
	if got := kuwoNumberText(nil); got != "" {
		t.Fatalf("kuwoNumberText(nil) = %q", got)
	}
	if got := kuwoNumberInt(float64(125)); got != 125 {
		t.Fatalf("kuwoNumberInt(float64) = %d", got)
	}
	if got := kuwoNumberInt("465"); got != 465 {
		t.Fatalf("kuwoNumberInt(string) = %d", got)
	}
	if got := kuwoNumberInt("abc"); got != 0 {
		t.Fatalf("kuwoNumberInt(invalid) = %d", got)
	}
}

func TestDecodeKuwoUserPlaylistBody(t *testing.T) {
	raw := []byte(`{"plist":[{"id":2014918480,"title":"我喜欢听","musicnum":19,"type":"MYFAVORITE"}],"result":"ok","total":1}`)
	gbk, err := simplifiedchinese.GB18030.NewEncoder().Bytes(raw)
	if err != nil {
		t.Fatalf("encode GB18030: %v", err)
	}

	decoded, err := decodeKuwoUserPlaylistBody(gbk)
	if err != nil {
		t.Fatalf("decodeKuwoUserPlaylistBody: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded body = %q, want %q", decoded, raw)
	}

	// 纯 ASCII 或已转码过的响应应原样返回。
	utf8Body := []byte(`{"result":"ok","plist":[]}`)
	decoded, err = decodeKuwoUserPlaylistBody(utf8Body)
	if err != nil {
		t.Fatalf("decodeKuwoUserPlaylistBody(utf8): %v", err)
	}
	if string(decoded) != string(utf8Body) {
		t.Fatalf("utf8 body = %q, want %q", decoded, utf8Body)
	}
}
