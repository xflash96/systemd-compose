# systemd-compose

docker-compose verbs over systemd, with a project-local `systemd-compose.yaml`.

Inside a project directory, or any directory below one (or from anywhere,
with `-f path/to/systemd-compose.yaml` before the verb), `up` renders unit
files from the yaml, registers them with the user manager, and starts them;
`down` unregisters them; `ps`, `logs`, `start`, `stop`, `restart`, `build`,
`config` and `top` are scoped to that project. Outside a project, the same
verbs are a thin spelling of `systemctl` on the user instance, so leaving a
project directory never changes which instance answers; `--system` (or `-s`)
names the system instance, and running as root defaults to it. Nothing is
reinvented: systemd owns the processes, the journal owns the logs, and every
verb is a few `systemctl` calls you could type yourself.

One static Go binary, no runtime dependency. Linux with systemd 248 or later;
CI runs it on 249 and 255. Each release has the binary for amd64 and arm64
on the [releases page](https://github.com/xflash96/systemd-compose/releases);
or build it:

```
./install            # -> ~/.local/bin/systemd-compose (needs a Go toolchain)
alias sc=systemd-compose
```

## A project

```yaml
# systemd-compose.yaml
name: demo                    # optional; default: the directory name, sanitized
resources: {memory: 2G}       # optional; a cap on the whole project

services:
  db:
    command: postgres -D data
    restart: always
  api:
    command: node server.mjs --port 8080
    working_dir: app                    # relative to this file
    environment: {PORT: "8080"}
    env_file: [.env, {path: .env.local, required: false}]
    restart: {policy: on-failure, delay: 3s}
    depends_on:
      db: {condition: service_started, required: true, restart: true}
    healthcheck:
      test: [curl, -sf, http://127.0.0.1:8080/health]
      interval: 2s
      timeout: 5s
      start_period: 60s
    build:
      run: [npm ci, npm run build]
      creates: dist
    resources: {memory: 512M, cpus: 0.5, pids: 64}
    unit:                               # raw systemd, merged last
      Service: {TimeoutStopSec: "10"}
  worker:
    command: node worker.mjs
    depends_on: {api: {condition: service_healthy}}
    on_change: start-only               # never restarted by `up`
  backup:
    command: backup --all
    schedule: "*-*-* 03:00:00"          # a timer + oneshot pair
```

```
sc up          # render, verify, register, start; restart what changed
sc ps          # the project's units
sc logs -f api # journalctl, scoped; compose's --tail, --since 10m, -t and
               # --no-log-prefix work too
sc run api npm run migrate   # one-off, in api's environment
sc down        # stop and unregister; the rendered files stay
```

## The project name is the namespace

Every unit, the target and the slice carry the project name as a prefix,
and `up` refuses a name another registration already owns. So two copies of
one project on one machine, a popular project you cloned or two variants of
your own, coexist by taking different names, without an edit to the yaml.
The name comes from, in order:

1. `-p NAME` (or `--project-name NAME`) before the verb
2. `SYSTEMD_COMPOSE_PROJECT_NAME` in the environment
3. the same variable in a `.env` file beside the yaml (the personal, uncommitted way)
4. `name:` in the yaml (the author's default)
5. the directory's name, sanitized

The scope line every verb prints says which one won: `project variant (user
instance, name from .env)`. Both variants render into the same
`.systemd-compose/` directory side by side, and each `down` and each orphan
sweep touch only the units carrying their own name. That cuts both ways:
renaming a project leaves the old name's units registered and running, so
take them down under the old name first, `systemd-compose -p OLDNAME down`.

## Profiles

A service with `profiles: [debug]` runs only while one of its profiles is
active: `--profile debug` before the verb (repeatable), else
`SYSTEMD_COMPOSE_PROFILES=debug,other` in the environment or in the `.env`;
`--profile '*'` is all of them, and the flag replaces the variable, as
compose's does. `up` renders, registers and starts the enabled services
only, and only they boot with the project. A service of an inactive profile
is not rendered, so a tool it needs may be missing; one that an earlier `up`
registered is left running and named in the plan. Unlike compose's `down`,
`down` and a bare `stop` take every profile, so nothing of the project stays
behind; a service named on the command line is acted on whatever its
profile. `depends_on` into an inactive profile is refused when `required`,
and dropped otherwise.

## What the keys mean

Names in `depends_on` are services in this file. Everything is rendered to
`<project>-<service>.service` under a `<project>.target` and a
`<project>.slice`; the target is what boots, the slice is what `logs` and
`top` scope to.

Any value (never a key, and nothing under `unit:`, which is systemd's own
syntax) may use compose's interpolation: `$VAR`, `${VAR}`, `${VAR:-default}`,
`${VAR-default}`, `${VAR:?error}`, `${VAR?error}`, `${VAR:+other}`,
`${VAR+other}`, nested. The variables come from the `.env` beside the yaml.
`$$` is a literal `$` and reaches the program as one, so `command: sh -c
'echo $$HOME'` leaves `$HOME` to the shell.

A block several services share goes under an `x-` key (top level, or inside
a service), which the tool ignores, and comes back through yaml's anchors and
merge keys, as in compose:

```yaml
x-base: &base
  restart: unless-stopped
  environment: {ROLE: worker}
services:
  w1: {<<: *base, command: worker.sh}
  w2: {<<: [*base], command: worker.sh, environment: {ROLE: special}}
```

A key the service gives itself wins over a merged one, and the earlier of
two merged blocks wins; a merged map such as `environment:` is replaced
whole, not combined (yaml's rule).

`%` is systemd's: in `command`, `healthcheck`, `environment`, `listen` and
`schedule`, `%h`, `%t`, `%U` and the rest of systemd's specifiers (as of
systemd 248) are left for the manager to expand, so `environment:
{SOCK: "%t/app.sock"}` works on any machine; `%%` is a literal percent. Anything else
after a `%` is refused at load, since systemd would drop the whole line with
only a log message. In yaml a value that starts with `%` must be quoted:
`command: "%h/bin/tool --flag"`, not `command: %h/bin/tool --flag`. `working_dir`, `env_file` and `build` take no `%`: the
tool reads those paths itself.

| key | renders to |
|---|---|
| `command` | `ExecStart=`. A string is parsed by systemd itself; a list is one word per element, quoted for you, so spaces, quotes and `$` are plain characters. The first word is resolved to an absolute path at render time, against `~/.local/bin` and your `PATH`, and refused if not found; nothing else from your shell reaches the service. A program given through a specifier (`%h/bin/tool`) is left to systemd, and the verify gate refuses it if it does not exist. For a shell, write it out (`[sh, -c, ...]`); the raw form is `unit: Service: ExecStart:` |
| `entrypoint` | a string or a list put in front of `command`, as compose does when there is no image to override |
| `working_dir` | `WorkingDirectory=`, default the project directory |
| `environment` | `Environment=` lines; `$` is literal, `%` a specifier (above); a bare `KEY` is refused, nothing is captured from your shell |
| `env_file` | `EnvironmentFile=`; a change to the file is a change to the unit |
| `restart` | `Restart=`, `RestartSec=`. `unless-stopped` is `always`: systemd never restarts a unit you stopped, though at boot the project starts it again. `on-failure:N` is refused: systemd's start limit counts manual starts too, so set `unit: Unit: StartLimitBurst:` yourself |
| `depends_on` | `After=` + `Wants=`; `required: true` → `Requires=`; `restart: true` → `PartOf=`. `condition: service_healthy` and `service_completed_successfully` always render `Requires=`, since a `Wants=` dependent would start even when the dependency fails |
| `healthcheck` | an `ExecStartPost=` probe; the unit is not "started" until it passes, so dependents wait. `ps` shows the verdict in a HEALTH column: `starting`, `ready`, `probe failed` |
| `oneshot` | `Type=oneshot`, `RemainAfterExit=yes`; a job dependents can wait for |
| `schedule` | a `.timer` (`OnCalendar=`, `Persistent=yes`, `AccuracySec=10s`) driving a oneshot service. Runs never overlap: the ticks that fall during a run collapse into one run that starts when it ends |
| `build` | not rendered: steps run at `up` (when `creates:`, relative to `working_dir`, is missing) or `build`, in the service's own environment |
| `resources` | `MemoryMax=`, `CPUQuota=`, `TasksMax=`; at project level, on the slice, where a change applies in place and restarts nothing |
| `on_change` | `restart` (default) or `start-only`: `up` never restarts it |
| `listen` | socket activation: a `<project>-<service>.socket` with one `ListenStream=` per address (a port, `host:port`, a path relative to this file, `@abstract`), for a program that takes its sockets from `LISTEN_FDS`. `up` starts the socket and the service together; `stop` stops both, so no connection restarts it; `restart` keeps the socket open, and connections wait in its backlog. A changed address restarts both |
| `unit` | raw sections merged last (`Socket` with `listen:`, `Timer` with `schedule:`); a directive a key above already writes is an error, even one systemd would accept twice, and so is a second `Environment=` for a variable `environment:` sets |

## What `up` does

1. Renders every unit in memory and runs `systemd-analyze verify` on them in
   a staging directory. Any output refuses: a misspelled directive is a
   warning systemd would otherwise ignore.
2. Prints the plan: per unit `new`, `unchanged`, `changed`, or one of the two
   kinds of changed explained in step 4, and what will happen. `up
   --dry-run` stops here, having written nothing.
3. Runs `build` steps whose `creates:` path is missing.
4. Writes the files into `.systemd-compose/` (which ignores itself in git),
   links them with `systemctl --user link`, enables the target, starts
   everything, and `try-restart`s the changed units; a unit that fails to
   start does not keep the others on their old definition. A running unit
   whose rendered file is gone (after a `git clean`, say) is `changed (no
   baseline)`, since there is nothing to compare against. One that has been
   running since before its file was last written is `changed (not
   applied)`: an earlier `up` that failed or was interrupted before the
   restart, or an `on_change: start-only` service not restarted yet. (So
   touching a file in `.systemd-compose/` restarts its unit at the next
   `up`.)
5. Retires units registered from this directory that the yaml no longer
   declares. An active one is refused unless `--force`. When step 4 fails,
   `up` stops before this; the next `up` or `down` retires them. A socket or
   timer that a service still in the yaml dropped (its `listen:` or
   `schedule:` removed) is part of that service's change instead: never
   refused, and retired before the service restarts, so the new run does
   not inherit the old socket.

`up --force-recreate` restarts every running service whether it changed or
not; `up --no-recreate` restarts none, and a changed one stays `changed (not
applied)` for the next `up`. Neither touches an `on_change: start-only`
service: `up` never restarts one.

`down` unregisters every unit (one `disable`, then a `stop` of what was
running, then `reset-failed`), and it retires what an older version of the
yaml registered from this directory, active or not (no `--force`: `down` is
the verb that stops things), so nothing of the project stays registered. The
current units' rendered files stay, as compose keeps the compose file; a
retired orphan's file goes.

## One-off commands

`run SERVICE [CMD...]` runs a command as a transient unit in the service's
working directory, environment, env files and slice, and returns its exit
code; with no command it runs the service's own, so `run backup` fires a
scheduled job now. `exec SERVICE CMD...` is the same with the command
required. `-e KEY=VAL`, `-w DIR` and `-T` (no terminal) are compose's; a
terminal gets a pty. Once `up` has registered the service, the environment
and command are the ones systemd runs it with, specifiers expanded; before
that they come from the yaml, which may then use no specifier. Nothing else
is started: dependencies are `up`'s business (compose's `--no-deps`).

## Overrides outside the yaml

systemd's drop-in directories work on the rendered units. A `.conf` file in
`~/.config/systemd/user/<project>-.service.d/` applies to every service unit
named `<project>-…`, which is every service of the project (a project name
has no dash, so no other project matches); `<project>-<service>.service.d/`
applies to one, and timers take `<project>-.timer.d/`. `up`'s verify gate
reads them, so a typo there refuses `up` like a typo in the yaml. They are
not part of the render, though, so `up` never restarts anything for them:

```
mkdir -p ~/.config/systemd/user/demo-.service.d
printf '[Service]\nNice=5\n' > ~/.config/systemd/user/demo-.service.d/nice.conf
sc up              # verify reads it (a typo refuses), and reloads
sc restart api db  # name them: a bare restart would also restart start-only services
```

## Where it differs from compose, on purpose

- No `ports:` and no `networks:`: a host process binds what it binds.
- `${VAR}` interpolation reads the `.env` beside the yaml and never your
  shell's environment, so a unit does not depend on which shell ran `up`. A
  variable that is not set and has no default (`${VAR:-default}`) is an
  error, not compose's empty string.
- No shell in `command:`: what you write is what systemd runs. A bare `;` is
  refused; the raw form is available through `unit: Service: ExecStart:`.
- `healthcheck` is readiness only. Liveness with a restart is a `schedule:`
  service of your own, because it needs judgment a generic probe lacks. So
  `ready` in `ps` means the probe passed when the service started; nothing
  probes it afterwards (compose's `ps` shows the last of its periodic
  checks).
- `depends_on` defaults to `Wants=`. `required: true`, and the health and
  completion conditions, give `Requires=`, which also stops the dependent
  when you stop the dependency.
- A project name may not contain a dash: it is systemd's slice separator. A
  name derived from a directory such as `my-app` becomes `my_app`.
- Every string is one line. A newline anywhere in a value is refused, because
  it would render as further directives. A key given twice is refused too.
- `on_change: start-only` cannot be combined with `depends_on: {x: {restart:
  true}}` on the same service: the `PartOf=` edge would restart it anyway.
- No `version:` key; a compose-habit `version:` line gets a pointed refusal.
  Releases are 0.x, and the schema's one promise is this: if a key ever has
  to change its meaning, an optional `version:` key comes with the change,
  and a file without one keeps meaning what it means today.

## Outside a project

```
sc ls                   # every project registered here: name, services running/total, yaml
sc ps -a                # every service and timer on your user instance
sc logs -f foo          # journalctl --user -u foo -f
sc up foo.timer         # enable --now
sc restart foo          # any other verb passes through to systemctl --user
sc -s ps                # the system instance, explicitly
```

## Tests

```
ci/unit                # gofmt, vet and the suite; no systemd needed
ci/live                # a throwaway project through your user manager; nothing left behind
ci/container [24.04]   # ci/live in a container booting systemd 249 (or 255); docker 28+
ci/release v0.1.0      # the release tarballs and SHA256SUMS, into dist/
```

## License

Apache License 2.0; see [LICENSE](LICENSE).
