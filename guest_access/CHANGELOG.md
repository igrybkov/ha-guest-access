# Changelog

## 0.2.0

The first usable version.

- Templates: rules pick entities by entity, area, label or device, with `*`
  wildcards and exclude. An entity counts only when the rule allows an
  action in its domain. Templates apply live to open links.
- Links: a start and end time, templates plus extra rules, one-time use,
  random or custom URL, rotate, revoke and a QR code.
- Controls for locks, alarm panels, lights, fans, media players, switches
  and scripts. Scripts run with fixed variables and can be cancelled. The
  app never handles alarm or lock codes.
- Every guest action is checked on the server: the exact entity and action,
  the data, the link window and the access rules. One entity per call.
- Optional passwords per link, hashed with argon2id. Too many wrong tries
  block an address and slow the link down.
- Optional device approval: manual, self-service or claim mode, with a
  code check and an HA blueprint for actionable notifications.
- Optional "local only" (opt-in, best effort) and "near home" rules.
- Optional Music Assistant party button.
- Rate limits on every route, all adjustable in the app options. They can
  be turned off, which is not recommended.
- Audit log of who did what and when, in the app log and in
  `/data/audit.jsonl`, with an Activity page.
- Admin UI in the HA sidebar: links, templates, settings, diagnostics and
  activity. Mobile-first guest page that also fits wide screens.
- Admin times use HA's time zone, daylight saving included. Guests see
  times in their own phone's zone and never learn HA's.
- All settings are app options. Templates live in
  `/homeassistant/guest_access`; links and keys live in `/data`.

## 0.1.0

- Skeleton: health check on the admin port. The guest port answers 503.
- `log_level` option. At debug, the guest port logs the address and proxy
  headers of each request.
