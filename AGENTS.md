# AGENTS.md

This file guides AI coding agents (Claude Code, Codex, Cursor and others) that work in this repository.

Guest Access is a Home Assistant app (add-on). A single static Go binary serves links that let guests control a few things at home. A link is built from templates (rules that pick entities and allowed actions) plus extra rules for the occasion. Every guest action is checked on the server before it reaches HA. The add-on manifest lives in `guest_access/` (`config.yaml`, `DOCS.md`, `CHANGELOG.md`, `translations/`).

## Commands

```sh
make build     # static binary ./guest-access
make test      # go test -race ./...
make test-ui   # node --test on internal/web/ui/*_test.mjs (passes when there are none)
make vet
make lint      # staticcheck, pinned version run via `go run`
make run-dev   # local run; data in ./data, config in ./dev-config
```

Single test: `go test -race ./internal/authz -run TestName`.

CI runs vet, test, lint, test-ui, a cross-build for linux/amd64 and arm64, and a Docker build. Run all four `make` checks before you call a change done. Run fuzz targets for a while after you touch parsing or security code, for example `go test ./internal/authz -run '^$' -fuzz FuzzCheck -fuzztime 30s`.

## Safety rules

- **Never touch a live Home Assistant.** Real locks, alarms and scripts are attached to it. Tests and dev work use the fakes in `internal/ha/hatest`. Do not point `run-dev` at a real HA unless a human asked for it, and never call lock, alarm or script services there.
- **Never commit secrets or private details.** This repo is public. No tokens, hostnames, home coordinates or LAN addresses. Tests use documentation ranges (192.0.2.0/24, 203.0.113.0/24, 2001:db8::/32).
- Do not push tags. See Release.

## Security invariants

Keep these true in every change. Each has tests; add a test when you touch one.

- **Deny by default.** A guest request is checked in this order: rate limit → slug → link window → password session (if the link has passwords) → device approval (if on) → `authz.Check` (exact entity and action pair, catalog domain, rule access, typed data) → one-time use → HA call. A missing step is a bug.
- **One entity per call.** The server builds every service call itself with exactly one `entity_id`. Guest data never carries `entity_id`, `area_id`, `device_id`, `label_id` or `floor_id`, and never a `code`.
- **Domain filter.** An entity picked by an area, label, device or wildcard counts only if the rule allows an action in its domain.
- **No codes.** The app never stores, sends or sees alarm or lock codes. Entities that need one are exposed through HA scripts.
- **Client IP.** Use `access.ClientFrom` and decide "local" only with `Client.Local(nets)`. Forwarding headers count only from trusted proxies. An unresolved client behind a trusted proxy is never local.
- **Passwords** are hashed with argon2id on the exact input. Never normalize, log or return them.
- **State leaks.** Guests see only granted entities, and only attributes on the catalog allowlist (`catalog.FilterState`).
- **Ports.** The admin API is on the ingress port only and accepts only the Supervisor ingress peer. The guest port has its own mux with no admin routes. Guest responses send a strict CSP (`default-src 'self'`). No inline `<script>`, no `on…=` attributes, no custom JS or HTML from users.
- **Secrets on disk.** `/data` files are written with mode 0600. Nothing secret goes into the config folder, which is tracked in the user's HA git repo.

## Configuration policy

No configuration is hardcoded. Every value that changes behavior a user could want to tune (limits, TTLs, timeouts, sizes, thresholds) is an app option in `guest_access/config.yaml` with:
- a default in `options`,
- a type in `schema`,
- a short entry in `translations/en.yaml`,
- a line in `DOCS.md`.

Code reads the value from `internal/config`. Package constants serve only as defaults. Fixed invariants are not configuration: key length, token formats, the argon2 PHC format and its parameter caps.

Rate limiting can be turned off with `rate_limits.enabled: false`. It is not recommended, so the app warns in the log and the admin UI shows a banner while it is off.

## Architecture

`cmd/guest-access/main.go` wires everything. Read it first to see how the packages connect.

- `model`: shared types (templates, rules, links, settings, inventory) with shape validation.
- `catalog`: every action the app knows, its data fields and the attribute allowlist per domain. An action not in the catalog can't be granted or called.
- `authz`: `Resolve` expands a link's templates against the inventory, mirroring HA's target expansion. `Check` validates one guest request and builds the call.
- `access`: client IP, local networks, distance, signed tokens, slugs, rate limiters, passwords and the password guard.
- `ha` and `ha/hatest`: the HA websocket client (copied from ha-nfc-player) and its fakes.
- `inventory`: builds the inventory snapshot and streams state changes.
- `links`: the link store. One-time use is consumed under its lock before the HA call and refunded on failure.
- `ma`: the optional Music Assistant client. The app must work fully without it.
- `config`: env and app options.
- `store` and `migrate`: see Storage.

Storage:
- `/homeassistant/guest_access` (config set): `templates.yaml`. Tracked in the user's HA git repo.
- `/data` (data set): `links.yaml` and `hmac.key`. Never in git.
- `store` writes atomically, reloads on file change and migrates in memory on read. Nothing is written until something changes.
- `migrate` owns each set's schema version. To change a file format, add a step that works on YAML nodes only (never app types), bump the set's `Current`, and add `testdata/<set>/v<N>/`. Steps stay forever. File names are never removed.

## Extending

- New domain or action: add it to the catalog with exact field kinds and ranges, add its attributes to the allowlist (leave out anything sensitive), add a guest UI control, and add escape tests.
- New option: follow the configuration policy above.

## Release

Set `version` in `guest_access/config.yaml`, add a `## X.Y.Z` section to `guest_access/CHANGELOG.md`, and push to `main`. Do not push tags. The release workflow tests, builds and pushes the image, then creates the tag and the GitHub release. A failed build leaves no tag. A version ending in `-dev` is never released. Ask a human before you bump the version.

## Conventions

- Comments and docs use short, plain sentences (Simplified Technical English style). No em dashes. Match that tone.
- Comments and docs must stand on their own. Never point at planning notes, task briefs, decision numbers or work phases that live outside the repo ("plan section 4", "decision 6", "the UI wave"). Say the rule itself. `make lint` fails on these.
- Match the idioms of the sibling app ha-nfc-player: stdlib first, `coder/websocket`, `go.yaml.in/yaml/v3`, `log/slog`, embedded vanilla JS with `node --test` tests, table tests.
- Every change comes with tests. Security code gets table tests for each attack and a fuzz target where input is parsed.
