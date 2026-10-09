# Changelog

Each release of systemd-compose, newest first.

## Unreleased

- A project name may contain `-`. Units spell it `\x2d`, as
  `systemd-escape` does: project `my-app` has `my\x2dapp-api.service`
  and `my\x2dapp.slice`. A directory named `my-app` still gives the name
  `my_app`.
- Outside a project, `-p NAME` acts on the project registered under that
  name, wherever its yaml is, as compose's `-p` does: `sc -p demo logs
  -f` from any directory.

## v0.1.0

The first release of systemd-compose: run a project's services as systemd
user units, with docker compose's verbs and a compose-like
`systemd-compose.yaml`. One static binary for Linux on amd64 and arm64. It
needs systemd 248 or later, and is tested on 249 and 255.

Install from the tarballs below, as the README's Install section shows.
Each tarball's `docs/` holds the man page, the guides, and a reference of
every key.
