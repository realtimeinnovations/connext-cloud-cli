# Connext installation productization

Currently this CLI hard-codes a concrete set of requirements and remediations (like installing the cloud-extras package for a specific version). This was fine for the initial versions of Connext Cloud and this CLI.

We now need to design a more flexible and maintainable approach to detect, using a manifest file that will be published openly in S3 in a similar path to the current .rtipkg. We need to design the UX and the format for this manifest file.

You need to know the following about Connext installations.

The libraries and therefore the installation comes in two modes:

- LM: License-managed mode; the core library only succeeds if a valid license file is available. This allows publishing these packages in public repositories.

- Non-LM: the core libraries don't check for licenses. This makes for easier customer deployments but requires that these installers be placed in private repositories where this CLI can't get them at the moment.

LM can be used by both evaluators with free licenses and paid customers; Non-LM only by paid customers.

There's another dimension but it's currently coupled to the LM axis:

- Single-bundle installations (LM only) - a single installer includes all the components (patches may still be needed). This is the current flow that is partially implemented in the CLI. It downloads the single 7.7 LM bundle and (for spy and observability only) installs the cloud-extras package (a patch)

- Multi-bundle installations - an installer installs the base components (host and target libraries), additional .rtipkg packages install, several of which are needed by the rticloud CLI (WAN transport, secure plugins; these packages are included in the single-bundle installer).

There's a extra consideration: some .rtipkg are neither LM nor non-LM; they include components that don't need license protection and can be installed on top of either LM or non-LM installations. For example, the current cloud-extras is such type of package.

The goal is for the rticloud CLI, after a connext install is chosen or automatically used to be able to detect the situation of that install and the best path to make it Cloud-ready, while preserving the type of install.

Additionally, the manifest and the cli should be able to know the minimum version of the components, the latest stable version, and the latest available experimental or preview version. The user should be able to configure their CLI to be in "stable" or "latest" mode (by default "latest") and offer to upgrade accordingly (we currently have an upgrade flow for the cli itself, with a command to run it, a check, and an automatic check with spaced in time to not run on each run; we may want to use this same flow for the connext components).

These are some initial thoughts on the UX:

If the user chooses to install Connext (they don't have it or choose to install a new version), the CLI should automatically get the latest/stable (based on config parameter deciding this as described earlier) version of the LM single-bundle.

If the user chooses a non-LM install and any components missing are needed, the CLI should be able to automatically install only those that don't require a license (like cloud-extras). Any components that have the LM/non-LM distinction should be listed for the user to download from the private repo. In this case, the CLI should also provide a message: "Tip: you can also choose to install Connext to get a Connext Cloud-ready installation without affecting your current one" (copy-edit this.)

If the user chooses an LM install and any components are missing or updatable, the CLI should be able to get them from the manifest-stated urls.

Some installs may not be patchable and in that case the CLI should prompt the user to choose another install or to let the CLI install the latest/stable version (which will always be the LM single-bundle). Again, this information should be encoded in the manifest file in a way that the CLI can detect it without hard-coding.

Only minor versions can be patched: E.g. 7.7.0 to 7.7.0.1 is patchable, but 7.7.0 to 7.8.0 or to 7.7.1 is not.

The installers and rtipkgs are defined here and adjacent directories. Explore to get a sense of the current state of the art:
~/repo/epic-connext/src/resource.3.0/product_installers/bitrock/configuration/rtipkgbuilder_based_rti_connext_lm.xml

To give you a sense of what we need to work in the next year our so, this is an expected timeline:

Current state:

Minimum for gateway: 7.3
Minimum for observability & spy: 7.7.0.1 + cloud extras.rtipkg

Near future:

Minimum for gateway: 7.3, latest-stable: 7.7.0.1, latest-preview: 7.7.0.1
Minimum for observability & spy: 7.7.0.1 (with LM/non-LM distinction). This new version already includes the cloud-extras components/patches.

Maybe a little later:

Gateway minimum: 7.3, but recommended latest-stable: 7.7.0.1, latest-preview: 7.7.0.1 + cloud-extras_7.7.0.1_ER_745
observability & spy: minimum and latest-stable: 7.7.0.1, latest-preview: 7.7.0.1 + cloud-extras_7.7.0.1_ER_745

(note that previews will always have ER_<> in the version string and the package name)

And maybe later:

Gateway & spy minimum: 7.7.0.1, but recommended latest-stable: 7.8.0, latest-preview: 7.8.0
Observability, minimum 7.7.0.1, but recommended latest-stable: 7.8.0, latest-preview: 7.8.0 + cloud-extras_7.8.0_ER_746.

I would like you to analyze the requirements, the current state of the CLI, and the installer at ~/repo/epic-connext/src/resource.3.0/product_installers/bitrock/configuration/ with the goal of designing a manifest file and a flow diagram to cover the requirements. Ask me any clarifying questions you need.





