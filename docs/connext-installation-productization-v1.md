# Connext installation productization — V1 proposal

**Status:** Build & Release discussion draft  
**Audience:** Connext Pro Build & Release, Connext Cloud CLI, Product Management  
**Release family:** 7.7.0.x  
**Initial target:** 7.7.0.1

## Executive decision

V1 uses a small, signed release manifest to patch an existing Connext installation in place.

For LM 7.7.0 on Apple Silicon, the 7.7.0.1 path is an ordered set of 11 `.rtipkg` files. The CLI downloads and verifies the complete set, shows every filename, asks for confirmation, installs the packages into the selected LM root, and verifies the resulting component state.

The CLI does **not** download the 7.7.0.1 single-bundle installer when patching an existing LM 7.7.0 installation. That installer remains relevant only when no Connext installation exists.

There is no monolithic `rti-connext-dds-7.7.0.1-update.rtipkg`. The manifest represents the real ordered package set.

For non-LM, V1 lists the exact nine-file 7.7.0.1 Pro, WAN, OpenSSL, and Security Plugins upgrade set and directs the user to the RTI customer portal. It does not retrieve private packages.

## Requirements

- Connext Cloud is itself a preview offering, so `latest` may select an approved ER.
- Support `stable` and `latest`.
- Preserve LM versus non-LM mode.
- Patch 7.7.0 to 7.7.0.1 in place.
- Never apply the 7.7.0.1 package set to 7.7.1, 7.8.0, another mode, or another architecture.
- Show the exact files before confirmation.
- Download and verify the complete LM patch set before installing the first package.
- For non-LM, show exact private filenames and instructions only.
- Download—but never execute—the LM single bundle only for a fresh installation.
- Keep implementation terms such as manifest IDs and compatibility fields out of normal CLI output.

## Capability model

| Capability | Core executable | Shared requirements |
|---|---|---|
| Gateway | `rtiroutingservice` | WAN, Security Plugins, OpenSSL |
| Spy | `rtiddsspy` | WAN, Security Plugins, OpenSSL |
| Observability | `rticollectorservicelite` | WAN, Security Plugins, OpenSSL |

For non-LM:

- Routing Service and DDS Spy are in the base host package in 7.7.0 and 7.7.0.1.
- Collector Service Lite joins the base host package in 7.7.0.1; it is not in the 7.7.0 base host.
- WAN, Security Plugins, and OpenSSL remain separate packages.

Capability minimums answer whether the command may run. The selected channel answers which product state the CLI recommends. A capability may already satisfy its minimum on 7.7.0 while the channel still offers the global 7.7.0.1 patch.

## Verified findings from the Connext repository

The review used `origin/release/connextdds/7.7.0.1` and the installed `/Applications/rti_connext_dds-7.7.0/rti_versions.xml`.

### Installation identity

The existing installation is unambiguously LM:

```xml
<host>
  <platform>arm64Darwin</platform>
  <base_version>7.7.0</base_version>
  <installation_type>RTI Connext DDS LM</installation_type>
  <installer_name>rti_connext_dds-7.7.0-lm-arm64Darwin.app</installer_name>
</host>
```

The selected target architecture is `arm64Darwin23clang16.0`.

### The supplied LM list is correct

The unconditional `rtipkg_files_list` in:

```text
resource.3.0/product_installers/bitrock/configuration/
  rtipkgbuilder_based_rti_connext_lm.xml
```

contains exactly the 11 patterns supplied for review:

```text
rti_connext_dds-${package_version}-lm-host-${host_platform}.rtipkg
rti_connext_dds-${package_version}-lm-host-unlicensed_components-${host_platform}.rtipkg
rti_connext_dds-${package_version}-lm-host-${host_platform}-extras.rtipkg
rti_connext_dds-${package_version}-lm-target-${shared_tools_libraries_architecture}.rtipkg
rti_connext_dds-${package_version}-lm-target-unlicensed_components-${shared_tools_libraries_architecture}.rtipkg
rti_real_time_wan_transport-${package_version}-lm-host-unlicensed_components-${host_platform}.rtipkg
rti_security_plugins-${package_version}-lm-host-openssl-${openssl3_version}-${host_platform}.rtipkg
rti_security_plugins-${package_version}-lm-host-unlicensed_components-openssl-${openssl3_version}-${host_platform}.rtipkg
rti_security_plugins-${package_version}-lm-target-openssl-${openssl3_version}-${shared_tools_libraries_architecture}.rtipkg
openssl-${openssl3_full_version}-${package_version}-host-${host_platform}.rtipkg
openssl-${openssl3_full_version}-${package_version}-target-${shared_tools_libraries_architecture}.rtipkg
```

For the upcoming Apple Silicon release, the substitutions are:

| Variable | Value |
|---|---|
| `package_version` | `7.7.0.1` |
| `host_platform` | `arm64Darwin` |
| `shared_tools_libraries_architecture` | `arm64Darwin23clang16.0` |
| `openssl3_version` | `3.5` |
| `openssl3_full_version` | `3.5.7` |

### Exact LM 7.7.0 → 7.7.0.1 patch set

The resulting ordered patch set is:

| Order | Exact filename | Purpose |
|---:|---|---|
| 1 | `rti_connext_dds-7.7.0.1-lm-host-arm64Darwin.rtipkg` | Licensed LM host content, including Routing Service, DDS Spy utilities, and Collector Service Lite |
| 2 | `rti_connext_dds-7.7.0.1-lm-host-unlicensed_components-arm64Darwin.rtipkg` | Unlicensed LM host content |
| 3 | `rti_connext_dds-7.7.0.1-lm-host-arm64Darwin-extras.rtipkg` | LM host extras |
| 4 | `rti_connext_dds-7.7.0.1-lm-target-arm64Darwin23clang16.0.rtipkg` | Licensed LM target content |
| 5 | `rti_connext_dds-7.7.0.1-lm-target-unlicensed_components-arm64Darwin23clang16.0.rtipkg` | Unlicensed LM target content |
| 6 | `rti_real_time_wan_transport-7.7.0.1-lm-host-unlicensed_components-arm64Darwin.rtipkg` | WAN host-side content |
| 7 | `rti_security_plugins-7.7.0.1-lm-host-openssl-3.5-arm64Darwin.rtipkg` | Licensed Security Plugins host content |
| 8 | `rti_security_plugins-7.7.0.1-lm-host-unlicensed_components-openssl-3.5-arm64Darwin.rtipkg` | Unlicensed Security Plugins host content |
| 9 | `rti_security_plugins-7.7.0.1-lm-target-openssl-3.5-arm64Darwin23clang16.0.rtipkg` | Security Plugins target content |
| 10 | `openssl-3.5.7-7.7.0.1-host-arm64Darwin.rtipkg` | OpenSSL host content |
| 11 | `openssl-3.5.7-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg` | OpenSSL target content |

The order above is the source order and should be preserved in the manifest.

### Why Cloud Discovery Service is not in this patch set

The same XML file has a second, separate block that conditionally appends three Cloud Discovery Service packages while assembling some complete LM installers:

```text
rti_cloud_discovery_service-${package_version}-lm-host-${host_platform}.rtipkg
rti_cloud_discovery_service-${package_version}-lm-host-unlicensed_components-${host_platform}.rtipkg
rti_cloud_discovery_service-${package_version}-lm-target-${shared_tools_libraries_architecture}.rtipkg
```

Those entries are not part of the supplied unconditional 11-package list and Cloud Discovery Service is not a Gateway, Spy, or Observability requirement. V1 therefore does not silently add them to the Cloud CLI patch.

If Build & Release later wants the CLI patch to guarantee complete LM-installer parity rather than the defined Cloud-ready 7.7.0.1 state, that must be an explicit patch-set revision.

## Non-LM 7.7.0 → 7.7.0.1 package guidance

For Apple Silicon, the non-LM update candidate derived from the 7.7.0.1 release package definitions contains these private packages:

| Order | Exact filename | Why it is needed |
|---:|---|---|
| 1 | `rti_connext_dds-7.7.0.1-pro-host-arm64Darwin.rtipkg` | Updates the base host; adds Collector Service Lite while retaining Routing Service and DDS Spy |
| 2 | `rti_connext_dds-7.7.0.1-pro-host-arm64Darwin-extras.rtipkg` | Host extras shipped as a separate Pro package |
| 3 | `rti_connext_dds-7.7.0.1-pro-target-arm64Darwin23clang16.0.rtipkg` | Pro target libraries for the selected architecture |
| 4 | `rti_real_time_wan_transport-7.7.0.1-host-arm64Darwin.rtipkg` | WAN host tools and runtime content |
| 5 | `rti_real_time_wan_transport-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg` | WAN target libraries |
| 6 | `openssl-3.5.7-7.7.0.1-host-arm64Darwin.rtipkg` | OpenSSL host runtime used by Security Plugins |
| 7 | `openssl-3.5.7-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg` | OpenSSL target libraries used by Security Plugins |
| 8 | `rti_security_plugins-7.7.0.1-host-openssl-3.5-arm64Darwin.rtipkg` | Security Plugins host tools and runtime content |
| 9 | `rti_security_plugins-7.7.0.1-target-openssl-3.5-arm64Darwin23clang16.0.rtipkg` | Security Plugins target libraries |

The previous four-file proposal was incorrect: it mixed a Pro host package with target-only WAN, Security, and OpenSSL packages, omitted the Pro target package, omitted every add-on host package, and excluded Pro host extras without a release-defined upgrade rule that justified doing so. V1 should not infer a capability-specific subset from filenames. It should display the complete Build & Release-approved upgrade set.

Unlike LM, the Pro definitions do not split these artifacts into licensed and `unlicensed_components` variants. That is why this list has no additional Pro unlicensed-component packages.

This nine-file set is derived from the package registrations and filename templates on `origin/release/connextdds/7.7.0.1`. Build & Release must still confirm that it is the complete supported closure and approve its installation order before publication; the manifest records that state with `validated_package_closure: false` until confirmation.

V1 does not download these private packages. It lists them and links to the customer-portal instructions.

## V1 ownership boundary

### Build & Release owns

- The exact ordered LM patch set.
- Public artifact base URL, size, and SHA-256 for every LM package.
- Stable/latest channel selection and approved ER metadata.
- Exact non-LM private filenames for every supported architecture.
- Expected post-install package markers.
- Recovery instructions for partial package-install failure.
- Signed manifest generation and publication.

### The CLI owns

- Installation discovery and `rti_versions.xml` parsing.
- LM, non-LM, and unknown classification.
- Host and target architecture normalization.
- Capability detection.
- Exact patch-set matching.
- Complete plan rendering and confirmation.
- Download, size verification, SHA-256 verification, and fixed `rtipkginstall` execution.
- Post-install rediscovery and validation.

The manifest never provides shell commands or executable arguments.

## Simplified manifest structure

| Field | Purpose |
|---|---|
| `schema`, `revision`, timestamps, `minimum_cli` | Trust and compatibility |
| `release_family` | Three-part family, `7.7.0` |
| `capabilities` | Minimum runnable state |
| `channels` | Global recommended state and patch/ER references |
| `lm_installers` | Fresh-install bundles only |
| `lm_patch_sets` | Exact in-place LM package lists |
| `public_packages` | Cloud Extras packages |
| `non_lm_guidance` | Exact private package lists and instructions |

V1 does not need provider graphs, arbitrary dependencies, conflicts, priorities, target inheritance, or manifest-defined detectors.

### 7.7.0.1 example manifest

This is a one-platform discussion slice. The release pipeline replaces uppercase publication values and refuses to sign until every size, digest, and URL is final.

```json
{
  "schema": 1,
  "revision": 43,
  "generated_at": "2026-07-16T00:00:00Z",
  "expires_at": "2026-10-16T00:00:00Z",
  "minimum_cli": "1.2.0",
  "release_family": "7.7.0",

  "capabilities": {
    "gateway": { "core": "routing-service", "minimum_base": "7.3.0" },
    "spy": { "core": "dds-spy", "minimum_base": "7.7.0", "minimum_package": "cloud-extras-er723" },
    "observability": { "core": "collector-lite", "minimum_base": "7.7.0", "minimum_package": "cloud-extras-er723" }
  },

  "shared_requirements": ["wan", "security", "openssl"],

  "channels": {
    "stable": {
      "recommended_version": "7.7.0.1",
      "lm_patch_set": "lm-7.7.0-to-7.7.0.1-darwin-arm64"
    },
    "latest": {
      "recommended_version": "7.7.0.1",
      "lm_patch_set": "lm-7.7.0-to-7.7.0.1-darwin-arm64",
      "additional_package": "cloud-extras-er745"
    }
  },

  "lm_installers": {
    "darwin-arm64": {
      "version": "7.7.0.1",
      "filename": "rti_connext_dds-7.7.0.1-lm-arm64Darwin23clang16.0.dmg",
      "url": "PUBLIC_FRESH_INSTALL_URL",
      "size": "GENERATED_BY_RELEASE_PIPELINE",
      "sha256": "GENERATED_BY_RELEASE_PIPELINE"
    }
  },

  "lm_patch_sets": {
    "lm-7.7.0-to-7.7.0.1-darwin-arm64": {
      "from_base": "7.7.0",
      "to_version": "7.7.0.1",
      "installation_type": "RTI Connext DDS LM",
      "host": "arm64Darwin",
      "target": "arm64Darwin23clang16.0",
      "artifact_base_url": "PUBLIC_7.7.0.1_RTIPKG_BASE_URL",
      "packages": [
        { "filename": "rti_connext_dds-7.7.0.1-lm-host-arm64Darwin.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_connext_dds-7.7.0.1-lm-host-unlicensed_components-arm64Darwin.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_connext_dds-7.7.0.1-lm-host-arm64Darwin-extras.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_connext_dds-7.7.0.1-lm-target-arm64Darwin23clang16.0.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_connext_dds-7.7.0.1-lm-target-unlicensed_components-arm64Darwin23clang16.0.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_real_time_wan_transport-7.7.0.1-lm-host-unlicensed_components-arm64Darwin.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_security_plugins-7.7.0.1-lm-host-openssl-3.5-arm64Darwin.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_security_plugins-7.7.0.1-lm-host-unlicensed_components-openssl-3.5-arm64Darwin.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "rti_security_plugins-7.7.0.1-lm-target-openssl-3.5-arm64Darwin23clang16.0.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "openssl-3.5.7-7.7.0.1-host-arm64Darwin.rtipkg", "size": "GENERATED", "sha256": "GENERATED" },
        { "filename": "openssl-3.5.7-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg", "size": "GENERATED", "sha256": "GENERATED" }
      ],
      "verify": {
        "executables": ["rtiroutingservice", "rtiddsspy", "rticollectorservicelite"],
        "package_version": "7.7.0.1"
      }
    }
  },

  "public_packages": {
    "cloud-extras-er723": {
      "version": "7.7.0_RTI_ER_723",
      "compatible_base": "7.7.0",
      "platforms": {
        "darwin-arm64": {
          "filename": "rti_connext_dds-7.7.0_RTI_ER_723-arm64Darwin-cloud-extras.rtipkg",
          "url": "PUBLIC_ER_723_URL",
          "size": "GENERATED",
          "sha256": "GENERATED"
        }
      }
    },
    "cloud-extras-er745": {
      "version": "7.7.0.1_RTI_ER_745",
      "compatible_base": "7.7.0",
      "platforms": {
        "darwin-arm64": {
          "filename": "rti_connext_dds-7.7.0.1_RTI_ER_745-arm64Darwin-cloud-extras.rtipkg",
          "url": "PUBLIC_ER_745_URL",
          "size": "GENERATED",
          "sha256": "GENERATED"
        }
      }
    }
  },

  "non_lm_guidance": {
    "from_base": "7.7.0",
    "to_version": "7.7.0.1",
    "validated_package_closure": false,
    "validation_note": "Build & Release must confirm this complete ordered non-LM upgrade closure.",
    "platforms": {
      "darwin-arm64": {
        "target": "arm64Darwin23clang16.0",
        "packages": [
          "rti_connext_dds-7.7.0.1-pro-host-arm64Darwin.rtipkg",
          "rti_connext_dds-7.7.0.1-pro-host-arm64Darwin-extras.rtipkg",
          "rti_connext_dds-7.7.0.1-pro-target-arm64Darwin23clang16.0.rtipkg",
          "rti_real_time_wan_transport-7.7.0.1-host-arm64Darwin.rtipkg",
          "rti_real_time_wan_transport-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg",
          "openssl-3.5.7-7.7.0.1-host-arm64Darwin.rtipkg",
          "openssl-3.5.7-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg",
          "rti_security_plugins-7.7.0.1-host-openssl-3.5-arm64Darwin.rtipkg",
          "rti_security_plugins-7.7.0.1-target-openssl-3.5-arm64Darwin23clang16.0.rtipkg"
        ]
      }
    },
    "instructions_url": "APPROVED_CUSTOMER_PORTAL_INSTRUCTIONS"
  }
}
```

## Resolution algorithm

1. Verify and load the signed manifest.
2. Discover installations and parse `rti_versions.xml`.
3. Classify the selected installation as LM, non-LM, or unknown.
4. Resolve the capability minimum and selected channel.
5. If no installation exists, offer the platform LM single bundle as download-only.
6. For LM 7.7.0, match the exact patch set by base, installation type, host, and target.
7. Render all 11 filenames; add ER 745 as item 12 for `latest`.
8. Ask for confirmation.
9. Download and verify the entire set before installing anything.
10. Install sequentially using CLI-owned `rtipkginstall` arguments.
11. Rediscover and verify the expected package version and executables.
12. For non-LM 7.7.0, print the nine private 7.7.0.1 filenames and portal instructions.

If the exact LM patch set is missing or incomplete, the CLI reports that the 7.7.0.1 patch metadata is unavailable. It does not substitute a full installer for an existing installation.

## UX flows

### LM 7.7.0 → stable 7.7.0.1

```text
Connext preflight · Observability · stable

Installation
  RTI Connext DDS LM 7.7.0
  /Applications/rti_connext_dds-7.7.0
  Apple silicon · arm64Darwin23clang16.0

The CLI will download and install 11 packages:

   1. rti_connext_dds-7.7.0.1-lm-host-arm64Darwin.rtipkg
   2. rti_connext_dds-7.7.0.1-lm-host-unlicensed_components-arm64Darwin.rtipkg
   3. rti_connext_dds-7.7.0.1-lm-host-arm64Darwin-extras.rtipkg
   4. rti_connext_dds-7.7.0.1-lm-target-arm64Darwin23clang16.0.rtipkg
   5. rti_connext_dds-7.7.0.1-lm-target-unlicensed_components-arm64Darwin23clang16.0.rtipkg
   6. rti_real_time_wan_transport-7.7.0.1-lm-host-unlicensed_components-arm64Darwin.rtipkg
   7. rti_security_plugins-7.7.0.1-lm-host-openssl-3.5-arm64Darwin.rtipkg
   8. rti_security_plugins-7.7.0.1-lm-host-unlicensed_components-openssl-3.5-arm64Darwin.rtipkg
   9. rti_security_plugins-7.7.0.1-lm-target-openssl-3.5-arm64Darwin23clang16.0.rtipkg
  10. openssl-3.5.7-7.7.0.1-host-arm64Darwin.rtipkg
  11. openssl-3.5.7-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg

All 11 files will be downloaded and verified before installation starts.
The selected LM installation will be patched in place.

Download and install these 11 packages? [y/N]
```

For `latest`, the CLI lists Cloud Extras ER 745 as package 12 and installs it after the stable patch succeeds.

### Non-LM 7.7.0 → stable 7.7.0.1

```text
Connext preflight · Observability · stable

Installation
  RTI Connext DDS Pro 7.7.0
  Apple silicon · arm64Darwin23clang16.0

Current base host:
  ✓ Routing Service
  ✓ DDS Spy
  ✗ Collector Service Lite — added by the 7.7.0.1 base host

Retrieve these 7.7.0.1 packages from the RTI customer portal:

  1. rti_connext_dds-7.7.0.1-pro-host-arm64Darwin.rtipkg
  2. rti_connext_dds-7.7.0.1-pro-host-arm64Darwin-extras.rtipkg
  3. rti_connext_dds-7.7.0.1-pro-target-arm64Darwin23clang16.0.rtipkg
  4. rti_real_time_wan_transport-7.7.0.1-host-arm64Darwin.rtipkg
  5. rti_real_time_wan_transport-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg
  6. openssl-3.5.7-7.7.0.1-host-arm64Darwin.rtipkg
  7. openssl-3.5.7-7.7.0.1-target-arm64Darwin23clang16.0.rtipkg
  8. rti_security_plugins-7.7.0.1-host-openssl-3.5-arm64Darwin.rtipkg
  9. rti_security_plugins-7.7.0.1-target-openssl-3.5-arm64Darwin23clang16.0.rtipkg

The CLI will not download or install private packages in V1.
Follow: <approved customer-portal instructions>

After installing the packages, rerun:
  rticloud observability
```

For Gateway or Spy, the 7.7.0 base may already satisfy the capability minimum. The CLI still shows the channel-recommended 7.7.0.1 package list, but declining the recommendation does not block a capability whose minimum is already satisfied.

### Fresh installation

```text
No compatible Connext installation was found.

The CLI will download:
  rti_connext_dds-7.7.0.1-lm-arm64Darwin23clang16.0.dmg

The installer will be verified but not opened or executed.
```

## Publisher validation

The publishing job must reject the manifest when:

- the Apple Silicon LM patch does not contain exactly 11 ordered packages;
- a package filename differs from the generated release artifact;
- size, SHA-256, or public package location is missing;
- the patch source is not exactly LM 7.7.0 with the declared host and target;
- a channel references an unknown patch or ER package;
- a private non-LM filename is missing;
- signed bytes differ from uploaded bytes.

## Security and failure behavior

- Verify manifest signature before planning.
- Verify all 11 downloads before installing package 1.
- Use HTTPS and reject filename/path traversal.
- Use fixed CLI-owned `rtipkginstall` arguments.
- Stop immediately on installation failure.
- Do not run the selected Cloud capability until post-install verification succeeds.
- Do not replace an existing installation with a single bundle.
- Do not retrieve private non-LM packages in V1.

## Test requirements

- Exact 11-package Apple Silicon LM plan and order.
- Stable plan with 11 packages; latest plan with 12 including ER 745.
- LM 7.7.0 in-place installation and post-install detection.
- Rejection of 7.7.1, 7.8.0, non-LM, and wrong-architecture matches.
- All-downloads-before-install behavior.
- Digest, size, partial-download, and package-install failures.
- Non-LM 7.7.0 nine-package instructions flow, including every required host/target pair.
- Collector Service Lite absent on non-LM 7.7.0 and present after the 7.7.0.1 base-host package.
- Fresh-install download-only behavior.

## Source traceability

| Finding | Source |
|---|---|
| Exact unconditional 11-package list and order | `resource.3.0/product_installers/bitrock/configuration/rtipkgbuilder_based_rti_connext_lm.xml` |
| Separate conditional Cloud Discovery append | Same XML file, immediately after the unconditional list |
| OpenSSL 3.5.7 | `conanfile.py`, `_get_openssl3_version()` |
| Pro host, host-extras, and target registrations | Root `CMakeLists.txt`: `rti_connext_dds_pro_host.yml`, `rti_connext_dds_pro_host_extras.yml`, and `rti_connext_dds_pro_target.yml` |
| WAN host and target registrations | `transport.1.0/CMakeLists.txt`: `real_time_wan_transport_host.yml` and `real_time_wan_transport_target.yml` |
| Security Plugins host and target registrations | `security.1.0/CMakeLists.txt`: `security_plugins_host_openssl3.yml` and `security_plugins_target_openssl3.yml` |
| Exact non-LM filename templates | The corresponding files under `resource.3.0/product_installers/definitions/`, including `openssl3_host.yml` and `openssl3_target.yml` |
| Collector Service Lite in the 7.7.0.1 host definition | `resource.3.0/product_installers/definitions/rti_connext_dds_lm_host.yml` and `rti_connext_dds_pro_host.yml` |
| Existing LM mode, host, and target evidence | `/Applications/rti_connext_dds-7.7.0/rti_versions.xml` |

## Build & Release decisions still required

1. Final public base URL, sizes, and SHA-256 values for the 11 LM packages.
2. Approved recovery instructions after a partial package-install failure.
3. Exact package markers the CLI should require after patching.
4. Whether a separate full-product-parity patch should include the three conditional Cloud Discovery packages.
5. Confirmation of the complete nine-package non-LM closure and supported installation order.
6. Final customer-portal instructions URL for non-LM packages.

## Recommendation

Ship the 7.7.0.1 V1 manifest with the explicit 11-package LM patch set. Patch existing LM 7.7.0 installations in place; never substitute the single-bundle installer for that path. Include the nine-package non-LM 7.7.0 guidance after Build & Release confirms its closure and order, and reserve the LM single bundle for fresh installations only.
