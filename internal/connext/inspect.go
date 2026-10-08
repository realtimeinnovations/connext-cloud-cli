// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"fmt"
	"os"
	"path/filepath"
)

// InspectManagedInstallation validates only metadata. Components are inspected
// separately so a missing Spy executable does not obscure Gateway findings.
// A missing installation is normal before first use. An installation marker
// or invalid existing installation is an error. This never invokes an installer.
func InspectManagedInstallation() (install Install, exists bool, err error) {
	root, err := managedRoot()
	if err != nil {
		return install, false, err
	}
	artifact, err := bundledInstaller(Platform())
	if err != nil {
		return install, false, err
	}
	install = Install{Path: artifact.installationPath(root), Version: artifact.version, Reason: "rticloud-managed installation"}
	if err := safeManagedPath(filepath.Dir(filepath.Dir(root)), install.Path); err != nil {
		return install, true, err
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(install.Path), installationPendingFile)); !os.IsNotExist(err) {
		return install, true, fmt.Errorf("Connext installation is incomplete at %s", install.Path)
	}
	if _, err := os.Lstat(install.Path); os.IsNotExist(err) {
		return install, false, nil
	} else if err != nil {
		return install, true, err
	}
	_, err = validateManagedMetadata(install.Path, artifact.version)
	return install, true, err
}

// ValidateManagedTool shares the same file and version checks as installation.
func ValidateManagedTool(install Install, tool, minimum string) error {
	if _, err := ValidateInstall(install.Path, DiscoveryOptions{ExecutableName: tool, MinVersion: minimum}); err != nil {
		return err
	}
	executable := Executable(install.Path, tool)
	if err := safeManagedPath(install.Path, executable); err != nil {
		return err
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return err
	}
	platform, _ := Platform()
	if !info.Mode().IsRegular() || (platform != "windows" && info.Mode().Perm()&0o111 == 0) {
		return fmt.Errorf("invalid Connext executable: %s", executable)
	}
	return nil
}
