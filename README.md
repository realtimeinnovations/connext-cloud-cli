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

Authenticate using browser login (`rticloud login`), device login
(`rticloud login --device`), or a service-account API key in
`CONNEXT_CLOUD_API_KEY`. Create an API key on the
[Service Accounts page](https://cloud.rti.com/dashboard/service-accounts).
Commands use API-key authentication automatically when no valid saved access
token is available; no separate login command is required.

Run `rticloud login -h` for authentication instructions and examples. Use
`rticloud <command> -h` for a command's options, output formats, and examples.

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

## Diagnose your setup

```sh
rticloud doctor
rticloud doctor --format json
rticloud doctor --timeout 20s
```

Doctor reports the running CLI and other copies on PATH, configuration source,
credential selection, and a bounded `GET /databuses` probe. The probe verifies
permission to list databuses, not every Cloud operation. DNS/TLS/connection
failures are reported separately from rejected credentials or permissions.
A usable cached token is reused without a new API-key exchange. Cached tokens
can originate from login or an API-key exchange; the current cache format does
not record that origin. `CONNEXT_CLOUD_API_KEY` is used when no usable cached
token exists. Doctor exchanges it for a temporary token kept only in memory,
and the Cloud access check reports which token source the probe used.
The default 10-second timeout covers both the exchange and the API probe.

Local checks inspect managed Connext metadata, the Gateway and Spy launchers,
and YAML configuration in the current directory. They report the managed
installation even when `NDDSHOME` is set, and explain the external override.
They do not launch Connext or verify project resources, DDS traffic, or external
Connext installations. An absent installation is a warning; an incomplete or
invalid existing installation is a failure. Missing project configuration is
reported as skipped and optional, with no required action or setup reminder in
Next steps. Missing optional Connext installation setup stays in the check
details; license renewal reminders are conditional on using Gateway/Spy.

License inspection uses the same source precedence as managed provisioning and
reports whether the installation copy matches its source. Recognized RTI
`FEATURE`/`INCREMENT` records include `expires_on` and `days_remaining` for each
feature. Remaining days are calendar days in the machine's local timezone:
zero means "expires today," and negative values mean past expiration dates.
Dates within 14 days, expired records, unknown formats, and pending license
provisioning produce warnings. An explicit `permanent` record has no expiration;
unrecognized dates are unknown, never assumed permanent. Different feature
records remain separate, including alternative grants in concatenated files.
These are expiration metadata findings; Connext validates license signatures,
entitlements, and runtime acceptance. License contents and credentials are
never included in the report.

Doctor is always non-interactive and does not migrate files, remove expired
credentials, save exchanged tokens, provision licenses, install software, or
check for CLI updates. If only legacy configuration exists, run
`rticloud configure --get-region` to perform the normal migration first.

Text and JSON contain the same checks, each with a stable `id` and a status of
`pass`, `warn`, `fail`, or `skip`. Findings can include `code`, `required_action`,
`next_step`, and `blocked_by`. The JSON envelope is
`{"schema_version":"1","data":{...}}`; `data` contains `healthy`, `exit_code`,
`summary`, and `checks`. `healthy` means no failed checks, not that every optional
workflow is configured. Inspect individual checks before unattended Gateway/Spy
use.

A completed report always goes to stdout, even when checks fail; stderr remains
empty. Warnings and skips alone exit 0. Otherwise, the first failed check in
report order determines the exit code using the existing categories below
(for example, configuration: 1, authentication/permission: 3, timeout: 6).
Argument errors and failures to produce a report follow the normal stderr error
contract. JSON mode emits exactly one document and no progress text.

## Automation and AI agents

Use `--non-interactive` to disable prompts, browser actions, and automatic update
checks. Authenticate with an existing, unexpired login or
`CONNEXT_CLOUD_API_KEY`. Missing credentials fail immediately rather than
starting a login flow. `configure` requires `--region` (or `--get-region`) in
this mode. Gateway and Spy use text output and require any interactive setup to
have been completed beforehand. Unattended edge-agent enrollment requires a
campaign token or the service, domain-template, and participant-template flags.

Databus `list`, `query`, `create`, and `delete` support `--format json`:

```sh
rticloud databus list --format json --non-interactive
rticloud databus query --name demo --format json --non-interactive
rticloud databus create --name demo --format json --non-interactive
rticloud databus delete --name demo --format json --non-interactive
```

JSON mode also disables interaction and automatic update checks. Successful
commands emit exactly one JSON document on stdout, with no progress text:

```json
{"schema_version":"1","data":{"name":"demo","status":"active"}}
```

The `data` field contains the API response for list/query/create; creation adds
`name` if the response omits it. Deletion returns the requested `name` and
`status: "deleted"`. Creation succeeds only after the resource reaches `active`;
deletion succeeds only after the status endpoint confirms absence with HTTP 404.
Other API failures and unexpected terminal states return a failure.

Command errors in JSON mode leave stdout empty and emit one JSON document on stderr
(except completed doctor reports, described above):

```json
{"schema_version":"1","error":{"code":"AUTH_REQUIRED","message":"No usable credentials are available.","retryable":false,"required_action":"configure_credentials"}}
```

Errors include a stable `code`, a human-readable `message`, `retryable`, and,
when applicable, `http_status` and `required_action`. Retryability is conservative:
reconcile resource state before repeating a mutation that may already have taken
effect. The same error categories determine exit codes in text and JSON modes:

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | API, configuration, response, operation, or other failure |
| 2 | Invalid arguments, required interactive input, or unsupported format |
| 3 | Authentication or permission failure |
| 4 | Resource not found |
| 5 | Conflict |
| 6 | Timeout |
| 130 | Cancellation reported by a command |

Without `--format json`, the existing databus output is preserved. Errors are
written to stderr. `--short` cannot be combined with `--format json`. Supported output formats are listed in each command's help. Doctor uses the
diagnostic report contract described above.

## Smoke test runbook

Use the installed `rticloud` and your current configured, logged-in session.
This creates one temporary Databus and deletes it afterward; no Observability
Service is needed. Run the lifecycle commands in order, stopping if one fails.

```sh
# Inspect the compact setup flow and detailed authentication/formatting help.
rticloud -h
rticloud login -h
rticloud databus list -h

# Exercise the full lifecycle with a unique name.
RTICLOUD_SMOKE_NAME="cli-smoke-$(date +%s)-$$"
rticloud databus create --name "$RTICLOUD_SMOKE_NAME" --replicas 1 --format json
rticloud databus list --non-interactive
rticloud databus list --short --non-interactive
rticloud databus list --format json
rticloud databus query --name "$RTICLOUD_SMOKE_NAME" --format json
rticloud databus delete --name "$RTICLOUD_SMOKE_NAME" --format json

# Confirm deletion: expect NOT_FOUND and exit code 4.
rticloud databus query --name "$RTICLOUD_SMOKE_NAME" --format json
printf 'Exit code (expected 4): %s\n' "$?"

# Confirm argument validation: expect INVALID_ARGUMENT and exit code 2.
rticloud databus create --format json
printf 'Exit code (expected 2): %s\n' "$?"
```

Help should show `rticloud login` in **Get Started**, alternative authentication
methods afterward, and formatting details in command help. Lifecycle commands
should exit 0 without prompts; creation waits for `active`, and deletion waits
for confirmed absence. JSON commands emit a single `schema_version: "1"` result
on stdout, or a single structured error on stderr with stdout empty. The plain
list keeps its existing JSON output; `--short` shows names and kinds.

If interrupted before cleanup, run
`rticloud databus delete --name "$RTICLOUD_SMOKE_NAME" --format json`.

## Known limitations on Windows

The edge-sync agent (`rticloud agent`) runs on Windows with a reduced interactive
dashboard:

- **View-only dashboard.** The in-dashboard key actions are not available on
  Windows: arrow-key navigation, Enter-to-renew, and Ctrl+A to add a participant.
  Artifact renewal still happens automatically; add participants with
  `rticloud agent enroll`.
- **Stopping the agent.** Use Ctrl+C to stop the agent.
