# Connext Cloud CLI

Connext Cloud CLI is the command-line interface for RTI Connext Cloud.

The installed command is:

```sh
rticloud
```

## Limited Preview

Connext Cloud is currently in limited preview.
This preview is not intended for production use unless explicitly authorized by RTI.

## Installation

```sh
curl -fsSL https://raw.githubusercontent.com/realtimeinnovations/connext-cloud-cli/main/scripts/install.sh | sh
```

The curl installer verifies the downloaded release archive against the published `checksums.txt` before installing.

To install without writing to `/usr/local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/realtimeinnovations/connext-cloud-cli/main/scripts/install.sh | env INSTALL_DIR="$HOME/.local/bin" sh
```

On mac OS you can also install with brew:

```sh
brew tap realtimeinnovations/tap
brew install rticloud
```

Windows support not available yet.

## Usage

First-time setup:

```sh
rticloud configure
```

Show CLI help:

```sh
rticloud --help
```

Print build metadata:

```sh
rticloud version
```

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
