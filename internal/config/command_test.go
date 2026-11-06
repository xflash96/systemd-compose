package config

import (
	"testing"
)

// Shell syntax inside quotes, or in a list's own words, is the program's
// business: a shell given as [sh, -c, ...] or "sh -c '...'" is the way.
func TestLoad_TakesShellSyntaxInQuotesAsTheProgramsOwn(t *testing.T) {
	for _, y := range []string{
		`services: {a: {command: "sh -c 'echo one ; echo two | tee x > y'"}}`,
		`services: {a: {command: [sh, -c, "a && b > c"]}}`,
		`services: {a: {command: x, healthcheck: {test: [sh, -c, "curl -f x || exit 1"]}}}`,
		`services: {a: {command: x, build: {run: ["sh -c 'make && make install'"]}}}`,
		`services: {a: {command: "a --url 'http://h/?a=1&b=2'"}}`,
		`services: {a: {command: "a --max=1>2 --re=a|b --tpl=x<y"}}`,
	} {
		if _, err := loadYAML(t, y); err != nil {
			t.Errorf("%s: %v", y, err)
		}
	}
}

// systemd reads a word \; as a literal ; argument, the one find -exec
// ends with, so it is no shell syntax.
func TestLoad_TakesSystemdsEscapedSemicolon(t *testing.T) {
	p, err := loadYAML(t, `services: {a: {command: 'find . -name "*.x" -exec rm {} \;'}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Service("a").Command.Line; got != `find . -name "*.x" -exec rm {} \;` {
		t.Errorf("command line %q", got)
	}
	if _, err := loadYAML(t, `services: {a: {command: 'a \; b ;'}}`); err == nil {
		t.Error("a bare ; beside a \\; loaded")
	}
}
