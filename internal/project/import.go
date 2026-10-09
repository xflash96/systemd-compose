package project

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// Import prints a systemd-compose.yaml that runs a service unit of the
// user instance, hand-written or installed, as a project's service, and
// the commands that retire the unit. The directives go under unit: as the
// unit file and its own drop-ins write them, so systemd reads the same
// service; the ones unit: refuses become their keys (working_dir:,
// environment:, env_file:). It changes nothing.
func Import(m *systemd.Manager, unit, service string) error {
	if !strings.Contains(unit, ".") {
		unit += ".service"
	}
	if !strings.HasSuffix(unit, ".service") {
		return fmt.Errorf("import: %s is no service; a timer becomes schedule: and a socket listen: in the service it starts (import that service)", unit)
	}
	if strings.Contains(unit, "@") {
		return fmt.Errorf("import: %s is a template or its instance; write the service by hand from systemctl --user cat %s", unit, unit)
	}
	if service == "" {
		service = strings.TrimSuffix(unit, ".service")
	}
	if !config.ServiceNameOK(service) {
		return fmt.Errorf("import: %q is no service name (letters, digits, _ and -, not starting with -); name it: import %s NAME", service, unit)
	}
	props, err := unitProperties(m, unit, "LoadState", "FragmentPath", "DropInPaths", "TriggeredBy")
	if err != nil {
		return err
	}
	switch props["LoadState"] {
	case "loaded":
	case "not-found":
		return fmt.Errorf("import: no unit %s on the %s", unit, m.ScopeName())
	default:
		return fmt.Errorf("import: %s is %s", unit, props["LoadState"])
	}
	fragment := props["FragmentPath"]
	if fragment == "" {
		return fmt.Errorf("import: %s has no unit file (a transient or generated unit)", unit)
	}
	if real, err := filepath.EvalSymlinks(fragment); err == nil && filepath.Base(filepath.Dir(real)) == config.RenderDirName {
		return fmt.Errorf("import: %s is a project's already (%s)", unit, filepath.Dir(filepath.Dir(real)))
	}

	var notes []string
	files := []string{fragment}
	for _, d := range strings.Fields(props["DropInPaths"]) {
		if filepath.Base(filepath.Dir(d)) == unit+".d" {
			files = append(files, d)
		} else {
			// a prefix (foo-.service.d) or top-level (service.d) drop-in:
			// it applies by name, and still applies, or not, by the new one
			notes = append(notes, "left out: the drop-in "+d+", which applies by name, not to this unit alone")
		}
	}
	var dirs []directive
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		dirs = append(dirs, parseUnitFile(string(data))...)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	svc, more := importService(dirs, home)
	notes = append(notes, more...)
	for _, t := range strings.Fields(props["TriggeredBy"]) {
		notes = append(notes, "left out: "+t+", which starts this unit; "+triggerKey(t))
	}

	doc := &yaml.Node{Kind: yaml.MappingNode}
	services := &yaml.Node{Kind: yaml.MappingNode}
	services.Content = append(services.Content, scalarNode(service), svc)
	doc.Content = append(doc.Content, scalarNode("services"), services)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	text := buf.String()
	loadErr := importLoads(text)

	sources := fragment
	if len(files) > 1 {
		sources += " and its drop-ins"
	}
	fmt.Printf("# %s as a project's service, from %s\n", unit, sources)
	for _, n := range notes {
		fmt.Println("# " + n)
	}
	fmt.Print(text)
	fmt.Println("# To move it into a project: put this in the project's systemd-compose.yaml")
	fmt.Println("# (into one that exists, the service under its services:), then")
	fmt.Printf("#   systemctl --user disable --now %s\n", unit)
	unitDir, _ := m.UnitDir()
	if st, err := os.Lstat(filepath.Join(unitDir, unit)); err == nil && st.Mode().IsRegular() {
		fmt.Printf("#   rm %s\n", shellWord(filepath.Join(unitDir, unit)))
	} else {
		fmt.Printf("#   (its file, %s, stays: disable keeps it from starting)\n", fragment)
	}
	if st, err := os.Stat(filepath.Join(unitDir, unit+".d")); err == nil && st.IsDir() {
		fmt.Printf("#   rm -r %s\n", shellWord(filepath.Join(unitDir, unit+".d")))
	}
	fmt.Println("#   systemctl --user daemon-reload")
	fmt.Println("#   systemd-compose up")
	fmt.Printf("# It runs as PROJECT-%s.service: a unit that names %s must name that instead.\n", service, unit)
	if loadErr != nil {
		return fmt.Errorf("import: the yaml above does not load as it is: %v; edit it before up", loadErr)
	}
	return nil
}

// directive is one Key=value line of a unit file.
type directive struct{ section, key, value string }

// parseUnitFile reads a unit file's directives in order, as systemd does:
// a line ending in \ goes on in the next, # and ; start comments.
func parseUnitFile(text string) []directive {
	var out []directive
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	pending := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if pending == "" && (line == "" || line[0] == '#' || line[0] == ';') {
			continue
		}
		if pending != "" && line != "" && (line[0] == '#' || line[0] == ';') {
			continue // a comment inside a continued line
		}
		if strings.HasSuffix(line, `\`) {
			pending += strings.TrimSuffix(line, `\`) + " "
			continue
		}
		line, pending = pending+line, ""
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			continue
		}
		out = append(out, directive{section, strings.TrimSpace(k), strings.TrimSpace(v)})
	}
	return out
}

// importService is the service entry for a unit's directives, and what it
// left out. An empty value resets the directive, as in systemd, so a
// drop-in's reset takes the lines before it away.
func importService(dirs []directive, home string) (*yaml.Node, []string) {
	type key struct{ section, name string }
	values := map[key][]string{}
	var order []key
	for _, d := range dirs {
		k := key{d.section, d.key}
		if _, seen := values[k]; !seen {
			order = append(order, k)
		}
		if d.value == "" {
			values[k] = []string{}
			continue
		}
		values[k] = append(values[k], d.value)
	}
	var notes []string
	svc := &yaml.Node{Kind: yaml.MappingNode}
	// a user unit with no WorkingDirectory= runs in the home directory;
	// a project's service runs in the yaml's unless working_dir: says so
	wd, why := scalarNode(strings.ReplaceAll(home, "$", "$$")), "where the unit ran: a user unit's default"
	if vs := values[key{"Service", "WorkingDirectory"}]; len(vs) > 0 {
		w := strings.TrimPrefix(vs[len(vs)-1], "-")
		if p, ok := homePath(w, home); ok {
			wd, why = scalarNode(p), ""
		} else {
			wd = nil
			notes = append(notes, "left out: WorkingDirectory="+w+"; working_dir: takes a path with no %")
		}
	}
	if wd != nil {
		wd.LineComment = why
		svc.Content = append(svc.Content, scalarNode("working_dir"), wd)
	}
	if vs := values[key{"Service", "Environment"}]; len(vs) > 0 {
		env := &yaml.Node{Kind: yaml.MappingNode}
		at := map[string]*yaml.Node{}
		for _, v := range vs {
			for _, w := range envWords(v) {
				name, val, ok := strings.Cut(w, "=")
				if !ok {
					continue // systemd ignores a word with no =
				}
				val = strings.ReplaceAll(val, "$", "$$") // literal in Environment=
				if n := at[name]; n != nil {
					n.Value = val // a later assignment wins
					continue
				}
				at[name] = scalarNode(val)
				env.Content = append(env.Content, scalarNode(name), at[name])
			}
		}
		svc.Content = append(svc.Content, scalarNode("environment"), env)
	}
	if vs := values[key{"Service", "EnvironmentFile"}]; len(vs) > 0 {
		files := &yaml.Node{Kind: yaml.SequenceNode}
		for _, v := range vs {
			path, optional := strings.CutPrefix(v, "-")
			p, ok := homePath(path, home)
			if !ok || strings.ContainsAny(p, "*?[") {
				notes = append(notes, "left out: EnvironmentFile="+v+"; env_file: takes one named file, with no %")
				continue
			}
			if optional {
				files.Content = append(files.Content, &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{
					scalarNode("path"), scalarNode(p), scalarNode("required"), {Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}}})
			} else {
				files.Content = append(files.Content, scalarNode(p))
			}
		}
		if len(files.Content) > 0 {
			svc.Content = append(svc.Content, scalarNode("env_file"), files)
		}
	}

	unit := &yaml.Node{Kind: yaml.MappingNode}
	sections := map[string]*yaml.Node{}
	var install []string
	for _, k := range order {
		vs := values[k]
		switch {
		case len(vs) == 0:
			continue
		case k.section == "Install":
			install = append(install, k.name+"="+strings.Join(vs, " "))
			continue
		case k.section == "Service" && slices.Contains([]string{"WorkingDirectory", "Environment", "EnvironmentFile"}, k.name):
			continue // a key of its own, above
		case k.section == "Service" && k.name == "Slice":
			notes = append(notes, "left out: Slice="+vs[len(vs)-1]+"; the service runs in the project's slice")
			continue
		case k.section != "Unit" && k.section != "Service":
			notes = append(notes, "left out: ["+k.section+"] "+k.name+"=; a service's unit: takes [Unit] and [Service]")
			continue
		}
		sec := sections[k.section]
		if sec == nil {
			sec = &yaml.Node{Kind: yaml.MappingNode}
			sections[k.section] = sec
			unit.Content = append(unit.Content, scalarNode(k.section), sec)
		}
		var v *yaml.Node
		if len(vs) == 1 {
			v = scalarNode(vs[0])
		} else {
			v = &yaml.Node{Kind: yaml.SequenceNode}
			for _, s := range vs {
				v.Content = append(v.Content, scalarNode(s))
			}
		}
		sec.Content = append(sec.Content, scalarNode(k.name), v)
	}
	if len(install) > 0 {
		notes = append(notes, "left out: [Install] "+strings.Join(install, ", ")+"; the project's target starts its services at boot")
	}
	if len(unit.Content) > 0 {
		svc.Content = append(svc.Content, scalarNode("unit"), unit)
	}
	return svc, notes
}

// homePath is an absolute path of systemd's, with ~ or %h for home, as
// the yaml writes it: home spelt out (these keys take no % or ~, and a
// $VAR comes from the .env only), and a $ as $$. false for a path the
// yaml cannot write: one with another %, or a relative one.
func homePath(p, home string) (string, bool) {
	for _, h := range []string{"~", "%h"} {
		if p == h || strings.HasPrefix(p, h+"/") {
			p = home + p[len(h):]
			break
		}
	}
	if strings.Contains(p, "%") || !filepath.IsAbs(p) {
		return "", false
	}
	return strings.ReplaceAll(p, "$", "$$"), true
}

// envWords splits an Environment= value into its assignments, as systemd
// does: quotes group, and a backslash escapes.
func envWords(s string) []string {
	var words []string
	var w strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
			switch e := s[i]; e {
			case 'n':
				w.WriteByte('\n')
			case 't':
				w.WriteByte('\t')
			case 's':
				w.WriteByte(' ')
			case 'x':
				if i+2 < len(s) {
					if b, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
						w.WriteByte(byte(b))
						i += 2
						break
					}
				}
				w.WriteByte(e)
			default:
				w.WriteByte(e)
			}
			in = true
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote, in = c, true
		case quote == 0 && (c == ' ' || c == '\t'):
			if in {
				words = append(words, w.String())
				w.Reset()
				in = false
			}
		default:
			w.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, w.String())
	}
	return words
}

// triggerKey says which key stands for a unit that starts a service.
func triggerKey(unit string) string {
	switch filepath.Ext(unit) {
	case ".timer":
		return "give the service schedule: (docs/scheduled-jobs.md), and its other [Timer] settings under unit: Timer:"
	case ".socket":
		return "give the service listen:, and its other [Socket] settings under unit: Socket:"
	}
	return "retire it with the unit, or point it at the new name"
}

// unitProperties reads properties of a unit from the manager.
func unitProperties(m *systemd.Manager, unit string, names ...string) (map[string]string, error) {
	args := []string{"show"}
	for _, n := range names {
		args = append(args, "-p", n)
	}
	out, err := m.Cmd("systemctl", append(args, "--", unit)...).Output()
	if err != nil {
		return nil, fmt.Errorf("import: systemctl show %s: %w", unit, err)
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = v
		}
	}
	return props, nil
}

// importLoads checks the import as up would read and render it, in a
// scratch directory: a directive the tool writes itself collides there.
func importLoads(text string) error {
	dir, err := os.MkdirTemp("", "systemd-compose-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, config.ConfigFileName)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	p, err := config.Load(path, config.Options{Name: "import"})
	if err != nil {
		return err
	}
	opt, err := render.DefaultRenderOptions()
	if err != nil {
		return err
	}
	_, err = render.Render(p, opt)
	return err
}

func scalarNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}
