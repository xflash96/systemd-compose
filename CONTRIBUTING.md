# Contributing

## Issues and pull requests

- Report a bug with the issue template. It asks for what is needed to
  reproduce it: the versions, the yaml, `ps` and the journal.
- Open an issue before a pull request that changes behaviour, so the change
  is agreed first. A typo or a small fix can go straight to a pull request.
- Keep a pull request to one change. Its description becomes the commit
  message when it is squashed and merged.

## Code written with AI

Code written with an AI assistant is welcome, if you have read and
understood all of it. Issues, pull request descriptions and your answers
to comments are written by you, not by an agent: you must be able to
explain why the change is right. A pull request that looks generated, and
not understood, is closed.

## Building and testing

You need Go 1.24 or later.

```
make build      # ./systemd-compose
make check      # gofmt, vet, staticcheck, the tests under the race detector, the man page's markup
make e2e        # the end-to-end tests, on your own user manager
make live       # the end-to-end tests in containers that boot systemd 249 and 255
make snapshot   # the release archives, into dist/
```

The end-to-end tests (`e2e/`) drive the binary through throwaway projects
on a real systemd user manager, and leave nothing behind. `make live`
needs docker 28 or later with cgroup v2.
`make snapshot` needs [goreleaser](https://goreleaser.com).

## The code

- `main.go` starts `internal/cli`.
- `internal/cli`: the command line, the help, and the verbs outside a
  project.
- `internal/config`: reading `systemd-compose.yaml`, with interpolation and
  validation.
- `internal/render`: the unit files.
- `internal/systemd`: running `systemctl`, `journalctl` and
  `systemd-analyze`, and reading what they say.
- `internal/project`: the verbs inside a project, one file per verb.
- `internal/probe`: the healthcheck probe.
- `internal/testenv`: what every package's tests share.

## Conventions

- A test sits beside the code it tests, and is named
  `Test<Thing>_<Behaviour>`.
- A comment says what the code does and why, as a fact that holds now.
- An error message says what is wrong and what to write instead, in the
  terms of the yaml or the command line.
- A new key goes into `internal/config/spec.go`, then the parser,
  `docs/config.example.yaml`, the man page's THE PROJECT FILE and, for a
  key of a service, the README's key table. Tests fail when one of them is
  missing it.
  `UPDATE_SCHEMA=1 go test ./internal/config -run TestSchema_IsGenerated`
  writes `config-schema.json` again from the spec.
- A new verb or flag goes into the help text and the man page. A new verb
  also goes into the README's features.
- Text for users is plain: one fact per sentence.

## Releases

Before 1.0, the middle number goes up only for a change that breaks a yaml
or a command line that worked before. Anything else, new features too, is
a release of the last number, such as `v0.1.1`.

A maintainer adds the version's section to `CHANGELOG.md`, then pushes the
tag, such as `v0.1.1`. CI tests the tag, builds the archives with
goreleaser, and publishes them with that section as the release notes.
