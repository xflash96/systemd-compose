# Examples

Each directory is a project: cd into it and run `systemd-compose up`.
Each README says what to try.

- [webapp](webapp): a web app with a worker and a database. Shows a oneshot
  that runs first, a healthcheck that dependents wait for and that keeps
  checking, and `.env` values.
- [backups](backups): scheduled jobs in place of a crontab.
- [socket](socket): a socket-activated service that restarts without
  refusing a connection.
