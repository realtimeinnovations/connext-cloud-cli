// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package rtipaths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var UserHomeDir = os.UserHomeDir

// Root is the shared RTI directory, including Windows' Local AppData fallback.
func Root(home, platform, localAppData string) string {
	if platform == "windows" {
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localAppData, ".rti")
	}
	return filepath.Join(home, ".rti")
}

func CloudRoot() (string, error) {
	_, root, err := resolveHomeAndCloudRoot()
	return root, err
}

func resolveHomeAndCloudRoot() (string, string, error) {
	home, err := UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("determine user home directory: %w", err)
	}
	if home == "" {
		return "", "", fmt.Errorf("determine user home directory: path is empty")
	}
	root := filepath.Join(Root(home, runtime.GOOS, os.Getenv("LOCALAPPDATA")), "rticloud")
	if !filepath.IsAbs(root) {
		return "", "", fmt.Errorf("determine RTI Cloud directory: path is not absolute: %s", root)
	}
	return home, root, nil
}
