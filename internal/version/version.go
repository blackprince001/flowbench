// Package version records the toolkit's build identity. Release builds stamp
// these variables via:
//
//	go build -ldflags "-X github.com/blackprince001/flowbench/internal/version.Version=v0.1.0 \
//	                   -X github.com/blackprince001/flowbench/internal/version.Commit=abc1234"
package version

var (
	// Version is the semantic version of the build, "dev" when unstamped.
	Version = "dev"
	// Commit is the short git commit the binary was built from.
	Commit = "none"
)

// String renders the identity line printed by `flowbench version`.
func String() string {
	return "flowbench " + Version + " (commit " + Commit + ")"
}
