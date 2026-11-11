package approval

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

func TestDescribeDevice(t *testing.T) {
	for _, tc := range []struct{ ua, want string }{
		{iPhoneUA, "iPhone, Safari"},
		{androidUA, "Android, Chrome"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0 Mobile/15E148 Safari/604.1", "iPhone, Chrome"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/131.0 Mobile/15E148 Safari/605.1.15", "iPhone, Firefox"},
		{"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) EdgiOS/130.0 Mobile/15E148 Safari/605.1.15", "iPad, Edge"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0", "Windows, Edge"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0", "Windows, Firefox"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15", "Mac, Safari"},
		{"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36", "Android, Samsung Internet"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "ChromeOS, Chrome"},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/114.0.0.0", "Linux, Opera"},
		{"curl/8.7.1", UnknownDevice},
		{"", UnknownDevice},
		{"\x1b[2J<script>alert(1)</script>", UnknownDevice},
		// Words past the read cap are not seen.
		{strings.Repeat("x", maxUALen) + " iPhone Safari/1", UnknownDevice},
	} {
		if got := DescribeDevice(tc.ua); got != tc.want {
			t.Errorf("DescribeDevice(%.60q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

func TestClean(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Anna", "Anna"},
		{"An\x00na\n\r\t", "Anna"},
		{"Ann\u0085a\u009b", "Anna"},
		{"bad \xff utf8", "bad � utf8"},
		{strings.Repeat("a", 100), strings.Repeat("a", model.MaxDeviceTextLen)},
		// A cut never splits a character.
		{strings.Repeat("a", model.MaxDeviceTextLen-1) + "é", strings.Repeat("a", model.MaxDeviceTextLen-1)},
	} {
		if got := Clean(tc.in); got != tc.want {
			t.Errorf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func FuzzDescribeDevice(f *testing.F) {
	for _, s := range []string{iPhoneUA, androidUA, "", "\xff\xfe", strings.Repeat("Chrome/", 200)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ua string) {
		d := DescribeDevice(ua)
		if d == "" || len(d) > model.MaxDeviceTextLen || Clean(d) != d {
			t.Fatalf("DescribeDevice(%q) = %q", ua, d)
		}
		c := Clean(ua)
		if len(c) > model.MaxDeviceTextLen || !utf8.ValidString(c) || strings.ContainsFunc(c, unicode.IsControl) {
			t.Fatalf("Clean(%q) = %q", ua, c)
		}
	})
}
