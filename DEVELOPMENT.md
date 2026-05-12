# Development

## Build

```sh
cp .env.example .env
# edit .env and set AUTH0_CLIENT_ID
./scripts/build.sh
```

The build script loads `.env` if it exists and injects the Auth0 client ID at build time. Existing shell environment values take precedence over `.env`.

## Test

```sh
go test ./...
```

## Release Automation

Tagged releases are published by GitHub Actions with GoReleaser.

The `connext-cloud-cli` repository needs this Actions secret:

```text
HOMEBREW_TAP_GITHUB_TOKEN
```

The token should be a fine-grained GitHub PAT limited to `realtimeinnovations/homebrew-tap` with `Contents: Read and Write`.

The release workflow also needs `AUTH0_CLIENT_ID` as a repository variable or secret. GoReleaser injects this into the packaged CLI at build time. Developers can override the packaged value at runtime with:

```sh
export CONNEXT_CLOUD_CLI_CLIENT_ID="<client-id>"
```

To publish a release:

```sh
git tag v0.1.0
git push origin main --tags
```
