# Changelog

## 0.4.1

- Entity names follow HA's display name (`friendly_name`), so renames
  you made in HA show on the guest page and in the admin. Before, an
  entity with no name of its own showed its integration's original
  name, ignoring a renamed device. A device rename now refreshes the
  names at once.

## 0.4.0

- Admin: a Copy URL button on each link in the Links list. New URL
  copies the new URL to the clipboard right away.
- A link's name is optional and for the admin only. Guests no longer see
  it: the guest page shows the link's own title, else the global
  `guest.title`. A new link starts with a friendly made-up name such as
  "Sleepy Otter" (↻ suggests another). A link with no name shows as
  "Untitled link" and the start of its id in the admin, the log, HA
  notifications and the `guest_link` script variable.
- A link can be saved inactive: pick **Inactive** under "When it
  works". Guests can't use it, whatever its window, and it needs no end
  date yet. Pick another preset or edit the dates to turn it on. The
  Links screen shows it as Inactive.

## 0.3.0

- Admin: two lists with drag and drop (mouse, touch and keyboard) set
  what a guest page shows and in which order. A link's templates move
  between "In this link" and "Available"; the guest page shows their
  groups in that order. The entities of a template, and of a link's
  extra rules, sit in "Shown" (in guest page order, saved as
  `entity_order`) and "Removed". Removing an entity adds its id to the
  rules' `exclude`, so the guest loses access to it; "+" takes it out
  again. Entities not placed now go by name, not by entity id. Rules
  can be sorted by drag too.
- A template may have no rules, and a link no templates and no rules.
  Both are valid and grant nothing, so you can build them up step by
  step. The admin shows a warning until they grant something.
- Guest page: once a guest is in, the title is the link's name, not the
  global `guest.title`. A link can set its own `guest_title` and
  `guest_welcome` in the link editor. The password, device approval and
  "not active" screens show the global `guest.title` and `guest.welcome`
  (before, they always showed "Welcome"), never the link's text. So the
  URL alone does not tell the link's name.
- Guest port: optional HTTPS, so a reverse proxy can talk HTTPS to the
  app. Turn on `ssl`. The app uses `certfile` and `keyfile` from `/ssl`
  (mapped read-only) and loads a renewed pair without a restart. When
  they are missing or broken, it uses a self-signed certificate it keeps
  in `/data`, valid for `ssl_self_signed_days`, and logs its SHA-256
  fingerprint. It never falls back to plain HTTP. Off by default; with
  it off nothing changes. The Settings page shows the certificate in use.
- Guest port: a browser that opens the bare address, an unknown path or
  a link that does not exist gets a plain "This link doesn't work" page
  with status 404, not raw JSON. The page names nothing about the app.
  Change its text with `guest.not_found_title` and
  `guest.not_found_message`, or send browsers to your own site with
  `guest.not_found_redirect`. Scripts still get the JSON 404, and every
  unknown or malformed link gets the same answer.
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
