# Coming from docker compose

systemd-compose takes compose's verbs and most of its service keys. A
service is a program on this host, not a container. systemd starts it,
restarts it, and keeps its logs in the journal.

## Porting a compose file

1. Copy `docker-compose.yml` to `systemd-compose.yaml`.
2. Run `systemd-compose config`. It names every key that has no
   counterpart here, with what to write instead, all at once.
3. Replace `image:` with `command:`: the program as it is installed on this
   host. If the program is built from source, put the steps in `build:`.
4. Drop `ports:`. A program binds its own port. For socket activation, use
   `listen:`.
5. Move the variables your shell sets into the `.env` beside the yaml.
   Nothing from your shell reaches a service, and interpolation reads only
   that `.env`.
6. Run `systemd-compose up --dry-run` to see the plan, then `up`.

## Keys

| compose | systemd-compose |
|---|---|
| `command`, `entrypoint` | the same; the program is looked up on this host when `up` renders the units |
| `working_dir` | the same, relative to the yaml |
| `environment`, `env_file` | the same, with one difference below |
| `restart` | the same words; `unless-stopped` is `always`, and `on-failure:N` is refused |
| `depends_on` | the same forms and conditions |
| `healthcheck` | `test`, `interval`, `timeout` and `start_period`; no `retries`, no `CMD` prefix |
| `profiles` | the same |
| `build` | `build: {run, creates}`: steps that make the program, not an image |
| `x-` keys, anchors, `<<:` | the same |
| `image`, `ports`, `volumes`, `networks` | no counterpart: a service is a program on this host. `config` names every other compose key, with what to write instead. |

Keys of its own: `schedule:` (a timer), `oneshot:` (a job that ends),
`listen:` (socket activation), `on_change:` and `unit:` (raw systemd
sections). [config.example.yaml](config.example.yaml) shows each.

## Verbs

The verbs are compose's. `systemd-compose help` lists them. A verb of
compose's with no counterpart (`pull`, `pause`, `scale`, `cp`...) is
answered with what to use instead.

Flags that differ:

- `up` is always detached, and waits for every service to start.
  `-d`, `--detach` and `--wait` are accepted.
- `up` retires services the yaml no longer declares, without
  `--remove-orphans`. A running one needs `up --force`.
- `up` takes no service names. `start SERVICE` starts one.
- `down` keeps the rendered unit files in `.systemd-compose/`. There are no
  volumes, so `-v` is refused.
- `kill` sends SIGTERM. Compose's sends SIGKILL; use `-s KILL` for that.
- `stop -t` is refused: a service's stop timeout is set in its unit,
  `unit: {Service: {TimeoutStopSec: 30s}}`.
- `run` starts no dependencies, and `--rm` and `--no-deps` are accepted.

## Behaviour that differs

- Interpolation reads only the `.env` beside the yaml. An unset variable
  with no default is an error, not an empty string.
- `command:` runs without a shell. `;`, `|`, `&&` and redirections are
  refused; write `[sh, -c, '...']` for a shell line.
- A value from an `env_file` wins over `environment:`, the opposite of
  compose. The two may not set one key to different values.
- `depends_on` without a condition is `Wants=`: the dependent starts even
  if its dependency fails. `required: true` makes it `Requires=`.
- A healthcheck gates the start only. Nothing probes the service after it
  has started, so `ready` in `ps` means it passed once.
- There is no `version:` key.
