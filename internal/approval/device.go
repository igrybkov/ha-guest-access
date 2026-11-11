package approval

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/igrybkov/ha-guest-access/internal/model"
)

// maxUALen caps the User-Agent read by DescribeDevice.
const maxUALen = 512

// UnknownDevice is the summary of a User-Agent DescribeDevice can't read.
const UnknownDevice = "Unknown device"

// DescribeDevice returns a short summary of a User-Agent, such as
// "iPhone, Safari". It is for people deciding on a request, not for
// security: a browser can claim anything. The result comes from a fixed
// word list, so no part of the header reaches HA or the logs.
func DescribeDevice(ua string) string {
	if len(ua) > maxUALen {
		ua = ua[:maxUALen]
	}
	var parts []string
	if os := deviceOS(ua); os != "" {
		parts = append(parts, os)
	}
	if b := browserName(ua); b != "" {
		parts = append(parts, b)
	}
	if len(parts) == 0 {
		return UnknownDevice
	}
	return strings.Join(parts, ", ")
}

func deviceOS(ua string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case has("iPhone"):
		return "iPhone"
	case has("iPad"):
		return "iPad"
	case has("Android"):
		return "Android"
	case has("CrOS"):
		return "ChromeOS"
	case has("Windows"):
		return "Windows"
	case has("Macintosh"), has("Mac OS X"):
		return "Mac"
	case has("Linux"):
		return "Linux"
	}
	return ""
}

// browser checks the brands that also say "Chrome" or "Safari" first.
func browserName(ua string) string {
	has := func(s string) bool { return strings.Contains(ua, s) }
	switch {
	case has("Edg/"), has("EdgA/"), has("EdgiOS/"):
		return "Edge"
	case has("SamsungBrowser/"):
		return "Samsung Internet"
	case has("OPR/"), has("OPiOS/"):
		return "Opera"
	case has("Firefox/"), has("FxiOS/"):
		return "Firefox"
	case has("CriOS/"), has("Chrome/"):
		return "Chrome"
	case has("Safari/"):
		return "Safari"
	}
	return ""
}

// Clean makes s safe to store and show: control characters go, invalid
// UTF-8 is replaced, and the result is at most model.MaxDeviceTextLen
// bytes, cut on a character boundary.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(s) <= model.MaxDeviceTextLen {
		return s
	}
	cut := model.MaxDeviceTextLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
