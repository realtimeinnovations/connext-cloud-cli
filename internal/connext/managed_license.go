// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File validity matches Studio provisioning: readable, regular, and not a
// symlink. License feature/expiry enforcement remains in the RTI runtime.
func readCopyableLicense(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("license must be a readable regular file: %s", path)
	}
	return os.ReadFile(path)
}

func managedLicensePath() (string, error) {
	root, err := managedRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), "licenses", LicenseFileName), nil
}

func IsManagedInstallation(install Install) bool {
	root, err := managedRoot()
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, install.Path)
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || !exactVersionRE.MatchString(parts[0]) || parts[1] != platformDirectory() {
		return false
	}
	base := strings.Split(parts[0], ".")
	if len(base) < 3 || parts[2] != "rti_connext_dds-"+strings.Join(base[:3], ".") {
		return false
	}
	return safeManagedPath(filepath.Dir(filepath.Dir(root)), install.Path) == nil
}

func writeVerifiedLicense(path string, contents []byte) error {
	root, err := managedRoot()
	if err != nil {
		return err
	}
	// An invalid destination entry can be replaced, but no parent symlink is followed.
	if err := safeManagedPath(filepath.Dir(filepath.Dir(root)), filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".rti-license-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	verified, err := os.ReadFile(temporary.Name())
	if err != nil {
		return err
	}
	if !bytes.Equal(verified, contents) {
		return fmt.Errorf("license copy verification failed")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		// Remove a symlink or empty invalid directory, never a directory tree.
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func seedManagedLicense(contents []byte) error {
	path, err := managedLicensePath()
	if err != nil {
		return err
	}
	if err := safeManagedPath(filepath.Dir(filepath.Dir(filepath.Dir(path))), filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := readCopyableLicense(path); err == nil {
		return nil
	}
	return writeVerifiedLicense(path, contents)
}

// ProvisionManagedLicense repairs the per-version copy from product-owned
// storage. A declared installation's invalid license blocks environment fallback.
func ProvisionManagedLicense(install Install) (bool, error) {
	if !IsManagedInstallation(install) {
		return false, fmt.Errorf("not an rticloud-managed installation: %s", install.Path)
	}
	canonical, err := managedLicensePath()
	if err != nil {
		return false, err
	}
	if err := safeManagedPath(filepath.Dir(filepath.Dir(filepath.Dir(canonical))), filepath.Dir(canonical)); err != nil {
		return false, err
	}
	contents, readErr := readCopyableLicense(canonical)
	if readErr != nil {
		// Preserve licenses saved by earlier CLI versions inside their installation.
		contents, err = readCopyableLicense(LicenseFilePath(install))
		if err != nil {
			selected := os.Getenv("NDDSHOME")
			selectedLicense := ""
			if selected != "" && !sameInstallationPath(selected, install.Path) {
				selectedLicense = filepath.Join(selected, LicenseFileName)
			}
			if selectedLicense != "" {
				if _, statErr := os.Lstat(selectedLicense); statErr == nil {
					contents, err = readCopyableLicense(selectedLicense)
					if err != nil {
						return false, fmt.Errorf("invalid NDDSHOME license; environment fallback is disabled: %w", err)
					}
				} else if !os.IsNotExist(statErr) {
					return false, statErr
				}
			}
			if contents == nil {
				if env := os.Getenv("RTI_LICENSE_FILE"); env != "" {
					contents, err = readCopyableLicense(env)
					if err != nil {
						return false, fmt.Errorf("invalid RTI_LICENSE_FILE: %w", err)
					}
				} else {
					contents = previousManagedLicense(install)
				}
			}
		}
		if contents == nil {
			return false, nil
		}
		if err := writeVerifiedLicense(canonical, contents); err != nil {
			return false, err
		}
	}
	if current, err := readCopyableLicense(LicenseFilePath(install)); err == nil && bytes.Equal(current, contents) {
		return true, nil
	}
	if err := writeVerifiedLicense(LicenseFilePath(install), contents); err != nil {
		return false, err
	}
	return true, nil
}

func previousManagedLicense(current Install) []byte {
	root, err := managedRoot()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return CompareVersion(entries[i].Name(), entries[j].Name()) > 0 })
	for _, entry := range entries {
		if !entry.IsDir() || !exactVersionRE.MatchString(entry.Name()) || CompareVersion(entry.Name(), current.Version) >= 0 {
			continue
		}
		parts := strings.Split(entry.Name(), ".")
		candidate := Install{Path: filepath.Join(root, entry.Name(), platformDirectory(), "rti_connext_dds-"+strings.Join(parts[:3], "."))}
		if !IsManagedInstallation(candidate) {
			continue
		}
		if contents, err := readCopyableLicense(LicenseFilePath(candidate)); err == nil {
			return contents
		}
	}
	return nil
}

// EnvironmentOverrides keeps subprocesses on the selected installation and its
// provisioned license, even when the invoking shell selects another installation.
func (install Install) EnvironmentOverrides() []string {
	overrides := []string{"NDDSHOME=" + install.Path, "CONNEXTDDS_DIR=" + install.Path}
	if IsManagedInstallation(install) {
		overrides = append(overrides, "RTI_LICENSE_FILE="+LicenseFilePath(install))
	}
	return overrides
}
