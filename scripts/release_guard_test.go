// Package scripts holds the shell scripts the Makefile runs. The Go files
// here test them; go test ./... from the root runs these alongside the
// library's tests.
package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReleaseGuardFloorFromOrigin reproduces openresponses#20. The
// version floor used to come from local tags, so a clone that had not
// fetched carried a stale floor and the guard approved a version that
// sorted below one already released, the one mistake with no remedy:
// the proxy and the checksum database keep every version forever. The
// floor and the tag-exists check now come from the tags origin has
// published, joined with the local tags make release writes before it
// pushes, and an unreachable origin refuses rather than guessing.
func TestReleaseGuardFloorFromOrigin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the release scripts are bash")
	}
	for _, tool := range []string{"git", "bash", "go"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("CI") != "" {
				t.Fatalf("%s not found in CI: %v", tool, err)
			}
			t.Skipf("%s not found: %v", tool, err)
		}
	}
	tests := []struct {
		name      string
		published []string // tags on origin
		forgotten []string // published tags the clone never learned of
		local     []string // tags written in the clone only, as make release writes the root tag
		cutOff    bool     // origin cannot be reached
		tag       string
		wantOK    bool
		want      string // substring of the script's output
	}{
		{
			name:      "stale clone cannot publish below the released root",
			published: []string{"v0.0.3", "v0.0.5"},
			forgotten: []string{"v0.0.5"},
			tag:       "v0.0.4",
			want:      "v0.0.4 does not sort above the current root release v0.0.5",
		},
		{
			name:      "a version above the released root passes",
			published: []string{"v0.0.3", "v0.0.5"},
			forgotten: []string{"v0.0.5"},
			tag:       "v0.0.6",
			wantOK:    true,
			want:      "v0.0.6 is safe to push",
		},
		{
			name:      "stale clone cannot publish below the released nested module",
			published: []string{"v0.0.3", "providers/x/v0.0.5"},
			forgotten: []string{"providers/x/v0.0.5"},
			tag:       "providers/x/v0.0.4",
			want:      "v0.0.4 does not sort above providers/x's current release v0.0.5",
		},
		{
			name:      "a tag origin has is not new, even when the clone lacks it",
			published: []string{"v0.0.3", "v0.0.5"},
			forgotten: []string{"v0.0.5"},
			tag:       "v0.0.5",
			want:      "tag v0.0.5 already exists on origin",
		},
		{
			// make release tags the root locally and guards each nested
			// module against it before anything is pushed, so the floor is
			// the union of local and published tags, not origin alone.
			name:      "a root tag written locally counts",
			published: []string{"v0.0.3"},
			local:     []string{"v0.0.4"},
			tag:       "providers/x/v0.0.4",
			wantOK:    true,
			want:      "root v0.0.4 is this commit",
		},
		{
			name:      "an unreachable origin refuses rather than guessing",
			published: []string{"v0.0.3"},
			cutOff:    true,
			tag:       "v0.0.4",
			want:      "cannot read the published tags from origin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			origin := filepath.Join(root, "origin")
			work := filepath.Join(root, "work")

			// origin: a repository carrying the scripts under test, a root
			// module and one nested module, with the published tags.
			writeTree(t, origin)
			git(t, origin, "init", "-q")
			git(t, origin, "add", "-A")
			git(t, origin, "commit", "-q", "-m", "init")
			for _, tag := range tt.published {
				git(t, origin, "tag", "-a", tag, "-m", tag)
			}

			// work: a clone that then forgets what it should have fetched,
			// or writes what make release writes.
			git(t, root, "clone", "-q", origin, work)
			for _, tag := range tt.forgotten {
				git(t, work, "tag", "-d", tag)
			}
			for _, tag := range tt.local {
				git(t, work, "tag", "-a", tag, "-m", tag)
			}
			if tt.cutOff {
				git(t, work, "remote", "set-url", "origin", filepath.Join(root, "nowhere"))
			}

			cmd := exec.Command("bash", "scripts/release-guard.sh", tt.tag)
			cmd.Dir = work
			cmd.Env = env()
			out, err := cmd.CombinedOutput()
			if (err == nil) != tt.wantOK {
				t.Errorf("exit error = %v, want ok = %v\n%s", err, tt.wantOK, out)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, out)
			}

			// The read is git ls-remote, which must not have fetched: the
			// clone still does not have the tags it never learned of.
			for _, tag := range tt.forgotten {
				probe := exec.Command("git", "rev-parse", "-q", "--verify", "refs/tags/"+tag)
				probe.Dir = work
				probe.Env = env()
				if probe.Run() == nil {
					t.Errorf("the guard fetched %s into the clone; the check must be read-only", tag)
				}
			}
		})
	}
}

// writeTree lays out the repository the test publishes: the scripts under
// test, copied from this directory, a root module and a nested module.
func writeTree(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"release-guard.sh", "versions.sh"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "scripts", name), data, 0o755)
	}
	write(t, filepath.Join(dir, "go.mod"), []byte("module example.com/root\n\ngo 1.25\n"), 0o644)
	write(t, filepath.Join(dir, "providers", "x", "go.mod"), []byte("module example.com/root/providers/x\n\ngo 1.25\n"), 0o644)
}

func write(t *testing.T, path string, data []byte, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
}

// env isolates git from the developer's configuration (signing, hooks,
// default branch) and go from the network: the guard asks whether the
// root version is on the proxy, and GOPROXY=off answers no without a
// request, which is the make release case the test wants.
func env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GOPROXY=off", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local", "GOWORK=off",
	)
}
