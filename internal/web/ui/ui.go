// Package ui embeds the static pages: the guest page and the admin page.
// Both are plain HTML, CSS and JS modules with no build step, and no
// inline script or style, so a strict CSP holds.
package ui

import "embed"

// FS holds the files. guest/ is served on the guest port, admin/ on the
// admin port. Tests (*_test.mjs) are not embedded.
//
//go:embed guest/*.html guest/*.css guest/*.js admin/*.html admin/*.css admin/*.js
var FS embed.FS
