package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/testenv"
	"gopkg.in/yaml.v3"
)

// Each project under examples/ loads and renders from its own directory
// (programs stood in), into the units its README describes.
func TestExamples_LoadAndRender(t *testing.T) {
	testenv.ClearOverrides(t)
	bin := testenv.FakeBin(t, "python3", "sh", "find")
	cases := map[string]string{
		"webapp":  "webapp.slice webapp-migrate.service webapp-api.service webapp-worker.service webapp.target",
		"backups": "backups.slice backups-backup.service backups-prune.service backups-backup.timer backups-prune.timer backups.target",
		"socket":  "hello.slice hello-hello.service hello-hello.socket hello.target",
	}
	for dir, want := range cases {
		p, err := config.Load(filepath.Join("../../examples", dir, config.ConfigFileName), config.Options{})
		if err != nil {
			t.Errorf("examples/%s: %v", dir, err)
			continue
		}
		units, err := render.Render(p, render.RenderOptions{Exe: "/opt/systemd-compose", SearchPath: []string{bin}})
		if err != nil {
			t.Errorf("examples/%s: %v", dir, err)
			continue
		}
		var names []string
		for _, u := range units {
			names = append(names, u.Name)
		}
		if got := strings.Join(names, " "); got != want {
			t.Errorf("examples/%s renders %s, want %s", dir, got, want)
		}
	}
	entries, _ := os.ReadDir("../../examples")
	for _, e := range entries {
		if _, ok := cases[e.Name()]; e.IsDir() && !ok {
			t.Errorf("examples/%s has no case here", e.Name())
		}
	}
}

// The README's key table names every key of a service, and each row's
// example loads and renders as a service's only key beside its command.
func TestReadme_KeyTableExamplesLoad(t *testing.T) {
	testenv.ClearOverrides(t)
	src := repoFile(t, "README.md")
	bin := testenv.FakeBin(t, "sleep", "python3", "uv", "make", "pg_isready")
	var keys []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| `([^`]+)` \\|").FindAllStringSubmatch(src, -1) {
		key, example := m[1], m[2]
		keys = append(keys, key)
		dir := t.TempDir()
		os.Mkdir(filepath.Join(dir, "web"), 0o755)
		os.WriteFile(filepath.Join(dir, "app.env"), nil, 0o644)
		y := "services:\n  db: {command: [sleep, infinity]}\n  s:\n    " + example + "\n"
		if key != "command" {
			y += "    command: [sleep, infinity]\n"
		}
		path := filepath.Join(dir, config.ConfigFileName)
		os.WriteFile(path, []byte(y), 0o644)
		p, err := config.Load(path, config.Options{})
		if err == nil {
			_, err = render.Render(p, render.RenderOptions{Exe: "/opt/systemd-compose", SearchPath: []string{bin}})
		}
		if err != nil {
			t.Errorf("README's %s example (%s): %v", key, example, err)
		}
	}
	want := slices.Clone(config.ServiceKeys)
	slices.Sort(keys)
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("README's key table has %s; want every key of a service: %s", keys, want)
	}
}

// docs/config.example.yaml is the reference users search: it must load and render
// as written (programs stood in), and show every key the parser takes.
func TestExampleYAML_LoadsAndRenders(t *testing.T) {
	testenv.ClearOverrides(t)
	src := repoFile(t, "docs/config.example.yaml")
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	for f, b := range map[string]string{"systemd-compose.yaml": src, ".env": ""} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := testenv.FakeBin(t, "postgres", "bookshelf-api", "bookshelf-web", "node", "curl", "sh", "find", "pgweb")
	p, err := config.Load(filepath.Join(dir, "systemd-compose.yaml"), config.Options{Profiles: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	units, err := render.Render(p, render.RenderOptions{Exe: "/opt/systemd-compose", SearchPath: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 15 { // the slice, 9 services, 3 timers, a socket, the target
		var names []string
		for _, u := range units {
			names = append(names, u.Name)
		}
		t.Errorf("rendered %d units: %v", len(units), names)
	}
}

// The man page has an entry for every verb of this program, under COMMANDS
// (inside a project, or anywhere), and for every service key, under THE
// PROJECT FILE, so one added later is added there too. An entry is a .TP
// paragraph headed by the word.
func TestManPage_NamesEveryVerbAndKey(t *testing.T) {
	src := repoFile(t, "docs/systemd-compose.1")
	section := func(name string) string {
		_, s, ok := strings.Cut(src, "\n.SH "+name+"\n")
		if !ok {
			t.Fatalf("docs/systemd-compose.1 has no %s section", name)
		}
		s, _, _ = strings.Cut(s, "\n.SH ")
		return s
	}
	entry := func(word string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^\.TP( [0-9]+)?\n\.BR? (\S+ \| )*` + regexp.QuoteMeta(word) + `\b`)
	}
	cmds, keys := section("COMMANDS"), section("THE PROJECT FILE")
	cmds, _, _ = strings.Cut(cmds, "\n.SS Outside a project\n") // its up, ps... are systemctl's
	for _, v := range ourVerbs {
		if !entry(v).MatchString(cmds) {
			t.Errorf("docs/systemd-compose.1 has no COMMANDS entry for %s", v)
		}
	}
	for _, k := range config.ServiceKeys {
		if !entry(k).MatchString(keys) {
			t.Errorf("docs/systemd-compose.1 has no THE PROJECT FILE entry for %s", k)
		}
	}
	// every other key, the top level's and those inside a service's maps,
	// is named in the section
	everyKey(func(_ string, k config.Key) {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(k.Name) + `\b`).MatchString(keys) {
			t.Errorf("docs/systemd-compose.1's THE PROJECT FILE does not name %s", k.Name)
		}
	})
	// a default it states is the spec's: ".B KEY ... .B WORD (the default)"
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "(the default)") {
			continue
		}
		var words []string // the .B words before it, nearest first
		for j := i - 1; j >= 0 && len(words) < 2; j-- {
			if w, ok := strings.CutPrefix(lines[j], ".B "); ok {
				words = append(words, w)
			}
		}
		if len(words) < 2 || !slices.Contains(defaults()[words[1]], words[0]) {
			t.Errorf("docs/systemd-compose.1 line %d calls %v the default, which the spec does not", i+1, words)
		}
	}
}

// docs/config.example.yaml shows every key of the spec, every word a key
// takes and every bound, and a key's own comment states its default. A
// guide's "# default X" comment on a key states the spec's default too.
func TestExampleYAML_ShowsEveryKeyWordDefaultAndBound(t *testing.T) {
	src := repoFile(t, "docs/config.example.yaml")
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				seen[n.Content[i].Value] = true
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&doc)
	items := commentItems(src)
	everyKey(func(parent string, k config.Key) {
		if !seen[k.Name] {
			t.Errorf("docs/config.example.yaml shows no %s:", k.Name)
		}
		if k.Default != "" && !statesDefault(items, parent, k) {
			t.Errorf("docs/config.example.yaml does not say %s's default is %s", k.Name, k.Default)
		}
	})
	shows := func(what, v string) {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(v) + `\b`).MatchString(src) {
			t.Errorf("docs/config.example.yaml does not show %s %s", what, v)
		}
	}
	walkSpec(func(_ string, f config.Form) {
		for _, w := range f.Words {
			shows("the word", w)
		}
		if f.Bounded {
			shows("the bound", strconv.FormatFloat(f.Min, 'g', -1, 64))
		}
		if f.Max > 0 {
			shows("the bound", strconv.FormatFloat(f.Max, 'g', -1, 64))
		}
		if f.MaxLen > 0 {
			shows("the length", strconv.Itoa(f.MaxLen))
		}
	})
	guides, _ := filepath.Glob("../../docs/*.md")
	inline := regexp.MustCompile(`(?m)^\s*([a-z_]+): .*#.*\bdefault:? ([^\s,;]+)`)
	for _, g := range guides {
		for _, m := range inline.FindAllStringSubmatch(repoFile(t, strings.TrimPrefix(g, "../../")), -1) {
			if !slices.Contains(defaults()[m[1]], m[2]) {
				t.Errorf("%s says %s's default is %s, which the spec does not", filepath.Base(g), m[1], m[2])
			}
		}
	}
}

// commentItem is one point of the example's comments: a run of comment
// lines is about the key named before the colon on its first line, its
// subject, and a point starts there or at a "- " line.
type commentItem struct{ subject, text string }

func commentItems(src string) []commentItem {
	var items []commentItem
	subject, in := "", false
	for _, line := range strings.Split(src, "\n") {
		c, ok := strings.CutPrefix(strings.TrimSpace(line), "#")
		c = strings.TrimSpace(c)
		switch {
		case !ok:
			in = false
		case !in:
			subject, _, _ = strings.Cut(c, ":")
			in = true
			items = append(items, commentItem{subject, c})
		case strings.HasPrefix(c, "- "):
			items = append(items, commentItem{subject, c})
		default:
			items[len(items)-1].text += " " + c
		}
	}
	return items
}

// statesDefault holds when a point about k says its default: one in k's
// own comment, or one in its parent's comment that names k.
func statesDefault(items []commentItem, parent string, k config.Key) bool {
	d := regexp.QuoteMeta(k.Default)
	says := regexp.MustCompile(`\b(default:?|defaults to) ` + d + `\b|\b` + d + ` \(default\)`)
	names := regexp.MustCompile(`\b` + regexp.QuoteMeta(k.Name) + `\b`)
	for _, it := range items {
		if (it.subject == k.Name || it.subject == parent && names.MatchString(it.text)) && says.MatchString(it.text) {
			return true
		}
	}
	return false
}

// defaults are the spec's defaults, by key name.
func defaults() map[string][]string {
	out := map[string][]string{}
	everyKey(func(_ string, k config.Key) {
		if k.Default != "" {
			out[k.Name] = append(out[k.Name], k.Default)
		}
	})
	return out
}

// walkSpec calls fn on every form of the spec, at every depth, with the
// name of the key it is a value of ("" at the top).
func walkSpec(fn func(key string, f config.Form)) {
	var walk func(key string, f config.Form)
	walk = func(key string, f config.Form) {
		fn(key, f)
		for _, k := range f.Keys {
			for _, kf := range k.Forms {
				walk(k.Name, kf)
			}
		}
		for _, item := range f.Items {
			walk(key, item)
		}
	}
	walk("", config.Spec)
}

// everyKey calls fn once on every key of the spec, with the name of the
// key it sits in ("" at the top).
func everyKey(fn func(parent string, k config.Key)) {
	seen := map[[2]string]bool{} // a form the spec shares is walked at each use
	walkSpec(func(key string, f config.Form) {
		for _, k := range f.Keys {
			if !seen[[2]string{key, k.Name}] {
				seen[[2]string{key, k.Name}] = true
				fn(key, k)
			}
		}
	})
}

// CONTRIBUTING.md names every package under internal/, and the README's
// features name every verb of this program's own.
func TestContributingAndReadme_NameEveryPackageAndVerb(t *testing.T) {
	contributing := repoFile(t, "CONTRIBUTING.md")
	entries, _ := os.ReadDir("..")
	for _, e := range entries {
		if e.IsDir() && !strings.Contains(contributing, "`internal/"+e.Name()+"`") {
			t.Errorf("CONTRIBUTING.md does not name internal/%s", e.Name())
		}
	}
	_, verbs, _ := strings.Cut(repoFile(t, "README.md"), "\n## Features\n")
	verbs, _, _ = strings.Cut(verbs, "\n## ")
	for _, v := range ourVerbs {
		if v != "version" && v != "help" && !strings.Contains(verbs, "`"+v+"`") {
			t.Errorf("README's features do not name %s", v)
		}
	}
}

// The README's install lines and scripts/install.sh name the directory a
// release archive unpacks to, which .goreleaser.yaml makes.
func TestReadme_NamesTheArchiveDirectory(t *testing.T) {
	cfg := repoFile(t, ".goreleaser.yaml")
	for _, want := range []string{`name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`, "wrap_in_directory: true", "project_name: systemd-compose"} {
		if !strings.Contains(cfg, want) {
			t.Errorf(".goreleaser.yaml no longer says %s", want)
		}
	}
	if !strings.Contains(repoFile(t, "README.md"), "systemd-compose_*_linux_amd64/systemd-compose") {
		t.Errorf("README's install lines do not name systemd-compose_*_linux_amd64/")
	}
	if !strings.Contains(repoFile(t, "scripts/install.sh"), "name=systemd-compose_${ver#v}_linux_$arch") {
		t.Errorf("scripts/install.sh does not name systemd-compose_VERSION_linux_ARCH")
	}
}

// Every relative link in the README, CONTRIBUTING.md, the guides and the
// examples' READMEs names a file or directory there, and its #anchor a
// heading of that file.
func TestDocs_LinksResolve(t *testing.T) {
	files := []string{"README.md", "CONTRIBUTING.md"}
	for _, glob := range []string{"docs/*.md", "examples/*/README.md"} {
		found, _ := filepath.Glob("../../" + glob)
		for _, f := range found {
			files = append(files, strings.TrimPrefix(f, "../../"))
		}
	}
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	for _, f := range files {
		for _, m := range link.FindAllStringSubmatch(prose(repoFile(t, f)), -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			path, anchor, _ := strings.Cut(target, "#")
			to := f
			if path != "" {
				to = filepath.Join(filepath.Dir(f), path)
			}
			if _, err := os.Stat(filepath.Join("../..", to)); err != nil {
				t.Errorf("%s links %s, which is not there", f, target)
				continue
			}
			if anchor != "" && !slices.Contains(headingAnchors(repoFile(t, to)), anchor) {
				t.Errorf("%s links %s, but %s has no such heading", f, target, to)
			}
		}
	}
}

// prose is markdown without its fenced code blocks.
func prose(md string) string {
	var out []string
	fenced := false
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "```") {
			fenced = !fenced
			continue
		}
		if !fenced {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// headingAnchors are the anchors GitHub gives a markdown file's headings:
// lowercased, spaces as -, other punctuation dropped, and -1, -2 after a
// repeat.
func headingAnchors(md string) []string {
	var out []string
	seen := map[string]int{}
	for _, l := range strings.Split(prose(md), "\n") {
		h, ok := strings.CutPrefix(strings.TrimLeft(l, "#"), " ")
		if !ok || !strings.HasPrefix(l, "#") {
			continue
		}
		var b strings.Builder
		for _, r := range strings.ToLower(h) {
			switch {
			case r == ' ':
				b.WriteRune('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(r)
			}
		}
		a := b.String()
		if n := seen[a]; n > 0 {
			out = append(out, fmt.Sprintf("%s-%d", a, n))
		} else {
			out = append(out, a)
		}
		seen[a]++
	}
	return out
}

// repoFile is a file of the repository, by its path from the top.
func repoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
