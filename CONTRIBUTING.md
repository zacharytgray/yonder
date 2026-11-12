# Contributing to Yonder

Bug reports, ideas and pull requests are welcome. Yonder is small on purpose, so the easiest
changes to merge keep it that way: one person's own machines, plain SSH over Tailscale, no
daemon, no server, no account.

## Before you start

- Read [docs/design.md](docs/design.md). It is the spec: file layout, the machine record, the
  `_exec` envelope, exit codes, the `ls --json` shape and the trust model.
- If your change alters behavior, files, flags or output, change `docs/design.md` in the same
  pull request. If it departs from the spec, say why in the pull request.
- For anything bigger than a fix, open an issue first so we can agree on the shape.

## Ground rules

- **The repo is public.** No real hostnames, IPs, tailnet names, emails or personal paths in
  code, tests, fixtures or docs. Use example values: `gpu-box`, `laptop`, `100.64.0.x`,
  `example.ts.net`, user `me`.
- **Tests never touch your real home.** Use a `YONDER_HOME` temp dir, a `YONDER_TAILSCALE_JSON`
  fixture, and a temp `HOME` for anything that spawns a shell.
- **Tests never ssh anywhere** except the docker containers in the integration test.
- **Dependencies:** spf13/cobra, pelletier/go-toml/v2 and charmbracelet/huh (init prompts
  only). Ask in an issue before adding another.
- **Comments** are short lowercase fragments, and sparse.

## Build and test

You need Go (the version in `go.mod`). Docker is optional.

```sh
go build ./...
go vet ./... && go vet -tags integration ./...
go test -race ./...
make build && ./bin/yonder --help && ./bin/yonder version
```

A quick end-to-end run against a fake tailnet, without touching your real setup:

```sh
YONDER_HOME=$(mktemp -d) YONDER_TAILSCALE_JSON=internal/tailnet/testdata/status.json \
  sh -c './bin/yonder init --yes --no-skill && ./bin/yonder ls && ./bin/yonder run laptop -- echo hi'
```

`make lint` needs golangci-lint v2. `make integration` starts two SSH containers as a fake
fleet; it needs Docker and skips cleanly without it. CI runs both.

## Pull requests

- Keep each one to one change.
- Write the subject in plain present or past tense, like "Fix the offline exit code for jobs".
- Fill in the checklist in the template. Say what you ran, not just that it passed.

## Security problems

Don't open a public issue. See [SECURITY.md](SECURITY.md).
