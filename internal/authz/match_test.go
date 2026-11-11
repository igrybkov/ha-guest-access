package authz

import (
	"strings"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"", "", true},
		{"", "a", false},
		{"*", "", true},
		{"*", "anything", true},
		{"**", "", true},
		{"light.kitchen", "light.kitchen", true},
		{"light.kitchen", "light.kitchen2", false},
		{"light.kitchen", "Light.kitchen", false},
		{"light.guest_*", "light.guest_", true},
		{"light.guest_*", "light.guest_lamp", true},
		{"light.guest_*", "light.gues", false},
		{"light.guest_*", "switch.guest_lamp", false},
		{"*.guest_room_*", "light.guest_room_ceiling", true},
		{"*.guest_room_*", "light.guest_room", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "acb", false},
		{"ab*ba", "aba", false}, // prefix and suffix may not overlap
		{"ab*ba", "abba", true},
		{"a*a", "a", false},
		{"a*a", "aa", true},
		{"*a*a*a*b", "aaab", true},
		{"*a*a*a*b", "aaa", false},
		{"Office*", "Office Speaker", true},
		{"office*", "Office Speaker", false},
		{"?", "a", false}, // only * is special
		{"[a]", "a", false},
		{"[a]", "[a]", true},
		{"a.c", "abc", false},
		{"é*", "éa", true},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.s); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
		if got := matchRef(tt.pattern, tt.s); got != tt.want {
			t.Errorf("matchRef(%q, %q) = %v, want %v (bad test)", tt.pattern, tt.s, got, tt.want)
		}
	}
}

// A backtracking matcher takes exponential time on these. Ours must not.
func TestMatchIsFast(t *testing.T) {
	long := strings.Repeat("a", 200_000)
	patterns := []string{
		"*a*a*a*a*a*a*a*a*a*a*a*a*b",
		strings.Repeat("*a", 200) + "*b",
		strings.Repeat("a*", 500) + "b",
	}
	start := time.Now()
	for _, p := range patterns {
		if Match(p, long) {
			t.Fatalf("Match(%.20q...) = true", p)
		}
	}
	// The budget is loose for -race and slow CI. Exponential would take
	// years; this takes well under a millisecond on a laptop.
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v", d)
	}
}

// matchRef is a plain dynamic-programming matcher, the reference for
// FuzzMatch. dp[j] is true when pattern[:i] matches s[:j].
func matchRef(pattern, s string) bool {
	dp := make([]bool, len(s)+1)
	dp[0] = true
	for i := 0; i < len(pattern); i++ {
		next := make([]bool, len(s)+1)
		if pattern[i] == '*' {
			seen := false
			for j := 0; j <= len(s); j++ {
				seen = seen || dp[j]
				next[j] = seen
			}
		} else {
			for j := 1; j <= len(s); j++ {
				next[j] = dp[j-1] && s[j-1] == pattern[i]
			}
		}
		dp = next
	}
	return dp[len(s)]
}

func FuzzMatch(f *testing.F) {
	for _, seed := range [][2]string{
		{"*", ""}, {"a*b", "ab"}, {"ab*ba", "aba"}, {"*a*a*b", "aaaa"},
		{"light.guest_*", "light.guest_x"}, {"**a**", "a"}, {"*.*", "light.a"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, s string) {
		if len(pattern) > 64 || len(s) > 256 {
			return // keep the reference cheap
		}
		if got, want := Match(pattern, s), matchRef(pattern, s); got != want {
			t.Fatalf("Match(%q, %q) = %v, reference says %v", pattern, s, got, want)
		}
	})
}
