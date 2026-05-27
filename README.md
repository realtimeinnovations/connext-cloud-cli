# RTI Connext Cloud CLI

`rticloud` is the command-line interface for RTI Connext Cloud. Use it to manage cloud resources, connect your systems, and inspect live data.

## Limited Preview

Connext Cloud is currently in limited preview.
This preview is not intended for production use unless explicitly authorized by RTI.

## Installation

### Linux

```sh
curl -fsSL https://raw.githubusercontent.com/realtimeinnovations/connext-cloud-cli/main/scripts/install.sh | sh
```

To install to a custom directory:

```sh
curl -fsSL https://raw.githubusercontent.com/realtimeinnovations/connext-cloud-cli/main/scripts/install.sh | env INSTALL_DIR="$HOME/.local/bin" sh
```

The curl installer verifies the downloaded archive against the published `checksums.txt` before installing.

### macOS

Install with Homebrew (recommended):

```sh
brew tap realtimeinnovations/tap
brew install rticloud
```

Or with the curl installer, see the Linux instructions above.

### Windows

In PowerShell:

```powershell
irm https://raw.githubusercontent.com/realtimeinnovations/connext-cloud-cli/main/scripts/install.ps1 | iex
```

To install to a custom directory:

```powershell
iex "& { $(irm https://raw.githubusercontent.com/realtimeinnovations/connext-cloud-cli/main/scripts/install.ps1) } -InstallDir C:\tools"
```

The installer verifies the downloaded archive against the published `checksums.txt`, adds the install directory to your user PATH, and installs PowerShell completions.

### Manual download

Pre-built binaries for all platforms are available on the [releases page](https://github.com/realtimeinnovations/connext-cloud-cli/releases). Download the archive for your platform, extract the `rticloud` binary, and place it on your PATH.

## Development

### Building

```sh
cp .env.example .env
# edit .env and set AUTH0_CLIENT_ID
./scripts/build.sh
```

### Testing

```sh
go test ./...
```

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
rticloud --version
```

### Pointing the CLI at a local Manager

By default the CLI resolves the API host from `~/.rticloud/config.json` (set by
`rticloud configure --region <region>`), which maps to a cloud endpoint such as
`https://us-west-2.cloud.dev-rti.com/api/v1`.

When developing against a local Manager instance you can override the API
host with an environment variable:

```sh
# Point all CLI API calls to the local Manager (e.g. started on port 8090).
export CONNEXT_CLOUD_API_HOST="http://localhost:8090"
```

With this variable set, every `rticloud` command (e.g. `rticloud edge-system list`) 
will hit the local Manager instead of the cloud endpoint.
