// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package buildinfo

import "fmt"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func VersionLine() string {
	return fmt.Sprintf("rticloud %s", version)
}

func VersionString() string {
	return fmt.Sprintf("%s\ncommit: %s\nbuilt: %s\n", VersionLine(), commit, date)
}
