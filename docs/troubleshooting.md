# Logs and troubleshooting

## Logs

A project's services write to the systemd journal. `logs` reads it:

```
systemd-compose logs                  # every service, and systemd's lines about them
systemd-compose logs -f api           # follow one service
systemd-compose logs --tail 50 api    # the last 50 lines
systemd-compose logs --since 10m      # compose's spans, or journalctl's times
systemd-compose logs -g ERROR api     # any journalctl flag: -g greps
systemd-compose logs --no-log-prefix  # the messages alone
```

`logs` reads the journal itself and needs no running user manager, so it
works from cron or `su` too.

With journalctl itself, a service's unit is `PROJECT-SERVICE.service`:
`journalctl --user -u demo-api.service`. That misses a line printed by a
child process that exits at once, such as a command in a shell loop,
because journald cannot tell its unit. `logs` also matches each line's
tag, `PROJECT-SERVICE`, and shows those lines.

## When up fails

`up` refuses before it changes anything when the yaml or the rendered
units are wrong. The message names the line of the yaml. A refusal from
`systemd-analyze verify` names lines of the rendered units, which
`systemd-compose config` prints.

When a service does not come up, `up` prints the project's table, then a
line per service that is not up, with the reason:

- `failed to start (it exited with status 1)`: the program failed. Its
  output is in `logs SERVICE`.
- `keeps restarting`: the program exits again and again, and `restart:`
  restarts it.
- `its healthcheck did not pass`: the test never exited 0 within
  `start_period` plus `timeout`. The test's last output is in
  `logs SERVICE`.
- `did not start: it needs db, which is not up`: a dependency failed
  first; its own line comes before.

## ps

```
UNIT                    LOAD    ACTIVE  SUB      HEALTH  REGISTERED
demo-api.service        loaded  active  running  ready   linked
```

- ACTIVE and SUB are systemd's states. A `oneshot` that has run is
  `active` `exited`.
- HEALTH is shown for a service with a healthcheck: `starting`, `ready`
  (the test passed once), `probe failed`, `program exited`, `failed` or
  `restarting`.
- REGISTERED is `linked` for a service, `enabled` for the target, which
  boot starts. A scheduled job's timer shows its next run.

## Changes that are not applied

`up` restarts a service whose definition changed. It leaves one running
with its old definition when the service has `on_change: start-only`, or
with `up --no-recreate`. The plan then says `changed (not applied)`, and
says it again at every `up`, until you restart the service:

```
systemd-compose restart api
```

## The user manager

A project runs on your systemd user manager. Without lingering, the
manager stops at your last logout, and with it every project, and it
does not start at boot. `up` warns while lingering is off. Turn it on
once per machine:

```
loginctl enable-linger
```

Outside a login session, as from cron or `su`, `systemctl --user` needs
`XDG_RUNTIME_DIR=/run/user/$(id -u)` to reach the manager. The verbs say
so when they cannot reach it.

## resources: cpus on systemd 249

`cpus:` needs the cpu cgroup controller delegated to your user manager.
systemd 249, as on Ubuntu 22.04, delegates only memory and pids, and `up`
refuses `cpus:` there. Root can delegate the rest with a drop-in:

```
sudo mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' | sudo tee /etc/systemd/system/user@.service.d/delegate.conf
sudo systemctl daemon-reload
```

It applies when your user manager next starts: at boot, or after `sudo
systemctl restart user@$(id -u).service`, which stops everything your user
manager runs.

## Another verb is running

`up`, `down`, `build`, `start` and `restart` of one project run one at a
time. A second one says which verb holds the project, with its pid. It
continues when the first is done.

## Looking with systemd's own tools

```
systemd-compose status api             # systemctl --user status demo-api.service
systemctl --user cat demo-api.service  # the unit as systemd loaded it, with drop-ins
systemd-compose top                    # systemd-cgtop on the project's slice
systemd-compose ls                     # every project registered on this user manager
```
