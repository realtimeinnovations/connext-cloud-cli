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
	"strings"
)

// pruneManagedInstallation removes optional SDK/GUI content only from the
// validated current rticloud-managed installation, matching Studio's pruning rules.
// Cleanup is best-effort; callers log warnings without failing installation.
func pruneManagedInstallation(directory string, options DiscoveryOptions) []error {
	artifact, err := bundledInstaller(Platform())
	if err != nil {
		return []error{err}
	}
	expected, err := ManagedInstallationPath()
	if err != nil {
		return []error{err}
	}
	if filepath.Clean(directory) != filepath.Clean(expected) {
		return []error{fmt.Errorf("refusing to trim an installation outside the current rticloud-managed destination: %s", directory)}
	}
	root, err := managedRoot()
	if err != nil {
		return []error{err}
	}
	rti := filepath.Dir(filepath.Dir(root))
	if err := safeManagedPath(rti, directory); err != nil {
		return []error{err}
	}
	if _, err := validateManaged(directory, artifact.version, options); err != nil {
		return []error{fmt.Errorf("refusing to trim an invalid installation: %w", err)}
	}
	rules := []struct {
		parent, name string
		prefix       bool
	}{
		{"", "doc", false},
		{"", "lib", false},
		{"third_party", "protobuf-", true},
		{"resource", "python_api", false},
		{filepath.Join("resource", "app"), "eclipse", false},
		{filepath.Join("resource", "app"), "node-", true},
		{filepath.Join("resource", "app", "app_support"), "system_designer", false},
		{filepath.Join("resource", "app", "app_support"), "admin_console", false},
	}
	var warnings []error
	for _, rule := range rules {
		parent := filepath.Join(directory, rule.parent)
		if err := safeManagedPath(rti, parent); err != nil {
			warnings = append(warnings, err)
			continue
		}
		entries, err := os.ReadDir(parent)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			warnings = append(warnings, err)
			continue
		}
		for _, entry := range entries {
			if entry.Name() != rule.name && !(rule.prefix && strings.HasPrefix(entry.Name(), rule.name)) {
				continue
			}
			target := filepath.Join(parent, entry.Name())
			if err := safeManagedPath(rti, target); err != nil {
				warnings = append(warnings, err)
				continue
			}
			if !entry.IsDir() {
				continue
			}
			if err := os.RemoveAll(target); err != nil {
				warnings = append(warnings, fmt.Errorf("unable to trim %s: %w", target, err))
			}
		}
	}
	return warnings
}
