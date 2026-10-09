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
  by hand as a project's service, and the commands that retire the unit
  and any unit that starts it, a timer or a socket, whose text it prints.
- `up` registers copies of the units in `.systemd-compose/` instead of
  links to them, so they load at boot wherever the project lives, even
  while its filesystem (NFS, FUSE) is not mounted yet; a project's next
  `up` replaces its links. `registration: link` keeps links, and `up`
  warns when a project with links is on such a filesystem.
- `up` no longer prints systemctl's "Created symlink" line for each unit
  after its plan has said so. Its healthcheck line says how long it waits
  in the yaml's terms: until the test passes or `start_period` runs out,
  and what the service's `restart:` does if it fails.
- `config` names a compose file's `build: .` and a healthcheck's `CMD` in
  the same round as the keys it does not take, each with what to write
  instead.
- A healthcheck keeps checking once the service has started, as compose's
  does: the test runs every `interval` (30s by default) while the service
  runs, and `retries` (3) failed in a row make `ps` say `unhealthy`, with
  since when and what the last check printed; `logs` has a line at each
  change. As in compose, nothing restarts the service for that. At the
  start the test runs every `start_interval` (2s), as `interval` did
  before: a yaml that set `interval` for a quicker start sets
  `start_interval` now. `ps` says `healthy` where it said `ready`.
- `start` on a scheduled service names the command that runs the job now
  as its timer would, with its output in `logs`: `systemctl --user start
  PROJECT-SERVICE.service`.
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
