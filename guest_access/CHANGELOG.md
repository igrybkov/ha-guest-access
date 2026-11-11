# Changelog

## Unreleased

- Admin: set the order of entities on the guest page. The template editor
  and the link editor (for the extra rules' group) list the entities with
  up and down buttons, saved as `entity_order`. Entities not placed now
  go by name, not by entity id.
- Guest port: a browser that opens the bare address, an unknown path or
  a link that does not exist gets a plain "This link doesn't work" page
  with status 404, not raw JSON. The page names nothing about the app.
  Change its text with `guest.not_found_title` and
  `guest.not_found_message`, or send browsers to your own site with
  `guest.not_found_redirect`. Scripts still get the JSON 404, and every
  unknown or malformed link gets the same answer.
- Admin: ticked templates on a link can be moved up and down. The guest
  page shows its groups in that order.
- Guest: a fan's speed slider shows only while the fan is on, like a
  light's brightness. A fan with no on/off grant keeps it. The slider
  moves in the fan's own steps (`percentage_step`), for example 25% for
  a fan with 4 speeds.
- Covers: open, close, stop and position, plus the same for tilt. The
  guest card shows the state and the position, with sliders for position
  and tilt. A garage, gate or door asks for a second tap to open, and a
  confirm before its position slider moves it.
- The demo house has a living room blind and a garage door.
- Admin: a "Waiting to be admitted" panel at the top of every page lists
  the device requests of all links, with Approve and Deny. It refreshes
  every `approval.admin_poll_s` seconds while the tab is visible. The menu
  shows the count. `/app/<slug>/pending` in HA opens the app on it.
- Each waiting device request shows as an HA persistent notification with
  the code and a link to the panel. It goes away when the request is
  approved, denied or expires. Turn it off with
  `approval.persistent_notification: false`.

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
