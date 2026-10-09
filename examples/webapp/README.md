# A web app, a worker and a database

The api serves a list of books from a SQLite database. A POST queues a
book, and the worker catalogues it. migrate creates the database before
the api starts. It needs python3.

```sh
cd examples/webapp
systemd-compose up
systemd-compose ps
curl -s localhost:8080/books
curl -s -d '{"title": "Dune"}' localhost:8080/books
systemd-compose logs worker
systemd-compose down
```

`API_PORT=8081` in a `.env` beside the yaml moves the api to another
port.

What it shows:

- `oneshot:` with `condition: service_completed_successfully`: migrate
  runs to the end before the api starts.
- `healthcheck:` with `condition: service_healthy`: up waits until the
  api answers, and the worker starts after that. The check then runs
  every 30s while the api runs: `ps` says `healthy`, or `unhealthy` after
  3 failed in a row.
- `${API_PORT:-8080}`: a value from the `.env`, with a default.
- `resources:` at the top: one memory cap for the whole project.
