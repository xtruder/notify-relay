package buildinfo

import "fmt"

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String(name string) string {
	return fmt.Sprintf("%s version=%s commit=%s date=%s", name, Version, Commit, Date)
}
