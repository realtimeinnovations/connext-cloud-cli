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
