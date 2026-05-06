package app

import "fmt"

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func VersionString() string {
	return fmt.Sprintf("rticloud %s\ncommit: %s\nbuilt: %s\n", version, commit, date)
}
