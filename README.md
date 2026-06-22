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

## Edge Provisioning Management

`rticloud edge-provisioning` is the management-plane command group for administering
Edge Systems (Provisioning Services), DDS Security templates, campaigns, and devices.

### Setup workflow

Before devices can enroll, the following setup order must be followed:

```
Governance Template ──┐
                       ├──> Domain Template ──> Campaign ──> Device Enrollment
Permissions Template ──> Participant Template ──┘
```

The default templates (`domain-protection-v1`, `permissions-allow-all-v1`,
`edge-default-tag`, `default-participant-template`) are seeded automatically
when an Edge System is created. Custom templates must be created explicitly.

### Edge System lifecycle

```sh
# Create a Provisioning Service (no governance XML at creation — seeded by the platform)
rticloud edge-provisioning service create --name demo

# List / query / delete
rticloud edge-provisioning service list
rticloud edge-provisioning service query --name demo
rticloud edge-provisioning service delete --name demo
```

### Template management (admin)

```sh
# Governance templates
rticloud edge-provisioning governance-template create \
  --service demo --name my-gov --governance-file governance.xml
rticloud edge-provisioning governance-template list  --service demo
rticloud edge-provisioning governance-template delete --service demo --name my-gov

# Permissions templates
rticloud edge-provisioning permissions-template create \
  --service demo --name my-perms --permissions-file permissions.xml
rticloud edge-provisioning permissions-template list  --service demo
rticloud edge-provisioning permissions-template get   --service demo --name my-perms
rticloud edge-provisioning permissions-template delete --service demo --name my-perms

# Domain templates
rticloud edge-provisioning domain-template create \
  --service demo --domain-id 29 --governance-ref domain-protection-v1
rticloud edge-provisioning domain-template list  --service demo
rticloud edge-provisioning domain-template delete --service demo --template-id "29:edge-default-tag"
```

### Participant templates

```sh
# Create a participant template (references a Permissions Template by name)
rticloud edge-provisioning participant-template create \
  --service demo --name sensors --permissions-ref permissions-allow-all-v1

# List / get / delete
rticloud edge-provisioning participant-template list   --service demo
rticloud edge-provisioning participant-template get    --service demo --name sensors
rticloud edge-provisioning participant-template delete --service demo --name sensors
```

### Campaigns

```sh
# Create a campaign (domainTemplateId is required)
# devices.csv: serial,macs[,name]  or  devices.json: [{serial, macs, name}]
rticloud edge-provisioning campaign create \
  --service <es-id> --participant-id <participant-id> \
  --domain-template-id edge-default-tag \
  --devices-file devices.csv

# List / list-devices / delete
rticloud edge-provisioning campaign list         --service <es-id> --participant-id <pid>
rticloud edge-provisioning campaign list-devices --service <es-id> --participant-id <pid> --campaign-id <cid>
rticloud edge-provisioning campaign delete       --service <es-id> --participant-id <pid> --campaign-id <cid>
```

### Devices

```sh
# List all devices across all participants
rticloud edge-provisioning device list --service <es-id>

# Revoke a device
rticloud edge-provisioning device revoke \
  --service <es-id> --participant-id <pid> --campaign-id <cid> --serial SN-001
```

## Edge-Sync Agent

`rticloud edge-sync agent` is a long-lived foreground process that autonomously manages
the full security-artifact lifecycle for one or more Participant Profiles against an RTI
Provisioning Service. It replaces the need to manually invoke `enroll`, `identity`,
`permissions`, `psk`, and `crl` on a schedule; those subcommands remain available as
manual escape hatches.

### Starting the agent

```sh
# Interactive first-run — wizard prompts for campaign token, device name,
# serial number, and MAC addresses, then enrolls and starts monitoring.
rticloud edge-sync agent

# Override the CRL refresh interval (default 5 minutes):
rticloud edge-sync agent --crl-interval 10m
```

The agent runs in the foreground. Use systemd (`Type=simple`), launchd, or a container
runtime for process supervision.

### First-run wizard

On first launch (no profiles in `.connext/`), the agent starts an interactive wizard:

1. **Campaign token** — paste the enrollment JWT (input is hidden).
2. **Device name** — the name registered in the inventory CSV (e.g. `pump-sensor`);
   namespaces this device's on-disk artifacts.
3. **Serial number** — auto-detected from `/sys/class/dmi/id/product_serial`;
   editable at the prompt.
4. **MAC addresses** — auto-detected from network interfaces; enter as
   comma-separated values (e.g. `AA:BB:CC:DD:EE:11, 11:22:33:44:55:22`).
5. **Confirmation** — review and confirm, or retry.

### Live status

When running on a terminal, the agent shows a live panel listing each monitored
profile and the time remaining before its next artifact renewal.

### What the agent does

1. **Enroll** — registers the device with the Provisioning Service, generating a device
   private key and CSR automatically.
2. **Fetch artifacts** — requests the identity certificate, permissions document, PSK,
   and CRL after enrollment.
3. **Proactive renewal** — renews each artifact before it expires and refreshes the CRL
   periodically (default every 5 minutes, configurable via `--crl-interval`).

The agent manages multiple profiles concurrently and persists all state under
`.connext/` so it can restart cleanly without re-contacting the Provisioning Service.

### Adding profiles at runtime

While the agent is running, additional profiles can be enrolled with:

```sh
rticloud edge-sync agent enroll \
  --campaign-token <JWT> \
  --device-name pump-sensor2 \
  --serial SN-002 \
  --mac AA:BB:CC:DD:EE:22
```

Serial and MAC are auto-detected if omitted.

### Cleaning up

To remove all enrolled profiles and start fresh:

```sh
rticloud edge-sync agent clean
```

This deletes the `.connext/` directory. The next run triggers the first-run wizard.

### Logging

Agent events are written to a UTC-timestamped log file (default
`.connext/rticloud-edge-agent.log`):

```sh
# Override the log file path:
rticloud edge-sync agent --log-file /var/log/rticloud-edge-agent.log

# Disable file logging:
rticloud edge-sync agent --log-file ""
```

> **Developers:** the renewal state machine, on-disk store layout, PSK rolling-key
> protocol, and log-event reference are documented in
> [`edgesyncagent/ARCHITECTURE.md`](edgesyncagent/ARCHITECTURE.md).
