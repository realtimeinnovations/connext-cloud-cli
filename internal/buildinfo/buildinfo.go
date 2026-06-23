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

func Version() string {
	return version
}

func VersionString() string {
	return fmt.Sprintf("%s\ncommit: %s\nbuilt: %s\n", VersionLine(), commit, date)
}
