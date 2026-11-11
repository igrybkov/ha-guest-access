package authz

import "strings"

// Match reports whether s matches pattern, where "*" matches any run of
// characters (none included) and every other character matches itself.
// Matching is case-sensitive and works on bytes.
//
// It splits the pattern on "*". The first part must be a prefix of s and
// the last part a suffix. Each middle part is then found left to right.
// Taking the leftmost match for each part is always safe with "*" as the
// only wildcard, so there is no backtracking. The cost is a few
// strings.Index calls, near linear in len(s) for any pattern.
func Match(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	prefix, suffix := parts[0], parts[len(parts)-1]
	if len(s) < len(prefix)+len(suffix) || !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
		return false
	}
	rest := s[len(prefix) : len(s)-len(suffix)]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return true
}

// matchAny reports whether s matches any of patterns.
func matchAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if Match(p, s) {
			return true
		}
	}
	return false
}

// isExact reports whether pattern has no wildcard.
func isExact(pattern string) bool { return !strings.Contains(pattern, "*") }
