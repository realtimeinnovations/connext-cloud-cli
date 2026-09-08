// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
)

// installerArtifact is a value snapshot of one pinned release artifact. Fields
// are private; callers receive a copy, never mutable shared release metadata.
type installerArtifact struct {
	version  string
	platform string
	url      string
	sha256   string
}

func (a installerArtifact) installationPath(root string) string {
	base := strings.Join(strings.Split(a.version, ".")[:3], ".")
	return filepath.Join(root, a.version, a.platform, "rti_connext_dds-"+base)
}

// Release metadata from Studio's resources/installer-artifacts.json. Update this
// definition as one unit when changing the bundled Connext release.
func bundledInstaller(goos, arch string) (installerArtifact, error) {
	const version = "7.7.0.1"
	var platform, bundle, digest string
	switch goos + "/" + arch {
	case "linux/amd64":
		platform, bundle, digest = "linux_x64", "x64Linux4gcc8.5.0.run", "21e29502a38c3d575a9d2f89cb1e5f7d3f6242fb26770fda7b70bbb1476031bc"
	case "linux/arm64":
		platform, bundle, digest = "linux_arm64", "armv8Linux4gcc8.5.0.run", "a8f5aebd5d554f7a6f5ff6ebc46b2657edf15eeff06278b27cccf336fac6d3ad"
	case "darwin/arm64":
		platform, bundle, digest = "darwin_arm64", "arm64Darwin23clang16.0.dmg", "9af9b5b45b99da71996ee73a0cb7d56c06bea634b957a71fb68e7026d60acdac"
	case "windows/amd64":
		platform, bundle, digest = "win32_x64", "x64Win64VS2017.exe", "4c3059614577aa1502eda509e2665c39b7976a2d4b8e4d1f9490fa9c1131f76b"
	default:
		return installerArtifact{}, common.UserError{Message: fmt.Sprintf("Automatic Connext Professional download is not available for %s/%s.", goos, arch)}
	}
	return installerArtifact{version: version, platform: platform, url: fmt.Sprintf("https://s3.amazonaws.com/RTI/Bundles/%s/Evaluation/rti_connext_dds-%s-lm-%s", version, version, bundle), sha256: digest}, nil
}
