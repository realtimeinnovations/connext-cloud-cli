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

Check for updates:

```sh
rticloud update --check
```

Install the latest release (supported on Linux and macOS):

```sh
rticloud update
```

Homebrew-managed installs should be updated with:

```sh
brew upgrade rticloud
```

On Windows, run the installer again to update the CLI.

## Known limitations on Windows

The edge-sync agent (`rticloud agent`) runs on Windows with a reduced interactive
dashboard:

- **View-only dashboard.** The in-dashboard key actions are not available on
  Windows: arrow-key navigation, Enter-to-renew, and Ctrl+A to add a participant.
  Artifact renewal still happens automatically; add participants with
  `rticloud agent enroll`.
- **Stopping the agent.** Use Ctrl+C to stop the agent.

## Cloud application downloads

Download a full editable example (XML, Python, README, manifest, generated QoS and types):

```sh
rticloud databus app get --name demo --app-name shapes --example --target-dir ./shapes
```

The root XML and Python files belong to you. The XML wraps the generated base QoS
profiles so you can add overrides while retaining the platform's connection settings.
The Python example loads generated QoS first and resolves paths from its own location.

```text
shapes/
  README.md
  shapes.xml
  shapes.py
  manifest.json
  generated/
    shapes.qos.xml
    shapes.types.xml
    security/          # Added only by client registration
```

Refresh only generated artifacts in an existing bundle, preserving all example edits
and client credentials:

```sh
rticloud databus app get --name demo --app-name shapes --generated --target-dir ./shapes
rticloud databus app get --name demo --app-name shapes --qos --target-dir ./shapes
rticloud databus app get --name demo --app-name shapes --types --target-dir ./shapes
```

Without artifact flags, `app get` refreshes generated QoS and types. `--types` uses
the application's saved type snapshot; it does not trigger live discovery.
`--example` also creates missing example files and preserves existing examples.
Use `--example --force` to explicitly replace edited examples. `--manifest` also
refreshes the manager-provided topic manifest.

Use `--zip` instead of other artifact flags to download the full example as an
archive. Secured applications need registration to receive credentials:

```sh
rticloud databus app client register --name demo --app-name shapes --client-id shapes-1 --gen-private-key
```

Registration creates `demo-shapes-shapes-1/` with the same layout and credentials
under `generated/security/`. Use `--target-dir ./shapes` to write the bundle
directly into `./shapes`, then use the same directory for generated-artifact
refreshes. Add `--zip` for an archive; with `--target-dir`, the named ZIP is saved
in that folder. Private keys and PSKs keep mode 0600, including inside ZIPs. Ordinary QoS/types refreshes never rotate or
replace credentials. Existing registration destinations or ZIPs require `--force`.

Cloud application downloads require the structured bundle response. Missing or
unsupported bundles fail before files are written or credentials are issued.
There is no fallback to the old flat layout. Update both manager and CLI to use
this feature; older CLIs fail their existing artifact checks against the new API.
