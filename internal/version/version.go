// Package version records the toolkit's build identity, stamped at release
// via -ldflags -X.
package version

var (
	Version = "dev"
	Commit  = "none"
)

func String() string {
	return "flowbench " + Version + " (commit " + Commit + ")"
}
