// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package rtipaths

import (
	"os"
	"path/filepath"
	"runtime"
)

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

func CloudRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(Root(home, runtime.GOOS, os.Getenv("LOCALAPPDATA")), "rticloud")
}
