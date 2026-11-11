# Guest Access

Share links that let guests control a few things at home: unlock the door,
disarm the alarm, dim the guest room lights, join the music party.

This app is in early development and does nothing useful yet.

## Ports

| Port | Use |
|---|---|
| 8099 | Admin UI. Reached only through the Home Assistant sidebar (ingress). |
| 8888 | Guest links. Map it to a host port and point your reverse proxy there. |

## Options

| Option | Default | Use |
|---|---|---|
| `log_level` | `info` | `debug`, `info`, `warn` or `error`. |
| `ha.call_timeout_s` | `10` | Seconds to wait for HA to run a guest action. |
| `ha.event_timeout_s` | `5` | Seconds to wait for HA to take an event the app sends. |
| `streams.recheck_s` | `5` | Seconds between checks of an open guest page. A revoked link or device stops its live updates within this time. |
| `streams.heartbeat_s` | `25` | Seconds between keep-alive messages on a live update stream. |
| `streams.retry_after_s` | `30` | Seconds a guest page waits when the link has the most open pages. |
| `audit.enabled` | `true` | Write the audit file `/data/audit.jsonl`. The app log gets each record either way. |
| `audit.max_file_kib` | `2048` | Size in KiB at which the audit file starts over. One older file (`audit.jsonl.1`) is kept. |

At `debug`, the guest port logs the address of each request and the proxy
headers it carries. Use that log to set up the network rules.
