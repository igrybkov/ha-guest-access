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

At `debug`, the guest port logs the address of each request and the proxy
headers it carries. Use that log to set up the network rules.
