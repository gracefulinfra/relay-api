package buildinfo

import (
	"runtime"
	"testing"
)

func TestGet(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		commit      string
		wantVersion string
		wantCommit  string
	}{
		{name: "stamped", version: "1.2.3", commit: "abc123", wantVersion: "1.2.3", wantCommit: "abc123"},
		{name: "unstamped defaults", version: "", commit: "", wantVersion: "dev", wantCommit: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origVersion, origCommit := Version, Commit
			t.Cleanup(func() { Version, Commit = origVersion, origCommit })
			Version, Commit = tt.version, tt.commit

			got := Get("relay-test")
			if got.Name != "relay-test" {
				t.Errorf("Name = %q, want %q", got.Name, "relay-test")
			}
			if got.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVersion)
			}
			if got.Commit != tt.wantCommit {
				t.Errorf("Commit = %q, want %q", got.Commit, tt.wantCommit)
			}
			if want := runtime.GOOS + "/" + runtime.GOARCH; got.Platform != want {
				t.Errorf("Platform = %q, want %q", got.Platform, want)
			}
		})
	}
}
