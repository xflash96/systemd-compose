# Dependencies and healthchecks

`depends_on:` orders a project's services and ties them together.
`healthcheck:` decides when a service counts as started, so that its
dependents start after it is ready. [examples/webapp](../examples/webapp)
uses both.

## depends_on

A list is the default edge to each service named:

```yaml
  worker:
    command: node worker.js
    depends_on: [db, api]
```

The map form sets the edge per service:

```yaml
  api:
    command: node server.js
    depends_on:
      migrate: {condition: service_completed_successfully}
      db: {required: true, restart: true}
```

Each edge becomes systemd directives in the dependent's unit:

| in the yaml | in the unit | what it does |
|---|---|---|
| `[db]`, or `db: {}` | `After=` `Wants=` | Starting the dependent starts db first. The dependent starts even if db fails. |
| `required: true` | `After=` `Requires=` | The dependent does not start if db fails to start. Stopping db stops the dependent. |
| `restart: true` | `PartOf=` added | Restarting or stopping db restarts or stops the dependent. |
| `condition: service_healthy` | `Requires=` | The dependent waits until db's healthcheck passes. |
| `condition: service_completed_successfully` | `Requires=` | The dependent waits until db has exited 0. db must be `oneshot: true`. |

`stop`, `start` and `restart` say which other services they took along.

## Healthchecks

```yaml
  api:
    command: node server.js
    healthcheck:
      test: [curl, -sf, http://127.0.0.1:8080/health]
      interval: 2s        # default 2s
      timeout: 5s         # default 5s
      start_period: 60s   # default 60s
```

`test:` is a program and its arguments, run without a shell. Write
`[sh, -c, '...']` for a shell line. It runs with the service's
environment, so `[sh, -c, 'curl -sf http://127.0.0.1:$$PORT/health']`
reads the port the service reads. It runs every `interval`, each run
limited to `timeout`, until it exits 0 or `start_period` is over.

While it runs, the service is starting: `ps` shows it `activating` with
HEALTH `starting`. When the test passes, the service is started, HEALTH
says `ready`, and its dependents start. `up` waits for it, and says how
long it may wait.

If `start_period` ends first, the start fails. The service's `restart:`
policy then decides whether systemd tries again, and `up` exits 1 naming
the service. The test's last output is in `systemd-compose logs SERVICE`.
If the service's program exits while the test is still waiting, the
start fails at once.

## Without a healthcheck

A service without one counts as started once its program has run 3
seconds. `up` names one that fails or keeps restarting before that.

A program that tells systemd when it is ready can say so itself, with
`unit: {Service: {Type: notify}}`.

## Jobs that run first

A service with `oneshot: true` is done when its program exits 0. It then
stays `active (exited)`: a later `up`, or a restart of a dependent, does
not run it again. `up` runs it again when its definition changes. A
migration is the usual case:

```yaml
  migrate:
    command: [node, migrate.js]
    oneshot: true
  api:
    command: node server.js
    depends_on:
      migrate: {condition: service_completed_successfully}
```
