# AGENTS.md

This is a Go CLI for RTI Connext Cloud. The binary entry point is
`cmd/rticloud/main.go`, and the command tree is built with Cobra in
`cli/parser.go`.

## Repo Map

- `app/`: runtime wiring and version output.
- `auth/`, `config/`, `cloudapi/`: login, local config, and API client code.
- `commands/`: resource command implementations used by the Cobra layer.
- `gateway/` and `spy/`: interactive/connectivity features.
- `internal/`: shared internal helpers such as terminal handling, prompts,
  build metadata, HTTP error formatting, and Connext utilities.
- `scripts/`: install and local build scripts.

## Common Commands

```sh
go test ./...
go vet ./...
```

When building new features or when the user asks to build the CLI/local
`rticloud` binary, use the repo build script so the local binary gets the same
injected Auth0 client IDs and build metadata expected by the app. Do not
substitute `go build ./cmd/rticloud` for this; plain `go build` is only a
compile/CI parity check, not the correct local feature build.

```sh
cp .env.example .env
# edit .env
./scripts/build.sh
```

CI runs `go vet ./...`, `go test ./...`, and `go build ./cmd/rticloud`.

Run tests when developing a feature or bug fix. Do not run tests during a code
review.

## Development Notes

- Do not commit local secrets. `.env` is for local build values only.
- Maintain design consistency of the different TUI elements (forms,
  tables, and prompts) across interactive commands like `rticloud gateway`, 
  `rticloud spy`, and future ones.
- Make sure changes are portable across platforms (Linux, macOS, Windows).
