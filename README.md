# Guest Access for Home Assistant

A Home Assistant app that serves links guests use to control a few things at
home. Each link is built from templates such as "House entry" or "Music
control". The app checks every action against the link, the network the
request comes from and, if you want, how close the guest is to home.

The first version is in progress.

## Credits

The idea comes from
[Limited Guest Access](https://github.com/TekniskSupport/homeassistant-addons/tree/master/limited-guest-access)
by TekniskSupport. This app is a rewrite in Go with a few changes of my own.

## Install

Add this repository to Home Assistant: Settings → Apps → App store → ⋮ →
Repositories, then paste `https://github.com/igrybkov/ha-guest-access`.
There is no release yet.

## Develop

```sh
make test lint    # go test -race and staticcheck
make build        # ./guest-access
make docker       # multi-arch image, needs buildx
```

`go run ./cmd/guest-access` serves the admin port on `:8099` and the guest
port on `:8888`. `LISTEN` and `GUEST_LISTEN` change them, and `LOG_LEVEL`
sets the log level.

## Release

Releases start from `main`, never from a tag. Bump `version` in
`guest_access/config.yaml` and add a `## X.Y.Z` section to
`guest_access/CHANGELOG.md`. On push, the Release workflow tests, builds and
pushes the image, then creates the tag and the GitHub release. A failed
build leaves no tag. A version ending in `-dev` is never released.

## Dependencies

Renovate opens update PRs on Monday mornings. Minor, patch and digest
updates merge by themselves once CI passes. Major updates wait for review.
