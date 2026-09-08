// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundledInstallerDescriptors(t *testing.T) {
	for _, test := range []struct{ os, arch, platform, bundle string }{
		{"linux", "amd64", "linux_x64", "x64Linux4gcc8.5.0.run"},
		{"linux", "arm64", "linux_arm64", "armv8Linux4gcc8.5.0.run"},
		{"darwin", "arm64", "darwin_arm64", "arm64Darwin23clang16.0.dmg"},
		{"windows", "amd64", "win32_x64", "x64Win64VS2017.exe"},
	} {
		artifact, err := bundledInstaller(test.os, test.arch)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.version != installerVersion || artifact.platform != test.platform {
			t.Fatalf("%+v", artifact)
		}
		wantURL := "https://s3.amazonaws.com/RTI/Bundles/" + artifact.version + "/Evaluation/rti_connext_dds-" + artifact.version + "-lm-" + test.bundle
		if artifact.url != wantURL {
			t.Fatal(artifact.url)
		}
		digest, err := hex.DecodeString(artifact.sha256)
		if err != nil || len(digest) != 32 {
			t.Fatalf("invalid digest %q", artifact.sha256)
		}
		wantPath := filepath.Join("root", artifact.version, test.platform, "rti_connext_dds-7.7.0")
		if artifact.installationPath("root") != wantPath {
			t.Fatal(artifact.installationPath("root"))
		}
		original := artifact
		artifact.version = "changed"
		artifact.sha256 = "fixture"
		again, err := bundledInstaller(test.os, test.arch)
		if err != nil || again != original {
			t.Fatal("descriptor copy changed shared metadata")
		}
	}
	for _, test := range [][2]string{{"darwin", "amd64"}, {"windows", "arm64"}, {"linux", "386"}} {
		if _, err := bundledInstaller(test[0], test[1]); err == nil || !strings.Contains(err.Error(), test[0]+"/"+test[1]) {
			t.Fatal(err)
		}
	}
}

func TestUnsupportedBundledPlatformStillAllowsExternalInstallation(t *testing.T) {
	managedTestEnvironment(t)
	Platform = func() (string, string) { return "darwin", "amd64" }
	custom := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiroutingservice")
	result, err := DiscoverInstall(map[string]string{"NDDSHOME": custom}, DiscoveryOptions{AcceptExternalNDDSHome: true})
	if err != nil || result.Path != custom {
		t.Fatalf("%+v %v", result, err)
	}
}
