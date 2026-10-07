# Profiles

A service with `profiles:` runs only while one of its profiles is active.
It keeps optional services in the yaml, such as a debugging tool or a
nightly job, without starting them everywhere.

```yaml
services:
  db:
    command: postgres -D data
  pgweb:
    command: [pgweb, --listen, "8081"]
    profiles: [debug]
    depends_on: [db]
```

A service without `profiles:` is always active.

## Activating a profile

Any of these, the first one given wins:

1. `--profile NAME` before the verb, repeated for several:
   `systemd-compose --profile debug up`.
2. `SYSTEMD_COMPOSE_PROFILES=debug,nightly` in the environment.
3. The same variable in the `.env` beside the yaml.

`--profile '*'` activates every profile.

A profile set in the environment that the yaml does not have is ignored,
since the variable may be set for every project.

## What each verb does with profiles

- `up` renders, registers and starts the active services only. A service
  whose profile is off is not touched: if an earlier `up` registered it, it
  keeps running, outside the set that starts at boot. The plan says so.
- `down` and a bare `stop` act on every profile, so nothing of the project
  is left running.
- `ps` shows the active services, and says which profile a registered
  service needs when its profile is off.
- A service named on the command line is acted on whatever its profile.

## Dependencies across profiles

- An optional edge to a service whose profile is off is dropped:
  `depends_on: [pgweb]` in an always-active service starts it without
  pgweb.
- A required edge (`required: true`, or a condition) to such a service
  makes `up`, `start`, `restart`, `run`, `exec`, `build` and `config`
  refuse, naming the profile to activate. `ps`, `logs`, `stop` and `down`
  still work.
