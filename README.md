# Guest Access for Home Assistant

A Home Assistant app that serves links guests use to control a few things at
home. Each link is built from templates such as "House entry" or "Music
control". The app checks every action on the server against the link, its
password, the device, the network the request comes from and, if you want,
how close the guest is to home.

What it does:

- Templates pick entities by entity, area, label, device or `*` wildcard, and
  grant only the actions you list.
- Links have a validity window, optional one-time use, passwords, and
  optional device approval from your phone.
- Rules can ask for the home network ("local only", opt-in) or for a guest
  within a radius of home.
- An optional Music Assistant party link.
- Rate limits on every route and an audit log of every security decision.
- The app never handles alarm or lock codes. Scripts hold them.

Read [guest_access/DOCS.md](guest_access/DOCS.md) for the full guide.

## Credits

The idea comes from
[Limited Guest Access](https://github.com/TekniskSupport/homeassistant-addons/tree/master/limited-guest-access)
by TekniskSupport. This app is a rewrite in Go with a few changes of my own.

## Install

Add this repository to Home Assistant: Settings → Apps → App store → ⋮ →
Repositories, then paste `https://github.com/igrybkov/ha-guest-access`.
Then follow the Install and Quick start sections of
[guest_access/DOCS.md](guest_access/DOCS.md).

## Develop

```sh
make test lint    # go test -race and staticcheck
make vet test-ui  # go vet and the UI tests
make build        # ./guest-access
make run-dev      # local run, see below
make docker       # multi-arch image, needs buildx
```

`make run-dev` serves the admin port on `127.0.0.1:8099` and the guest port
on `127.0.0.1:8888`. Data goes to `./data` and config to `./dev-config`. Put
`HA_URL` and `HA_TOKEN` in `.env` (git ignores it). App options go in
`data/options.json`; keys you leave out keep their defaults. The admin port
takes any peer in this mode, since there is no ingress proxy.

Never point it at a live Home Assistant with real locks or alarms. Tests use
the fakes in `internal/ha/hatest`.

[AGENTS.md](AGENTS.md) describes the code layout and the rules for changes.

## Release

Releases start from `main`, never from a tag. Bump `version` in
`guest_access/config.yaml` and add a `## X.Y.Z` section to
`guest_access/CHANGELOG.md`. On push, the Release workflow tests, builds and
pushes the image, then creates the tag and the GitHub release. A failed
build leaves no tag. A version ending in `-dev` is never released.

## Dependencies

Renovate opens update PRs on Monday mornings. Minor, patch and digest
updates merge by themselves once CI passes. Major updates wait for review.
