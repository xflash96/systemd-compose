# Scheduled backups

Two jobs in place of a crontab: backup archives `notes/` every night at
02:30, and prune deletes archives older than two weeks once a day.

```sh
cd examples/backups
systemd-compose up          # arms both timers; nothing runs yet
systemd-compose ps          # each job's next run
systemd-compose run backup  # run it now, in the foreground
ls archive
systemctl --user start backups-backup.service  # run it as the timer does
systemd-compose logs backup
systemd-compose down
```

What it shows:

- `schedule:` as a calendar string, and in the map form with
  `persistent:` and `randomized_delay:`.
- A shell line as `[sh, -c, '...']`, with `$$` for a literal `$` and `%%`
  for a literal `%`.
- `run` on a scheduled job: the job, now, with its output on the
  terminal. `systemctl --user start` on its service runs it as the timer
  does, with its output in `logs`.
