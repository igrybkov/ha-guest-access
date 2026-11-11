# Guest Access

Guest Access gives guests a link. The link opens a page with a few controls: unlock the door, dim the guest room lights, join the music party.

You build each link from templates. A template is a set of rules. A rule picks entities and says which actions a guest may run on them.

The app checks every guest action on the server before it reaches Home Assistant (HA). A guest can only do what a rule grants, from the places the rule allows.

## Install

1. In Home Assistant open Settings, Apps, App store, the three-dot menu, Repositories.
2. Add `https://github.com/igrybkov/ha-guest-access`.
3. Install Guest Access and start it.
4. Open **Guest Access** in the sidebar.
5. In the app's Configuration tab, set `public_base_url` to the address guests will use, such as `https://guests.example.com`. Without it, links have no full URL and no QR code.
6. Point your reverse proxy at the guest port (see **Ports and reverse proxy**).

Saving options in the Configuration tab restarts the app.

## Quick start

1. Open **Templates** and press **New template**. Name it "Guest room". Add a rule: pick the area "Guest room" and the actions `light.turn_on` and `light.turn_off`. Save.
2. Open **Links** and press **New link**. Name it after the guest. Tick the template. Set an end date. Every link must end.
3. Save. Press **Share** to copy the URL or show the QR code.
4. Send the URL to the guest. Add a password if you want one (see **Passwords**).

Open the URL yourself first. You see what the guest sees.

## Templates and rules

A template has a name, an icon, a list of rules and an optional default `access`. A rule has these parts:

| Part | Meaning |
|---|---|
| **`select`** | What the rule picks. |
| **`exclude`** | What the rule removes from the pick. It wins over `select`. |
| **`actions`** | The actions a guest may run. |
| **`access`** | Where the guest must be. Replaces the template's `access` for this rule. |
| **`variables`** | Fixed variables for `script.run`. |

You edit templates in the **Templates** screen. They are stored in `templates.yaml` in the config folder, so you can edit that file by hand or keep it in git. The app picks up a change to the file.

### Select and exclude

`select` and `exclude` take four lists. The lists combine as a union.

| List | Matches |
|---|---|
| **`entities`** | Entity IDs, such as `light.guest_lamp`. |
| **`areas`** | Area IDs. |
| **`labels`** | Label IDs. |
| **`devices`** | Device IDs, or device names (the name you gave it, else the registry name). |

An area picks the entities in it. A device picks the entities of the device. A label picks entities, devices and areas that carry it. A device or area picked by label expands like one picked by name.

An entity's own area wins over its device's area. A device in the area "Guest room" gives its entities to that area only when the entity has no area of its own.

The app does not expand floors, groups or child devices.

A `devices:` pattern matches the display name, and that name can change without you. An HA user can rename a device, and some integrations take the name from the device itself. A device whose new name matches the pattern joins the rule, and its entities become guest controls. Device IDs never change, so use them for anything that matters, such as locks and alarms.

### Wildcards

A `*` matches any run of characters, none included. It is the only wildcard.

- `light.guest_*` matches `light.guest_lamp` and `light.guest_ceiling`.
- `*_lamp` in `entities` matches every entity that ends in `_lamp`.
- Matching is case-sensitive. Nothing is trimmed or lower-cased.
- In `select`, a pattern made only of `*` and `.` (such as `*` or `*.*`) is refused. It would pick the whole house. `exclude` may use them.

### The domain filter

A rule grants an action only on entities of that action's domain. `light.turn_on` applies to lights. It never applies to a lock.

So an area can hold a lock and a rule can still be safe. This rule picks the whole guest room, but grants light actions only. The lock in the room gets nothing:

```yaml
rules:
  - select:
      areas: [guest_room]
    actions: [light.turn_on, light.turn_off]
```

To let the guest lock the room's door, add `lock.lock` to the rule's actions. Leave out `lock.unlock` and the guest can lock it but not open it.

### Hidden, disabled and categorized entities

Areas, devices, labels and `*` wildcards skip three kinds of entity:

- hidden entities
- disabled entities
- entities with an entity category (config and diagnostic entities)

This follows how HA itself expands a target. An exact entity ID in `entities` always counts, even for a hidden entity. `exclude` removes entities of all three kinds too.

The app is slightly stricter than HA. HA keeps a categorized entity when it is picked by an entity label. The app skips it, so every path skips the same entities.

### Live templates

Templates are live. When you edit a template, every open link that uses it changes at once. There is nothing to re-share. A guest's page updates within a few seconds.

The same holds for HA. If you move a device to another area, a rule that picks that area follows. The app re-reads the HA registries after at most `inventory.max_age_s` seconds, or sooner when HA reports a change.

A template that fails validation grants nothing until you fix it. The **Templates** screen lists the problems. Deleting a template removes its rules from every link that used it, and the link shows a warning.

### Per-rule access

`access` limits where a rule works. It has two parts:

| Key | Meaning |
|---|---|
| **`network`** | `any` (default) or `local`. See **Local only**. |
| **`near_home_m`** | The guest must prove they are within this many meters of home. `0` is off. See **Near home**. |

A template's `access` is the default for its rules. A rule's own `access` replaces it as a whole. The two are never merged.

If two rules grant the same entity and action, the first one whose access passes the request applies. In effect, the looser rule wins. Do not grant one pair twice with different access unless you want that.

A link's extra rules each carry their own `access`. A link's validity window applies to everything.

When no rule passes for a pair, the guest does not see the control. A control that would pass after the guest shares a location shows with a prompt.

### Scripts, variables and `guest_link`

To run an HA script, grant `script.run` on the script entity. The app calls `script.turn_on`. It passes the rule's `variables` and one more, `guest_link`: the name of the link. `script.cancel` calls `script.turn_off`.

```yaml
rules:
  - select:
      entities: [script.let_me_in]
    actions: [script.run]
    variables:
      room: front_door
```

Inside the script, use `{{ guest_link }}` and `{{ room }}`. The app sets `guest_link` last, so a rule can't fake it. The guest sends no variables of their own.

`variables` apply only to `script.run`. A key called `code`, at any depth, is refused.

### Codes: why the app never handles them

The app never stores, sends or sees an alarm or lock code. No field in a template, link or guest request carries one. A guest can never type one.

This keeps a code out of the app's files, its logs and its network traffic. If someone steals the app's data, they find no code.

An alarm or lock that needs a code goes behind a script. The script holds the code. In the **Templates** editor, an entity that wants a code shows a warning.

```yaml
# HA: scripts.yaml
let_me_in:
  alias: Let me in
  fields:
    guest_link:
      description: Name of the guest link
  sequence:
    - action: alarm_control_panel.alarm_disarm
      target:
        entity_id: alarm_control_panel.home
      data:
        code: !secret alarm_code
    - action: lock.unlock
      target:
        entity_id: lock.front_door
    - action: notify.notify
      data:
        message: "{{ guest_link }} let themselves in."
```

Keep the code in `secrets.yaml`. A script made in the HA UI cannot use `!secret`, so write such a script in YAML.

### Example templates

This is a complete `templates.yaml`. It has three templates.

```yaml
version: 1
templates:
  - id: house-entry
    name: House entry
    icon: mdi:door
    # Default for every rule below that has no access of its own.
    access:
      near_home_m: 300
    rules:
      - select:
          entities: [script.let_me_in]
        actions: [script.run]
        variables:
          door: front
      - select:
          entities: [script.im_leaving]
        actions: [script.run]
        # Replaces the default as a whole. Closer, so a guest
        # who is still on the street can't run it.
        access:
          near_home_m: 100

  - id: music-control
    name: Music control
    icon: mdi:speaker
    party: true
    rules:
      - select:
          labels: [guest_music]
        exclude:
          entities: [media_player.office]
        actions:
          - media_player.media_play_pause
          - media_player.media_next_track
          - media_player.media_previous_track
          - media_player.volume_set
          - media_player.volume_mute

  - id: guest-room
    name: Guest room control
    icon: mdi:bed
    rules:
      # Lights and the fan. A lock in the area gets nothing.
      - select:
          areas: [guest_room]
        actions:
          - light.turn_on
          - light.turn_off
          - fan.turn_on
          - fan.turn_off
          - fan.set_percentage
      # The door: lock it, never unlock it.
      - select:
          entities: [lock.guest_room_door]
        actions: [lock.lock]
```

"Let me in" and "I'm leaving" are two scripts in HA. Each one holds its own codes and steps. The guest sees two buttons.

## Links

A link is a URL, a validity window and a set of templates plus optional extra rules.

### The window

- `valid_until` is required. Every link ends.
- `valid_from` is optional. Before it, the link does not work.
- Outside the window the guest sees that the link is not active. Nothing runs.
- The window applies to everything on the link, whatever the rules say.
- A guest's open page stops getting updates within `streams.recheck_s` seconds after the link ends.

### Time zones

- The admin page shows and reads every time in HA's time zone (Settings → System → General). This covers the window inputs, the presets, the link list, devices and the activity log. The editor names the zone above the inputs.
- The presets use HA's wall clock. **Today** ends at midnight. **This weekend** runs from Friday 17:00 to Monday 00:00. **1 week** and **1 month** keep the time of day.
- Around a daylight saving change, a time that does not exist moves forward by the gap. For example, 02:30 on a day that jumps from 02:00 to 03:00 becomes 03:30. A time that happens twice means the first one.
- If HA has not reported a time zone, the admin page uses the browser's zone and says so.
- Guests see times in their own phone's time zone. The app never tells guests HA's time zone, since it hints at where the home is. Every time the guest page gets is in UTC.

### One-time use

A link with **one time** on lets each pair of entity and action run once. Other pairs stay available. A guest who unlocks the door once can still dim a light, and can dim it once.

The app marks the use before it calls HA. A failed call gives the use back only when HA clearly refused it, for example with an error about the entity. After a timeout or a lost connection the app can't tell whether the action ran, so the use stays spent. Parallel taps on the same control let exactly one through.

### Random and custom slugs

The slug is the secret part of the URL: `/g/<slug>`.

- A random slug is 22 characters from 128 random bits. It cannot be guessed.
- A custom slug is easier to remember and easier to guess. It uses `a-z`, `0-9` and `-`. It cannot start or end with `-` or hold `--`. Its length is between `links.custom_slug_min_len` and `links.custom_slug_max_len` (12 to 64 by default). Pair a custom slug with a password.
- Random slugs are the default and the safest. A custom slug shorter than `links.custom_slug_warn_len` (16) gets a warning on the admin page. It still works.
- The limits on wrong links slow down guessing, but they never block a valid link. A guest with the right URL always gets in, even from an address that guessed wrong too often.

The app keeps each slug so the admin screen can show the URL again.

### Rotate and revoke

- **New URL** (rotate) gives the link a new slug, random or custom. The old URL stops working at once. Guests who already signed in keep their password session and their approved device. Only the address changes, so send the new URL.
- **Revoke** ends the link at once. It cannot be undone. Make a new link instead. Open pages stop within `streams.recheck_s` seconds.
- **Delete** removes the link.

To cut off one guest and keep the rest, delete that guest's password, or revoke that guest's device. See **Passwords** and **Device approval**.

### QR code

**Share** shows a QR code of the full URL. It needs `public_base_url`. The QR code is an image of the secret URL. Treat it like the URL.

## Passwords

A link can have zero or more passwords. Guests often get their phone number as the password. With none, the URL alone opens the link.

- Each password has a label. The label is the guest's name. It appears in logs, in the audit file and in the `guest_access_action` event.
- The app stores an argon2id hash of the exact text you typed. It stores no hint and no part of the plain text. It never logs a password.
- **Exact match.** The app does not trim spaces, ignore case, strip digits or match part of a password. `555 0100` and `5550100` are two different passwords. Tell the guest the exact text.
- A password can be up to 1024 bytes.
- A correct password gives the browser a session cookie tied to that password. Deleting the password ends its sessions.

### Lockout and slow-down

Wrong passwords are counted:

| Scope | Default | Option | Over the limit |
|---|---|---|---|
| Per link and client address | 5 wrong in 15 minutes | `rate_limits.password_fails_per_ip`, `rate_limits.password_window_per_ip_min` | That address is locked out of the link for `rate_limits.password_lockout_min` minutes (60). |
| Per link, all addresses | 30 wrong in 60 minutes | `rate_limits.password_fails_per_link`, `rate_limits.password_window_per_link_min` | The link slows down. It takes one password try every `rate_limits.password_link_delay_s` seconds (10), from any address. |

A slowed link never locks anyone out. A guest who tries during the delay sees how many seconds to wait, and gets in on the next try. Someone who guesses from many addresses gets about one guess per delay: 360 an hour at the default. The link speeds up again once the wrong passwords are older than the window.

The delay counts from the last try that went through. Tries that the delay refused do not count, so nobody can keep a real guest waiting longer than the delay.

Each lockout of an address, and each start of a slow-down, fires the HA event `guest_access_lockout` with `link_id`, `link_name`, `ip` and `scope`. `scope` is `ip` for a locked-out address and `link` for a slowed link. A slow-down fires once when it starts, not on every try. Guests who are already signed in are not affected.

## Device approval

Device approval makes each new browser or phone ask before it can use a link. It is optional, per link. A password proves what the guest knows. Approval proves which device they use.

| Mode | Who approves a new device |
|---|---|
| Off | Nobody. Anyone with the URL and password gets in. |
| Manual | You. In the app, or from HA. |
| Self-service | A device that is already approved on the same link. |
| Self-service without manual ("claim") | The first device claims the link at once. Later devices need an approved device. |

When a new device opens the link, the guest sees a four-character code on screen. The code uses letters and digits with no look-alikes (no `0`, `O`, `1`, `I` or `L`). The request waits `approval.pending_ttl_min` minutes.

In claim mode, whoever opens the link first becomes its first device. Add a password, or open the link yourself first.

### The code check

The approver checks that the code on the guest's screen matches the code on the request. This tells a real guest from someone who found the URL. You never type the code anywhere. You only compare it.

Each request shows the code, the password label, a short summary of the device (such as "iPhone, Safari") and the client address.

### Where you approve

- In the app: the **Waiting to be admitted** panel at the top of every page lists the waiting requests of all links. Press **Approve** or **Deny**. The link's **Devices** section lists that link's requests too.
- From an HA notification: each request shows in HA's notifications, with a link that opens the app on the panel.
- From your phone: use the blueprint below.
- In self-service, from an approved device: its page shows the waiting requests.

### The waiting panel

The panel shows the link, the guest's password label, the device, the code and how long ago the guest asked. It refreshes on its own every `approval.admin_poll_s` seconds while the app's tab is open and visible. It stops while the tab is hidden, and refreshes at once when you come back. With nothing waiting, the panel hides. The **Waiting** item in the menu shows the count.

To open the app on the panel, go to `/app/<app slug>/pending` in HA. The notification link does this for you. Outside HA, add `#/pending` to the app's address.

### The HA notification

For each request that waits, the app creates an HA persistent notification. It shows up under HA's notifications (the bell). The title is "Someone wants in:" and the link name. The text has the guest's password label (if any), the device and the code, and a link to the waiting panel. The notification goes away by itself when the request is approved, denied (from the app, from HA or from a guest device) or expires.

- Turn it off with `approval.persistent_notification: false`.
- In claim mode, the first device gets in at once, so it gets no notification.
- The app reads its slug from Supervisor to build the link. If that fails, the notification has no link. Open the app from the sidebar.
- The guest's text is shown as plain text. Markdown in a password label, such as a link, does not work.
- A failed HA call is logged. It never stops the request. `ha.event_timeout_s` caps each call.

### The blueprint

The blueprint sends an actionable notification to your phone. It shows the link, the guest, the device and the code. **Approve** and **Deny** buttons send the decision back.

Import it in HA: Settings, Automations & scenes, Blueprints, Import blueprint. Paste this URL:

```
https://github.com/igrybkov/ha-guest-access/blob/main/blueprints/guest_access_device_approval.yaml
```

Then create an automation from it and pick your phone. The phone must run the official HA Companion app. The approve button asks for the phone to be unlocked.

Without the blueprint, any automation can listen for the events:

| Event | Data |
|---|---|
| **`guest_access_device_request`** | `link_id`, `link_name`, `request_id`, `code`, `label`, `device`, `ip` |
| **`guest_access_device_decision`** (you fire it) | `request_id`, `approve` (`true` or `false`, a real boolean) |

An event with no `approve` is ignored. It is never read as a denial.

### Limits

- At most `approval.max_pending` waiting requests per link (3).
- At most `approval.events_per_hour` request events per link per hour (5). Over it, the guest sees "try later". You can still approve from the app.
- At most `approval.max_devices` approved devices per link (10).

### Revoke and reset

- **Remove** on one device revokes it. Its next request fails and its open pages stop.
- **Reset devices** removes every approved device and every waiting request. In claim mode the next device claims the link again.

Neither sends anything to HA. Both appear in the audit log.

### Rotating a slug

Rotating the slug does not touch approved devices or password sessions. Their cookies belong to the link, not to the URL. Guests keep access at the new URL. To make them all start again, use **Reset devices** and delete passwords.

## Local only

A rule can ask for `network: local`. It means the guest must be on your home network.

This is opt-in and best effort. Turn it on with `local_access.enabled`. It is off by default. When it is off:

- You cannot save a rule with `network: local`.
- An existing `network: local` rule denies every request (it fails closed). The link shows a warning.

When it is on, set `trusted_proxies` if guests come through a reverse proxy or tunnel. Without it, every guest has the proxy's address, and if that address is on your network, every guest counts as local. The app warns about this in the log at start and on the Diagnostics page.

Local only can fail. A guest on your Wi-Fi may still look like an outside guest. That guest loses the local controls. Nobody gains any access. Read the failure cases below before you rely on it.

### What counts as local

The app decides from the client address. A client is local when its address is in one of `local_access.networks`. The default is the private ranges: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`.

With local access on, a local request also counts as near home.

### Trusted proxies

Guests usually reach the app through a reverse proxy, and maybe a tunnel. The app then sees the proxy's address, which is private. A plain address check would call every internet guest local.

To prevent that, set `trusted_proxies` to the proxies in front of the app. The app reads `X-Forwarded-For` only when the request comes from a trusted proxy. It takes the right-most address in that header that is not itself a trusted proxy.

With a chain such as Cloudflare, cloudflared and Nginx Proxy Manager (NPM), trust both cloudflared and NPM. Both have private addresses on your network. If you leave cloudflared out, every internet request looks local.

With `trusted_proxies` empty, the app ignores all forwarding headers and uses the peer address.

### The unresolved-client rule

A request can come from a trusted proxy and still carry no usable `X-Forwarded-For`. The header may be missing, hold garbage, or list only trusted proxies. The app cannot tell who the client is. It marks the client as unresolved. An unresolved client is never local and never near home. Failures are safe.

### Suggested setup: split DNS on one hostname

Use one hostname, such as `guests.example.com`, for guests at home and away.

- On the internet, it resolves to your tunnel or public address.
- On your home network, your DNS answers with the LAN address of your reverse proxy.
- Get a certificate through a DNS challenge so HTTPS works on the LAN too.

A guest at home then reaches the proxy directly. The proxy puts their LAN address in `X-Forwarded-For`, and the app sees a local client. The guest has one link that works everywhere.

### When it fails

Local only fails when a device skips your DNS and reaches the internet path:

- **iCloud Private Relay.** Safari may resolve the name through Apple.
- **DNS over HTTPS in the browser.** Firefox and Chrome may skip the network's DNS.
- **VPNs.** All traffic leaves through the VPN.
- **Android Private DNS.** Android sends DNS to its own server.

The guest then arrives from the internet path and counts as outside. The guest loses local-only controls. Nothing gets more access than it should.

You can change two things on your network:

- Apple says a network that blocks `mask.icloud.com` and `mask-h2.icloud.com` makes Private Relay turn off on that network. The device tells the user.
- If your DNS answers NXDOMAIN for `use-application-dns.net`, Firefox keeps the network's DNS.

There is no network-side switch for VPNs or Android Private DNS.

### Check how the app sees a request

1. Set `log_level` to `debug`. The guest port then logs the peer address, the forwarding headers, the derived client and whether it is local, for each request.
2. Open a guest link once on home Wi-Fi and once on mobile data.
3. Open **Diagnostics** in the app. It lists recent guest requests: peer, headers, client, local or not, and warnings such as "Untrusted forwarding".

If **Peer** is your proxy and **Client** is the same address, add the proxy to `trusted_proxies`. Check again. **Client** should show the guest's address.

## Near home

A rule can ask for `near_home_m`, a radius in meters around your HA home location. Set the home location in HA first (Settings, System, General). Without one, HA reports 0, 0. The app then refuses every location check, so near-home rules deny every request.

How it works:

1. The guest page asks the guest to share their location.
2. The browser sends the coordinates and their accuracy to the app.
3. The app measures the distance to home and adds the accuracy, to be safe. It discards the coordinates.
4. The app gives the browser a signed cookie that holds only that distance. The cookie lasts `location.ttl_min` minutes (15 by default). Then the page asks again.
5. A rule passes when the distance in the cookie is within its radius.

A location with an accuracy worse than `location.max_accuracy_m` is refused. The reply never says how far away the guest is.

Know these limits:

- **HTTPS is needed.** Browsers share a location only to a secure page. Use HTTPS on the public address. A local guest on plain HTTP does not need it, because a local request counts as near home when `local_access.enabled` is on.
- **It can be spoofed.** The location comes from the guest's browser. A guest who wants to fake it can. Near home stops casual misuse and fixes honest mistakes. It is not a barrier against a determined guest.
- **What is stored.** The app stores no coordinates. The cookie holds the distance. The audit file records the distance and the accuracy of each check, never the coordinates.

## Music Assistant party

The party is optional. Without Music Assistant (MA), the app never calls it, and every other control works the same.

To turn it on:

1. Set `music_assistant.url` to your MA address, such as `http://d5369777-music-assistant:8095`.
2. Create a long-lived token in MA (look in your MA user profile) and put it in `music_assistant.token`.
3. Turn on guest access for the party in MA's party settings.
4. Put `party: true` on a template. Links that use it show a "Join the party" button when the rules' access passes.

The app asks MA for the join URL each time the page needs it. The call times out after `music_assistant.timeout_ms` milliseconds (2000). If MA is down, slow or refuses the token, the button is left out and nothing else changes.

The app also checks the token once at start, and then every `music_assistant.check_interval_min` minutes (60). The check asks MA who the token belongs to (`auth/me`). It creates nothing in MA: only a guest's tap on the party button asks for a join URL, and MA may make a join code then. A refused token shows as a warning on the Settings and Diagnostics pages, before a guest finds the button missing. Without Music Assistant set up, the app never calls it.

A long-lived token expires. When it does, the party button disappears. Make a new token and update the option.

When MA refuses the token, the app logs a warning, and the Settings and Diagnostics pages show it. The app learns this only when a guest asks for the party, so the warning shows after the first refused call. It goes away after the next call that works.

### The party outlives the link

MA runs its own guest session. When a link ends or is revoked, a guest who already joined the party stays in MA until MA ends that session. The join code in MA also lives for MA's own party duration.

To cut everyone off, turn off guest access for the party in MA. The app then gets no join URL. Check MA's party settings for how it ends sessions already open.

## Rate limits

Every route has a limit. Over a limit, the app answers 429 with a `Retry-After` header. All values are options.

| Limit | Default | Keyed by |
|---|---|---|
| Guest page and any request before sign-in | 20 per minute, burst 15 | client address |
| Page files (script and style) | 300 per minute, burst 100 | client address |
| Wrong links (unknown slugs) | 10 per hour | client address |
| Wrong links, all addresses together | 100 per hour | one global key |
| Discrete actions and state reads | 120 per minute, burst 40 | link and client address |
| Continuous actions (sliders) | 600 per minute, burst 120 | link, client address and entity |
| Open live-update streams | 10 per link, 3 per client address | link, and link and client address |
| Wrong passwords | see **Passwords**: a lockout per address, a slow-down per link | link and client address, and link |
| New device requests | see **Device approval** | link |
| Admin API | 600 per minute | peer |

Notes:

- A valid link always works, whatever the wrong-link limits say. Someone else's misses never lock a real guest out.
- **IPv6 clients count by prefix.** A home or a phone gets a whole /64 of IPv6 addresses and can use a new one for each request. So every "client address" limit, and the password lockout, counts all addresses of one `rate_limits.ipv6_prefix` (/64) as one client. The log and the activity records still name the full address.
- **Slow requests are cut off.** A client has `http.read_timeout_s` seconds (30) to send one request, and its headers may be at most `http.max_header_kib` KiB (64). Live updates stay open longer: the timeout only covers reading the request.
- **Page files have their own limit.** The page's script and style files are the same for everyone and hold no secret. They do not count against the limit before sign-in, so many guests behind one home or mobile network address can load the page often.
- **Continuous and discrete are separate.** Light brightness and color, fan speed and media volume send many values while a guest drags a slider. They use the continuous limiter, so dragging does not use up the limit for buttons. Note that `light.turn_on` counts as continuous, so a plain "on" tap uses it too.
- Behind a proxy you do not trust, all guests share the proxy's address and one limiter. If many guests hit 429 together, check `trusted_proxies`.
- Limiters drop idle keys, so memory stays bounded.

Turning limits off (`rate_limits.enabled: false`) is not recommended. Nothing then slows down guesses at links or passwords. The app logs a warning at each start, the admin screens show a banner, and **Diagnostics** warns.

## Audit log

The app records who did what and when, for every security decision and every admin change.

**Recorded:**

- guest actions, allowed and denied, with an internal reason for each denial
- sign-ins, wrong passwords, lockouts and slow-downs
- wrong links, rate-limit hits and invalid sessions
- device requests (with the code shown on the guest's screen), approvals, denials, revokes and resets
- location checks (the distance and the accuracy)
- every admin write: templates, links, passwords, devices
- app starts

Each record names the link, the password label (the guest), the device, the client address, the peer and whether a proxy was used, a short summary of the browser, and for admin writes, the HA user.

**Never recorded:** slugs, passwords, tokens, cookies, coordinates, data values of an action (only the key names), and raw headers.

Records a client can cause in bulk are thinned out: wrong links, rate limits, slider drags, refused device requests, and every other denial of a guest request. Within a minute, the first record of each kind, reason, link and client is written. The next one carries a count of the ones it skipped. So a flood can't push older records, such as admin changes, out of the file. Wrong passwords and lockouts are always written one by one. The password limits already keep them few.

**Where it goes:**

- The app log gets each record at `info` level.
- The file `/data/audit.jsonl` gets it too, as one JSON object per line, with mode `0600`.
- At `audit.max_file_kib` (2048 KiB) the file is renamed to `audit.jsonl.1` and a new one starts. The app keeps one older file. Delete these files and the history is gone.
- With `audit.enabled: false`, the app writes no file and the Activity page is empty. The app log still has every record.

The **Activity** page shows recent records, newest first. Each link also shows its own recent activity.

## Options

Set these in the app's Configuration tab. Saving restarts the app. Each row shows the default.

| Option | Default | Meaning |
|---|---|---|
| `log_level` | `info` | `debug`, `info`, `warn` or `error`. At `debug`, the guest port logs the address and proxy headers of each request. |
| `local_access.enabled` | `false` | Allow rules with `network: local`. See **Local only**. |
| `local_access.networks` | `[10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7]` | Address ranges (CIDR) that count as home. |
| `trusted_proxies` | `[]` | Proxies whose `X-Forwarded-For` the app believes, as IPs or CIDRs. Add your reverse proxy and your tunnel. |
| `home_radius_default_m` | `300` | The near-home radius, in meters, that the template editor suggests. |
| `music_assistant.timeout_ms` | `2000` | How long to wait for Music Assistant, in milliseconds. |
| `music_assistant.check_interval_min` | `60` | Minutes between checks that Music Assistant takes the token. The app also checks once at start. `0` checks at start only. |
| `guest.title` | `Welcome` | Title of the guest page. Plain text. |
| `guest.slider_interval_ms` | `300` | Least milliseconds between two values the page sends while a slider moves. The final value always goes. |
| `location.ttl_min` | `15` | Minutes a shared location counts. |
| `location.max_accuracy_m` | `1000` | Worst location accuracy the app accepts, in meters. |
| `rate_limits.enabled` | `true` | Master switch. Not recommended to turn off. |
| `rate_limits.guest_per_min` | `20` | Requests per minute before sign-in, per client address. |
| `rate_limits.guest_burst` | `15` | Requests allowed in a quick row before that limit applies. |
| `rate_limits.assets_per_min` | `300` | Page files (script and style) per minute, per client address. |
| `rate_limits.assets_burst` | `100` | Burst for the limit above. |
| `rate_limits.actions_per_min` | `120` | Discrete actions and state reads per minute, per link and client address. |
| `rate_limits.actions_burst` | `40` | Burst for the limit above. |
| `rate_limits.continuous_per_min` | `600` | Slider actions per minute, per link, client address and entity. |
| `rate_limits.continuous_burst` | `120` | Burst for the limit above. |
| `rate_limits.max_streams_per_link` | `10` | Open live-update pages per link. |
| `rate_limits.misses_per_hour_per_ip` | `10` | Unknown links per hour, per client address. |
| `rate_limits.misses_per_hour_global` | `100` | Unknown links per hour, all addresses. Valid links keep working. |
| `rate_limits.password_fails_per_ip` | `5` | Wrong passwords per link and client address before that address is locked out. |
| `rate_limits.password_window_per_ip_min` | `15` | Minutes in which those wrong passwords count. |
| `rate_limits.password_fails_per_link` | `30` | Wrong passwords per link, all addresses, before the link slows down. |
| `rate_limits.password_window_per_link_min` | `60` | Minutes in which those wrong passwords count. |
| `rate_limits.password_lockout_min` | `60` | Minutes a locked-out address stays locked out of the link. |
| `rate_limits.password_link_delay_s` | `10` | Seconds between two password tries on a slowed link, from any address. |
| `rate_limits.admin_per_min` | `600` | Admin requests per minute, per peer. |
| `rate_limits.ipv6_prefix` | `64` | Every per-address limit, and the password lockout, counts an IPv6 client by this prefix length. One home or phone gets a whole /64 and can use any address in it. 32 to 128. |
| `links.custom_slug_min_len` | `12` | Shortest custom slug. Not below 12. |
| `links.custom_slug_max_len` | `64` | Longest custom slug. |
| `links.custom_slug_warn_len` | `16` | The admin page warns about a custom slug shorter than this. 12 to 22. |
| `http.guest_body_kib` | `16` | Largest guest request body, in KiB. |
| `http.admin_body_kib` | `1024` | Largest admin request body, in KiB. |
| `http.read_timeout_s` | `30` | Seconds a client has to send one whole request, headers and body. Open live-update pages are not cut. |
| `http.max_header_kib` | `64` | Largest request headers, in KiB. |
| `diagnostics.requests` | `20` | Recent guest requests the Diagnostics page keeps. |
| `inventory.max_age_s` | `5` | Seconds to keep the entity, device and area lists between HA registry changes. |
| `approval.pending_ttl_min` | `10` | Minutes a new device waits for approval. |
| `approval.max_pending` | `3` | Waiting device requests per link. |
| `approval.max_devices` | `10` | Approved devices per link. |
| `approval.events_per_hour` | `5` | Device request events per link per hour. |
| `approval.touch_throttle_min` | `5` | Least minutes between "last seen" updates of a device. |
| `approval.persistent_notification` | `true` | Show an HA notification for each device that waits. It goes away when the request ends. |
| `approval.admin_poll_s` | `5` | Seconds between checks for waiting devices while the app's page is open and visible. |
| `ha.call_timeout_s` | `10` | Seconds to wait for HA to run a guest action. |
| `ha.event_timeout_s` | `5` | Seconds to wait for HA to take an event the app sends. |
| `streams.recheck_s` | `5` | Seconds between checks that an open page may still see its link. A revoke ends the page's updates within this time. |
| `streams.heartbeat_s` | `25` | Seconds between keep-alive messages, so proxies keep the connection open. |
| `streams.retry_after_s` | `30` | Seconds a page waits when its link has the most open pages. |
| `streams.max_per_ip` | `3` | Open live-update pages per link from one client address, inside `rate_limits.max_streams_per_link`. One address can't take every slot. |
| `audit.enabled` | `true` | Write `/data/audit.jsonl`. The app log gets each record either way. |
| `audit.max_file_kib` | `2048` | Size in KiB at which the audit file starts over. One older file is kept. |
| `audit.max_denials_per_min` | `120` | Refused guest requests recorded each minute, from all addresses together. Past it, one `guest.denials.capped` record counts the rest. Admin, device and allowed records are always kept. |

These options have no default. They are empty until you set them:

| Option | Meaning |
|---|---|
| **`public_base_url`** | The address guests use, such as `https://guests.example.com`. Link URLs and QR codes start with it. |
| **`music_assistant.url`** | Address of Music Assistant. Empty turns the party off. |
| **`music_assistant.token`** | A long-lived Music Assistant token. A password field. The app never shows it again. |
| **`guest.welcome`** | A line of plain text under the title. |
| **`guest.accent_color`** | A color for the guest page, such as `#1e88e5`. |

## Ports and reverse proxy

| Port | Use |
|---|---|
| 8099 | Admin screens. Reached only through the HA sidebar (ingress). The app refuses any other peer. |
| 8888 | Guest links, inside the app. The host port is **8898** by default. Change it in the app's Network section. |

Never expose the admin port. Only expose the guest port.

### Nginx Proxy Manager

1. Add a proxy host for `guests.example.com`.
2. Forward to your HA host on port `8898`, scheme `http`.
3. Turn on a certificate and Force SSL.
4. Add the NPM address to `trusted_proxies`. Read the address from **Diagnostics** or the debug log. Do not guess it.

The app's live-update stream asks for no proxy buffering by itself. You do not need to change NPM for it.

### cloudflared plus NPM

When traffic goes Cloudflare, cloudflared, NPM, app:

1. Point the tunnel's public hostname at NPM, and NPM at the app as above.
2. Add both cloudflared and NPM to `trusted_proxies`.
3. Open **Diagnostics** after a test request. **Untrusted forwarding** must be gone and **Client** must show the guest's address.

The chain sends `X-Forwarded-For`, and the app reads only that header and `X-Forwarded-Proto`. It ignores `Forwarded`, `X-Real-IP` and `CF-Connecting-IP`.

## Security notes

### What the app protects against

- **Guessing a link.** Random slugs have 128 bits. Wrong links, passwords and request floods are rate limited.
- **Guessing a password.** Each address gets a few tries before it is locked out. Past the per-link limit, the link takes one try per delay from all addresses together, so guessing from many addresses stays slow.
- **Doing more than granted.** Each request is checked against the exact entity and action. The app builds the HA call itself, with exactly one entity. A guest can never name an area, a device, a label or a code.
- **Checks run in a fixed order**, and each one fails closed: rate limit, link, request shape (JSON and same-origin), link window, password session, device approval, grant and access rules, one-time use, then the HA call.
- **Seeing too much.** A guest sees only the entities a rule grants, and only a short list of attributes per domain. Locations, user IDs and picture URLs are never sent.
- **Cross-site tricks.** Guest cookies are `HttpOnly` and `SameSite=Strict`, and `Secure` over HTTPS. Guest POSTs need JSON and a same-origin header. The page runs under a strict content policy with no inline script.
- **The admin screens.** The admin port accepts only the Supervisor ingress peer, and only JSON for writes.
- **Stored secrets.** Passwords are stored only as hashes. The files with slugs and keys are in `/data` with mode `0600`, not in your config folder.

### What it does not protect against

- **A script does whatever it does.** The app can only call the script. It cannot limit what the script does. Write scripts with care, and pick who gets them.
- **A guest who shares the link and the password.** Anyone who has both can use the link until it ends. Use device approval to make that harder, and revoke when needed.
- **A guest who fakes their location.** See **Near home**.
- **A guest who is not really local.** See **Local only**. It fails safe, but it is not a firewall.
- **A stolen device that is already approved.** Revoke the device.
- **Someone with access to the HA host or the `/data` folder.** They can read the slugs.
- **Someone who slows the password form.** Anyone with the URL can make the link slow down. Guests then wait up to `rate_limits.password_link_delay_s` seconds to sign in. Nobody is locked out by someone else's guesses. See **Passwords**.

## Files

| Place | Holds |
|---|---|
| Config folder, `/homeassistant/guest_access/` | `templates.yaml` only. It is in your HA config, so it can go in git. It holds no secrets. |
| **`/data`** | Everything that changes at run time: `links.yaml` (links, hashed passwords, approved devices, use counts, slugs), `hmac.key` (the signing key), `audit.jsonl` and `audit.jsonl.1`. Never commit these. |

Each file has a `version`. A newer app reads older files and converts them in memory. It writes nothing until something changes. An app that finds a file from a newer version refuses to write it.

Pending device requests live in memory only. They are lost on a restart.

## Troubleshooting

**The admin screen says "access only through Home Assistant".** Open the app from the HA sidebar. The admin port accepts only the HA proxy.

**A link says it is not active.** The link has ended, has not started, or was revoked. Check its window on the **Links** screen.

**A guest gets 404.** The slug is wrong, or it was rotated. Send the new URL. A link in `links.yaml` that a hand edit broke also gives 404: the app never serves a link that does not validate. The **Links** screen marks it and lists its problems, and the log has one warning for it.

**A guest gets 429.** A rate limit was hit. If many guests get it, they probably share one address. Check `trusted_proxies` in **Diagnostics**.

**The guest sees no controls.** On the **Links** screen, open the link and read its warnings. Common causes:

- The rule selects nothing, or only hidden, disabled or categorized entities. An exact entity ID counts even when hidden.
- The rule's actions are for another domain than its entities.
- A `network: local` rule while `local_access.enabled` is off.
- A template is missing or failed validation.

**A local-only control is missing at home.** Open **Diagnostics**. See **When it fails**.

**Near home never passes.** The page must use HTTPS. The browser must allow location. The accuracy must be within `location.max_accuracy_m`. HA must have a home location.

**A new device waits forever.** The request expires after `approval.pending_ttl_min` minutes. If your phone got no notification, check the blueprint automation, or approve in the app or from the HA notification. The guest can ask again.

**The party button is missing.** Check that `music_assistant.url` and `.token` are set, that the template has `party: true`, that the rule's access passes, and that party guest access is on in MA. Read the app log for "music assistant party url".

**The app does not start after a restore.** The log names the cause. Two common ones are an `hmac.key` of the wrong length (the app refuses to overwrite it) and an audit file it cannot open. A missing `hmac.key` is created fresh. A fresh key ends every session, device approval and location cookie. Guests enter their passwords again. Approved devices stay in the list but their cookies fail. In claim mode, use **Reset devices** so a device can claim the link again.

**A script gets no code.** That is by design. See **Codes: why the app never handles them**.
