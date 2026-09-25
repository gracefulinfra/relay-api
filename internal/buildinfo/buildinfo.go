// Package buildinfo reports the version metadata stamped into binaries at build time.
package buildinfo

import "runtime"

// Version and Commit are set with -ldflags "-X" at build time.
var (
	Version = "dev"
	Commit  = "unknown"
)

// Info is the build metadata for a binary.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// Get returns the build metadata for the named binary.
func Get(name string) Info {
	return Info{
		Name:      name,
		Version:   orDefault(Version, "dev"),
		Commit:    orDefault(Commit, "unknown"),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
