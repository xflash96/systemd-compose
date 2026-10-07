# Security

## Reporting a vulnerability

Report it privately, with "Report a vulnerability" on the repository's
Security tab. Do not open a public issue for it. Say what an attacker can
do, and how to reproduce it.

Fixes go into the latest release.

## What systemd-compose trusts

- It runs as you, on your systemd user manager, and does not need root.
- A project's yaml, its `.env` and its build steps run with your rights.
  Running `up` in a directory runs that project's programs, as running
  `make` there would.
- The rendered unit files may hold environment values. They are readable
  by you alone, in a directory only you can write to.
