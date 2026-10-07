package project

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
)

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}

// uniqLines drops a line that repeats an earlier one, keeping the order.
func uniqLines(s string) string {
	seen := map[string]bool{}
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// goneFiles says how many rendered files are missing, in the singular for
// one.
func goneFiles(n int) string {
	if n == 1 {
		return "1 rendered file in " + config.RenderDirName + " is gone"
	}
	return fmt.Sprintf("%d rendered files in %s are gone", n, config.RenderDirName)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// mtime is when p was last written; zero when it cannot be read. WriteUnit
// rewrites only on change, so for a render file it is the definition's birth.
func mtime(p string) time.Time {
	if st, err := os.Stat(p); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
