// Package version records the toolkit's build identity, stamped at release
// via -ldflags -X.
package version

import "strings"

var (
	Version = "dev"
	Commit  = "none"
)

func String() string {
	return "flowbench " + Version + " (commit " + Commit + ")"
}

// UserAgent is how flowbench identifies its traffic to a target. Whoever runs
// the target reads it in an access log or keys an allow-list on it, so it is a
// stable `flowbench/<version>` token: the tag carries a leading v, a
// User-Agent product version conventionally does not.
func UserAgent() string {
	return "flowbench/" + strings.TrimPrefix(Version, "v")
}
