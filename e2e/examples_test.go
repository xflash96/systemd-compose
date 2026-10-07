//go:build e2e

package e2e

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// example copies examples/DIR into the test's temporary directory, as a
// project named by the tests' prefix, with env as its .env, and takes it
// down when the test ends.
func example(t *testing.T, dir string, env ...string) *project {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil && dir != "backups" {
		t.Skip("examples/" + dir + " needs python3")
	}
	p := newProject(t, dir, dir, "")
	src := filepath.Join(repo, "examples", dir)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			if d.Name() == ".systemd-compose" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(p.dir, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(p.dir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	p.file(".env", strings.Join(append([]string{"SYSTEMD_COMPOSE_PROJECT_NAME=" + p.name}, env...), "\n")+"\n")
	return p
}

// get is the body of an HTTP GET, or the error.
func get(url string) (string, error) {
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return string(body), fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return string(body), err
}

// examples/webapp, as its README runs it: migrate runs first, the api
// answers, and the worker catalogues a book the api queues.
func TestExamples_Webapp(t *testing.T) {
	port := freePort(t)
	p := example(t, "webapp", fmt.Sprintf("API_PORT=%d", port))
	url := fmt.Sprintf("http://127.0.0.1:%d/books", port)
	check(t, "up of examples/webapp exits 0", p.sc("up").ok())
	check(t, "  migrate has run", p.sc("ps").shows(p.name+`-migrate\.service .*active *exited`))
	books, err := get(url)
	check(t, "  the api lists no books", firstErr(err, equal("the books", strings.TrimSpace(books), "[]")))
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Post(url, "application/json", strings.NewReader(`{"title": "Dune"}`))
	if err == nil {
		resp.Body.Close()
		err = equal("the POST's status", resp.StatusCode, http.StatusCreated)
	}
	check(t, "  a POST queues a book", err)
	catalogued := func() error {
		var books string
		var err error
		for i := 0; i < 20; i++ {
			if books, err = get(url); err == nil && strings.Contains(books, `"catalogued"`) {
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return fmt.Errorf("the book is not catalogued: %s %v", books, err)
	}
	check(t, "  and the worker catalogues it", catalogued())
	check(t, "  logs worker says so", p.sc("logs", "--no-color", "worker").shows("catalogued 'Dune'"))
	check(t, "  down exits 0", p.sc("down").ok())
}

// examples/backups, as its README runs it: up arms the timers, and run
// runs a job now.
func TestExamples_Backups(t *testing.T) {
	p := example(t, "backups")
	check(t, "up of examples/backups exits 0", p.sc("up").ok())
	check(t, "  ps shows each timer's next run", firstErr(
		p.sc("ps").shows(p.name+`-backup\.timer .*next run`),
		p.sc("ps").shows(p.name+`-prune\.timer .*next run`)))
	check(t, "  run backup exits 0", p.sc("run", "-T", "backup").ok())
	archive := p.path("archive/notes-" + time.Now().Format("2006-01-02") + ".tar.gz")
	check(t, "  and makes today's archive of notes/", run("", nil, "tar", "-tzf", archive).shows(`^notes/todo\.txt$`))
	check(t, "  run prune exits 0, and keeps today's archive", firstErr(p.sc("run", "-T", "prune").ok(), exists(archive)))
	check(t, "  down exits 0", p.sc("down").ok())
}

// examples/socket, as its README runs it: every request made during a
// restart is answered, since systemd holds the socket.
func TestExamples_Socket(t *testing.T) {
	port := freePort(t)
	p := example(t, "socket", fmt.Sprintf("PORT=%d", port))
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	check(t, "up of examples/socket exits 0", p.sc("up").ok())
	body, err := get(url)
	check(t, "  hello answers", firstErr(err, that(strings.HasPrefix(body, "hello from pid "), "hello said %q", body)))
	type answer struct {
		pid string
		err error
	}
	answers := make(chan answer)
	go func() {
		for end := time.Now().Add(6 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			body, err := get(url)
			f := strings.Fields(body)
			pid := ""
			if len(f) > 3 {
				pid = strings.TrimSuffix(f[3], ",")
			}
			answers <- answer{pid, err}
		}
		close(answers)
	}()
	time.Sleep(2 * time.Second)
	restart := p.sc("restart", "hello")
	pids := map[string]bool{}
	var refused []error
	for a := range answers {
		if a.err != nil {
			refused = append(refused, a.err)
		}
		pids[a.pid] = true
	}
	check(t, "  restart hello exits 0", restart.ok())
	check(t, "  and every request made through it is answered", that(len(refused) == 0, "%d refused: %v", len(refused), refused))
	check(t, "  by the program before the restart and the one after", equal("programs that answered", len(pids), 2))
	check(t, "  down exits 0", p.sc("down").ok())
}
