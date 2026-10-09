# Changelog

Each release of systemd-compose, newest first.

## Unreleased

- A project name may contain `-`. Units spell it `\x2d`, as
  `systemd-escape` does: project `my-app` has `my\x2dapp-api.service`
  and `my\x2dapp.slice`. A directory named `my-app` still gives the name
  `my_app`.
- `help` works offline, with no man page installed (`go install` installs
  none): `help VERB` prints the manual's entry for the verb, flags and
  all, `help yaml` every key of the file, and `help man` the whole manual.
  `help` alone lists the verbs, one line each.
- `import UNIT` prints a `systemd-compose.yaml` that runs a unit you wrote
  by hand as a project's service, and the commands that retire the unit.
- `registration: copy` registers copies of the units instead of links to
  `.systemd-compose/`, so they load at boot while the project's filesystem
  (NFS, FUSE) is not mounted yet. `up` warns when a project there is
  registered with links.
- `up` no longer prints systemctl's "Created symlink" line for each unit
  after its plan has said so. Its healthcheck line says how long it waits
  in the yaml's terms: until the test passes or `start_period` runs out,
  and what the service's `restart:` does if it fails.
- Outside a project, `-p NAME` acts on the project registered under that
  name, wherever its yaml is, as compose's `-p` does: `systemd-compose -p
  demo logs -f`.

## v0.1.0

The first release of systemd-compose: run a project's services as systemd
user units, with docker compose's verbs and a compose-like
`systemd-compose.yaml`. One static binary for Linux on amd64 and arm64. It
needs systemd 248 or later, and is tested on 249 and 255.

Install from the tarballs below, as the README's Install section shows.
Each tarball's `docs/` holds the man page, the guides, and a reference of
every key.
