# Scheduled jobs

A service with `schedule:` is a job that a systemd timer runs. It replaces
a crontab line, and its runs are logged in the journal like any service's.

```yaml
services:
  backup:
    command: [sh, -c, 'tar -czf "archive/$$(date +%%F).tar.gz" notes']
    schedule: "*-*-* 02:30:00"
```

`up` arms the timer. It never runs the job itself. [examples/backups](../examples/backups)
is a project to try.

## The calendar

`schedule:` is a systemd calendar expression, as `OnCalendar=` takes it.
`systemd-analyze calendar 'EXPR'` prints the next time it elapses.

| crontab | `schedule:` |
|---|---|
| `@hourly` | `hourly` |
| `@daily` | `daily` |
| `@weekly` | `weekly` |
| `*/15 * * * *` | `"*:0/15"` |
| `0 3 * * *` | `"*-*-* 03:00:00"` |
| `30 2 * * 1-5` | `"Mon..Fri *-*-* 02:30:00"` |
| `0 0 1 * *` | `monthly` |
| `@reboot` | no schedule: a service with `oneshot: true` runs when the project starts, at boot too |

`up` refuses a calendar systemd cannot read. A calendar with no next run,
such as a date that has passed, is noted.

## The map form

```yaml
    schedule:
      calendar: "Mon *-*-* 07:00:00"
      accuracy: 1min          # AccuracySec=; default 10s
      persistent: true        # default true
      randomized_delay: 10min # RandomizedDelaySec=
```

- `persistent: true` runs a job that was missed while the machine was off
  at the next boot.
- `accuracy:` lets systemd move the run by up to that much, to group
  wake-ups. systemd's own default is a minute.
- `randomized_delay:` adds a random delay, so several machines do not run
  a job at the same moment.

Other `[Timer]` settings go under `unit:`, such as a run after boot:

```yaml
    schedule: hourly
    unit:
      Timer:
        OnBootSec: 15min
```

## Running and watching jobs

```
systemd-compose ps                 # each job's next run
systemd-compose list-timers        # next run, and last run
systemd-compose run backup         # run the job now, with its output on the terminal
systemd-compose logs backup        # the output of its timer's runs
systemd-compose stop backup        # disarm the timer; start backup arms it again
```

`run` exits with the job's exit status. A run that the timer started is
in the journal, and `ps` shows a failed one until the next run succeeds.
To run the job now as the timer would, with its output in `logs`, start
its unit: `systemctl --user start PROJECT-backup.service`.

## How a job runs

- Two runs of one job never overlap: the timer does not start a job that is
  still running.
- `restart: on-failure` retries a run that failed.
- A job runs as a oneshot, so `oneshot: true` beside `schedule:` changes
  nothing.

As under cron, a job gets nothing from your login shell. Its environment
is what the yaml gives it: `environment:`, `env_file:` and `working_dir:`.
The program in `command:` is found on your `PATH` when `up` renders the
units.
