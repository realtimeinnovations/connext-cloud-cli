package app

import "github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo"

func VersionString() string {
	return buildinfo.VersionString()
}
