# systemd-compose

[![ci](https://github.com/xflash96/systemd-compose/actions/workflows/ci.yml/badge.svg)](https://github.com/xflash96/systemd-compose/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/xflash96/systemd-compose?include_prereleases)](https://github.com/xflash96/systemd-compose/releases)
[![license](https://img.shields.io/github/license/xflash96/systemd-compose)](LICENSE)

Run a project's services as systemd user units, with docker compose's verbs
and a compose-like `systemd-compose.yaml`.

It is for programs that run on the host, not in containers: a web app and
its worker, a database, a scheduled job. systemd starts them, restarts
them, keeps their logs, and starts them again at boot.

[docs/config.example.yaml](docs/config.example.yaml) shows every key of
the file. [Try it](#try-it) starts with a small one.

## Features

- Compose's verbs: `up`, `down`, `ps`, `logs`, `start`, `stop`, `restart`,
  `kill`, `run`, `exec`, `build`, `config`, `top` and `ls`
  ([coming from docker compose](docs/compose.md)).
- Compose's keys where the meaning matches. A compose key with no meaning
  here is an error that says what to write instead
  ([compose's keys](docs/compose.md#keys)).
- `up` prints a plan, checks the units with `systemd-analyze verify` first,
  and restarts only the services that changed
  ([when up fails](docs/troubleshooting.md#when-up-fails)).
- `depends_on` with compose's conditions, healthchecks that gate the start and keep checking,
  and jobs that run to the end before their dependents start
  ([dependencies and healthchecks](docs/dependencies.md)).
- Scheduled jobs on systemd timers ([scheduled jobs](docs/scheduled-jobs.md)),
  and socket activation ([examples/socket](examples/socket)).
- Memory, CPU and process caps, per service and for the whole project
  (`resources` in [the keys](#writing-the-yaml)).
- Profiles ([profiles](docs/profiles.md)), and several copies of one
  project under different names ([the project name](#the-project-name)).
- `import` turns a unit you wrote by hand into a project's service
  ([from a hand-written unit](#from-a-hand-written-unit)).
- A JSON Schema, for completion and checks in an editor
  ([writing the yaml](#writing-the-yaml)).
- One static binary, with no runtime dependency. It needs systemd 248 or
  later ([install](#install)).

## How it works

`up` writes a unit file per service into `.systemd-compose/` beside the
yaml, copies the files into `~/.config/systemd/user/`, and starts the
project. For a project named `demo`:

```
demo.slice                 the cgroup every service runs in, with the project's caps
demo.target                wants every service; boot starts it
demo-SERVICE.service       one per service
demo-SERVICE.socket        for a service with listen:
demo-SERVICE.timer         for a service with schedule:
```

`down` stops and unregisters them; the files stay. `up --dry-run` prints
the plan without changing anything, and `config` prints the units. It is
ordinary systemd underneath: `systemctl --user` and `journalctl --user` can
inspect or undo anything it does.

Your user manager runs only while you are logged in, unless lingering is
on. For services that should keep running, and start at boot, turn it on
once per machine (`up` warns while it is off):

```
loginctl enable-linger
```

## Install

The install script downloads the latest release for your machine, checks
its sum, and puts the binary in `~/.local/bin` and the man page in
`~/.local/share/man/man1`:

```
curl -fsSL https://raw.githubusercontent.com/xflash96/systemd-compose/main/scripts/install.sh | sh
```

Or by hand: download the tarball for your machine and `SHA256SUMS` from the
[releases page](https://github.com/xflash96/systemd-compose/releases). In
that directory:

```
sha256sum -c --ignore-missing SHA256SUMS
tar xzf systemd-compose_*_linux_amd64.tar.gz       # or _arm64
mkdir -p ~/.local/bin && cp systemd-compose_*_linux_amd64/systemd-compose ~/.local/bin/
mkdir -p ~/.local/share/man/man1 && cp systemd-compose_*_linux_amd64/docs/systemd-compose.1 ~/.local/share/man/man1/
```

Or from source, with Go 1.24 or later:

```
go install github.com/xflash96/systemd-compose@latest    # -> $(go env GOPATH)/bin
make install                                            # from a clone: ~/.local/bin, and the man page
```

`go install` installs no man page; the binary carries the manual and the
key reference ([Documentation](#documentation)).

The directory must be on your `PATH`. The examples below use a short alias:

```
alias sc=systemd-compose
sc --version
```

## Try it

This project needs nothing but a shell. Save it as `systemd-compose.yaml` in
an empty directory called `hello`:

```yaml
services:
  ticker:
    command: [sh, -c, 'while sleep 5; do echo tick; done']
    restart: always
  greeter:
    command: [sh, -c, 'echo "hello, $$NAME"; exec sleep infinity']
    environment: {NAME: world}
    depends_on: [ticker]
  job:
    command: echo the job ran
    schedule: "*:0/1"             # every minute
```

```
cd hello
sc up                         # start ticker and greeter, and arm job's timer
sc ps                         # their units, and when job runs next
sc logs -f                    # hello, world; a tick every 5 s; the job each minute (^C ends it)
sc run greeter printenv NAME  # a one-off in greeter's environment: world
sc down                       # stop and unregister it all
```

`$$` passes a literal `$` to the shell. A single `$` would be filled in by
`up`, from a `.env` file beside the yaml.

[examples/](examples) has projects to run: a web app with a worker
and a database, scheduled backups, and a socket-activated service.

## Writing the yaml

[docs/config.example.yaml](docs/config.example.yaml) shows every key, with
its default and its valid values. Put this line at the top of your file
for completion and checks in an editor with a YAML language server:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/xflash96/systemd-compose/main/config-schema.json
```

Values may use compose's interpolation (`$VAR`, `${VAR:-default}` and the
other forms). The variables come from the `.env` beside the yaml, never from
your shell.

In `command`, `entrypoint`, `environment`, `healthcheck` and `listen`, `%`
starts a systemd specifier, such as `%h` for your home directory. Write
`%%` for a literal percent sign: `date +%%s`, not `date +%s`.

Each key of a service, with the least it takes:

| key | for example | what it does |
|---|---|---|
| `command` | `command: [python3, app.py]` | the program and its arguments, as a string or a list. There is no shell; write `[sh, -c, '...']` for one. |
| `entrypoint` | `entrypoint: [uv, run]` | words put in front of `command` |
| `working_dir` | `working_dir: web` | the directory it runs in, relative to the yaml |
| `environment` | `environment: {PORT: 8000}` | variables, as a map or a list |
| `env_file` | `env_file: app.env` | files of variables, which systemd reads. A value from a file wins over `environment:`. |
| `restart` | `restart: on-failure` | `no`, `on-failure`, `always` or `unless-stopped` |
| `depends_on` | `depends_on: [db]` | services to start first, with compose's conditions ([guide](docs/dependencies.md)) |
| `healthcheck` | `healthcheck: {test: [pg_isready]}` | a test that must pass before the service counts as started, then runs every interval ([guide](docs/dependencies.md#healthchecks)) |
| `oneshot` | `oneshot: true` | a job that runs to its end, which other services can wait for ([guide](docs/dependencies.md#jobs-that-run-first)) |
| `schedule` | `schedule: daily` | a timer that runs the service ([guide](docs/scheduled-jobs.md)) |
| `build` | `build: {run: [make], creates: app}` | the steps that make the program; `up` runs them when `creates:` is missing |
| `resources` | `resources: {memory: 512M}` | `memory`, `cpus` and `pids` caps, for a service, or at the top level for the whole project |
| `on_change` | `on_change: start-only` | keeps `up` from restarting the service when it changes |
| `listen` | `listen: 8000` | socket activation, for a program that takes its sockets from `LISTEN_FDS` ([example](examples/socket)) |
| `profiles` | `profiles: [debug]` | the profiles the service belongs to ([guide](docs/profiles.md)) |
| `unit` | `unit: {Service: {Nice: 5}}` | raw systemd sections, merged into the unit last |

## The project name

Every unit carries the project name: `demo-api.service`, `demo.target`,
`demo.slice`. Two copies of a project can run on one machine under two
names, without an edit to the yaml.

The name is the first of: `-p NAME` before the verb,
`SYSTEMD_COMPOSE_PROJECT_NAME` in the environment or in the `.env`, `name:`
in the yaml, and the directory's name.

A name is letters, digits, `_` and `-`. Units spell a `-` of the name as
`\x2d`, as `systemd-escape` does: project `my-app` has
`my\x2dapp-api.service`. A directory named `my-app` gives the name
`my_app`; `name: my-app` keeps the dash.

Outside a project (no `systemd-compose.yaml` here or above), `-p NAME`
acts on the project registered under that name, as `sc ls` lists it:
`sc -p demo logs -f`.

Renaming a project leaves the old units running. Take them down under the
old name first: `sc -p OLDNAME down`.

## How it compares

- **docker compose** runs containers from images, with their own
  filesystems and networks. systemd-compose runs programs installed on the
  host, under systemd. Use compose when you want images and isolation.
  [docs/compose.md](docs/compose.md) maps compose's keys and verbs.
- **Podman Quadlet** also turns files into systemd units, for containers:
  a `.container` or `.pod` file becomes a service, run with systemctl's
  verbs. systemd-compose runs host programs, from one file per project,
  with compose's verbs.
- **process-compose** runs a project's processes as its own children, and
  they stop when it stops. systemd-compose leaves them to systemd: no
  process of its own keeps running, the services start at boot, and they
  log to the journal.
- **Hand-written unit files** give full control. systemd-compose writes the
  same files from one yaml, groups them in a slice and a target, shows a
  plan before it changes anything, and retires the units the yaml no longer
  declares. `unit:` passes any systemd setting through.

## Status

systemd-compose is new, and its releases are 0.x. A verb or a key may
still change, and the [changelog](CHANGELOG.md) says when one does. If a
key ever has to change its meaning, an optional `version:` key will come
with the change, and a file without one will keep its meaning.

CI tests it on systemd 249 (Ubuntu 22.04) and 255 (Ubuntu 24.04).

## Moving or deleting a project

Take a project down before you move or delete its directory. Its units are
registered as that directory's: moved or deleted, they fail to start at
boot, and `up` in a new place refuses them. If you have moved or deleted
it already, `sc -p NAME down` from anywhere retires them. With
`registration: link`, move it back and run `down` there, or remove the
units by hand. Set `N` to the project
name as `sc ls` shows it, with each `-` written `\x2d`:

```
N='my\x2dapp'
systemctl --user stop "${N}[.-]*"
rm ~/.config/systemd/user/"$N"[.-]* ~/.config/systemd/user/default.target.wants/"$N".target
systemctl --user daemon-reload
```

systemd may warn during the stop that the unit files changed on disk. That
is expected.

## A project on a network filesystem

`up` copies each unit into `~/.config/systemd/user/`, so systemd loads it
at boot wherever the project lives. On NFS, FUSE or another filesystem
mounted after your user manager starts, a service whose files are there
fails to start at boot until it is mounted. Then `sc up` in the project,
or `sc -p NAME up` from anywhere, starts it; a `restart:` with a delay
retries it on its own:

```yaml
services:
  api:
    command: [python3, app.py]
    restart: {policy: always, delay: 10s}
```

`registration: link` registers links to the files in `.systemd-compose/`
instead. On such a filesystem they are missing at boot, and the project
does not start, then or once the filesystem is mounted; `up` warns about
it.

To keep the yaml in a repository there and the project on a local disk,
link the yaml into a local directory. A `systemd-compose.yaml` that is a
symlink makes a project where the link is, not where the yaml is.

```
mkdir -p ~/services/demo && cd ~/services/demo
ln -s /mnt/nfs/src/demo/systemd-compose.yaml .
sc up
```

## Local changes

`up` rewrites `.systemd-compose/` and the units it copied into
`~/.config/systemd/user/`, so do not edit those files. For a change of
your own, use a systemd drop-in. A `.conf` file in
`~/.config/systemd/user/demo-.service.d/` applies to every service of
project `demo`, and one in `demo-api.service.d/` to api alone. `up` checks
drop-ins with the units, but restarts nothing for them:

```
mkdir -p ~/.config/systemd/user/demo-.service.d
printf '[Service]\nNice=5\n' > ~/.config/systemd/user/demo-.service.d/nice.conf
sc up              # checks the drop-in and reloads
sc restart api db  # restart the services it should apply to
```

## From a hand-written unit

`sc import foo` prints a yaml that runs `foo.service` as a project's
service, then the commands that retire the unit and any unit that starts
it. The unit's directives go under `unit:` as written; `Environment=`,
`EnvironmentFile=` and `WorkingDirectory=` become their keys. The text of
a timer or socket that starts it is printed as notes, for `schedule:` or
`listen:`. Nothing changes until you run the commands:

```
mkdir -p ~/services/foo && cd ~/services/foo
sc import foo > systemd-compose.yaml
systemctl --user disable --now --quiet foo.service
rm ~/.config/systemd/user/foo.service
systemctl --user daemon-reload
sc up              # foo runs as foo-foo.service, project foo
```

## Outside a project

Outside a project, the same verbs act on your user instance through
`systemctl --user` and `journalctl --user`. `-s` (`--system`) acts on the
system instance instead, which is the default when you run as root.

```
sc ls             # every project on your user instance
sc ps -a          # every service and timer
sc logs -f foo    # journalctl --user -u foo -f
sc up foo.timer   # systemctl --user enable --now foo.timer
sc restart foo    # any other verb passes through to systemctl --user
```

## Documentation

- `man systemd-compose` ([docs/systemd-compose.1](docs/systemd-compose.1)):
  every verb, flag, file and exit status. `sc help VERB` shows one verb's
  entry, and `sc help man` the whole manual.
- [docs/config.example.yaml](docs/config.example.yaml), or `sc help yaml`:
  every key.
- Guides: [coming from docker compose](docs/compose.md),
  [dependencies and healthchecks](docs/dependencies.md),
  [scheduled jobs](docs/scheduled-jobs.md), [profiles](docs/profiles.md),
  [logs and troubleshooting](docs/troubleshooting.md).
- [examples/](examples): projects to run.

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) says how to build, test and send a
change. Bugs go to the
[issue tracker](https://github.com/xflash96/systemd-compose/issues).

Developed with agent assistance.

## License

Copyright 2026 The systemd-compose Authors. Licensed under the Apache
License 2.0; see [LICENSE](LICENSE).
