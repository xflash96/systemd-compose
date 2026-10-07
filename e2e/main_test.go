//go:build e2e

// Package e2e drives systemd-compose through throwaway projects on a real
// systemd user manager, and checks that nothing is left behind.
//
//	go test -tags e2e -count=1 -v ./e2e/          the binary built from this tree
//	SC=/path/to/systemd-compose go test -tags e2e ./e2e/
//
// It needs a user manager (waited for up to 60 s, for one that lingering
// just started) on cgroup v2, systemd's tools, sh, coreutils and script
// (util-linux), python3 for the examples, and Go unless SC names a
// binary. Every project's name starts with a prefix no one else uses, and
// is taken down when its test ends. ci/container runs the compiled tests
// in a container that boots systemd; E2E_REPO then names the source tree
// they read README.md, docs/ and examples/ from.
package e2e

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	sc      string // the binary under test
	repo    string // the source tree: README.md, docs/, examples/
	prefix  string // every project's name starts with it
	unitDir string // where the user manager keeps its links
)

func TestMain(m *testing.M) {
	os.Exit(setUp(m))
}

func setUp(m *testing.M) int {
	// The tool's own overrides would rename a project, or change its
	// profiles, away from the throwaway one a test takes down.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "SYSTEMD_COMPOSE_") {
			os.Unsetenv(k)
		}
	}
	var err error
	if repo, err = filepath.Abs(cmp.Or(os.Getenv("E2E_REPO"), "..")); err != nil {
		fmt.Println(err)
		return 1
	}
	if sc = os.Getenv("SC"); sc == "" {
		dir, err := os.MkdirTemp("", "e2e-bin-")
		if err != nil {
			fmt.Println(err)
			return 1
		}
		defer os.RemoveAll(dir)
		sc = filepath.Join(dir, "systemd-compose")
		build := exec.Command("go", "build", "-o", sc, ".")
		build.Dir, build.Env = repo, append(os.Environ(), "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			fmt.Printf("build failed: %v\n%s", err, out)
			return 1
		}
	}
	reachable := func() bool { return exec.Command("systemctl", "--user", "show", "-p", "Version").Run() == nil }
	for i := 0; i < 60 && !reachable(); i++ {
		time.Sleep(time.Second)
	}
	if !reachable() {
		fmt.Println("no user manager here (systemctl --user)")
		return 1
	}
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		fmt.Println("needs cgroup v2 (a build reads its slice's memory.max)")
		return 1
	}
	if unitDir = managerUnitDir(); unitDir == "" {
		fmt.Println("the user manager's UnitPath names no persistent systemd/user.control")
		return 1
	}
	prefix = fmt.Sprintf("e2e%d", os.Getpid())
	version, _ := exec.Command(sc, "--version").Output()
	fmt.Println(strings.ReplaceAll(strings.TrimSpace(string(version)), "\n", " "))

	code := m.Run()

	// Nothing is left behind, an on-demand slice included. Each test took
	// its projects down; what is still loaded is a failure, swept here.
	time.Sleep(time.Second)
	if left := loaded(prefix); left != "" {
		fmt.Printf("FAIL: units of the tests are still loaded:\n%s\n", left)
		code = 1
	}
	sweep()
	return code
}

// managerUnitDir is where the user manager links units: the first entry of
// its UnitPath that ends in /systemd/user.control and is not under /run,
// without the .control (Manager.UnitDir's rule, internal/systemd); "" when
// there is none.
func managerUnitDir() string {
	out, _ := exec.Command("systemctl", "--user", "show", "-p", "UnitPath", "--value").Output()
	for _, d := range strings.Fields(string(out)) {
		if strings.HasSuffix(d, "/systemd/user.control") && !strings.HasPrefix(d, "/run/") {
			return strings.TrimSuffix(d, ".control")
		}
	}
	return ""
}

// loaded lists the units whose names start with p.
func loaded(p string) string {
	out, _ := exec.Command("systemctl", "--user", "list-units", "--all", "--plain", "--no-legend", p+"*").Output()
	return strings.TrimSpace(string(out))
}

// sweep removes whatever a failed test left: links, running units and
// failed states of the tests' projects.
func sweep() {
	for _, dir := range []string{unitDir, filepath.Join(unitDir, "default.target.wants")} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), prefix) {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
	exec.Command("systemctl", "--user", "stop", prefix+"*").Run()
	exec.Command("systemctl", "--user", "reset-failed", prefix+"*").Run()
	exec.Command("systemctl", "--user", "daemon-reload").Run()
}
