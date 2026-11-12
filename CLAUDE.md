# yonder

Go CLI that connects a person's own machines over Tailscale and SSH. Module
`github.com/zacharytgray/yonder`. The folder may still be named `patchbay`; the product,
command and module are `yonder`.

`docs/design.md` is the spec: layout, file paths, record schema, the `_exec` envelope, exit
codes and the `ls --json` shape. Change code and design.md together. If you deviate from
the spec, update design.md in the same change and say why.

## Hard rules

- This repo is public. No real hostnames, IPs, tailnet names, emails or personal paths in
  code, tests, fixtures or docs. Use example values: `gpu-box`, `laptop`, `100.64.0.x`,
  `example.ts.net`, user `me`.
- Tests never touch the real home. Use `YONDER_HOME` temp dirs, `YONDER_TAILSCALE_JSON`
  fixtures, and a temp `HOME` for anything that spawns a shell.
- Tests never ssh anywhere except the docker containers in the integration test.
- Deps: spf13/cobra, pelletier/go-toml/v2, charmbracelet/huh (init prompts only). Ask
  before adding others.
- Comments are short lowercase fragments, sparse.

## How to verify

```
go build ./...
go vet ./... && go vet -tags integration ./...
go test -race ./...
make build && ./bin/yonder --help && ./bin/yonder version
YONDER_HOME=$(mktemp -d) YONDER_TAILSCALE_JSON=internal/tailnet/testdata/status.json \
  sh -c './bin/yonder init --yes --no-skill && ./bin/yonder ls && ./bin/yonder run laptop -- echo hi'
```

`make lint` needs golangci-lint v2. `make integration` needs docker and skips without it;
CI runs it on ubuntu.
