package access

// GuestCookiePath is the Path of every guest cookie. It covers all links,
// so a cookie survives a slug rotation. Each cookie name carries the link
// id (see LinkCookie), so two links open in one browser never overwrite
// each other's cookies. The token inside is bound to the link id too.
const GuestCookiePath = "/g/"

// LinkCookie returns the name of cookie base for link linkID, like
// "ga_session_3f9a1c0d7e2b4a65". Link ids are hex, so the name is a valid
// cookie token.
func LinkCookie(base, linkID string) string { return base + "_" + linkID }
