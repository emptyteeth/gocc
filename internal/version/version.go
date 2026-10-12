// Package version carries what the binaries say about themselves.
//
// Version is set at build time:
//
//	go build -ldflags "-X github.com/free/gocc/internal/version.Version=v1.2.3"
//
// The Makefile does that, from a git tag or the commit. A plain `go build`
// leaves "dev" here, which is the honest answer for a tree built that way.
package version

// Version names the build: a tag, a commit, or "dev" for one built without
// either.
var Version = "dev"

// Commit and Date say which commit the build came from and when, when the
// build was made with them.
var (
	Commit = ""
	Date   = ""
)

// String is the version with what else the build knows, as
// "v1.2.3 (3dec4af, 2026-10-13)", or just the version when it knows
// nothing more.
func String() string {
	s := Version
	var extra []string
	if Commit != "" {
		extra = append(extra, Commit)
	}
	if Date != "" {
		extra = append(extra, Date)
	}
	if len(extra) == 0 {
		return s
	}
	out := s + " ("
	for i, e := range extra {
		if i > 0 {
			out += ", "
		}
		out += e
	}
	return out + ")"
}
