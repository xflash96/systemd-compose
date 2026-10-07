package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Service is one entry of services:, validated.
type Service struct {
	Name        string
	Command     Words
	Entrypoint  Words  // ExecStart = entrypoint + command, as compose without an image
	WorkingDir  string // absolute; defaults to the project dir
	Environment []KV
	EnvFiles    []EnvFile
	Restart     *Restart
	DependsOn   []Dependency
	Schedule    *Schedule
	Unit        PassThrough
	OnChange    string // its on_change: word
	Healthcheck *Healthcheck
	Oneshot     bool
	Build       *Build
	Resources   *Resources
	Listen      []string // ListenStream= addresses of the service's socket
	Profiles    []string // empty: always enabled
}

// Restart is restart:, as Restart= and RestartSec=.
type Restart struct {
	Policy  string // no | on-failure | always
	Written string // the yaml's own word, for messages: unless-stopped is always
	Delay   string // RestartSec, optional
}

// Build is build: steps that up runs before it starts the service.
type Build struct {
	Run []string
	// Creates is where up looks for what the build makes: absolute once
	// loaded (the yaml's path is relative to the service's working_dir,
	// where the build runs); "" when the build declares none, which up
	// then skips unless --build.
	Creates string
}

// Resources are the caps of resources:, on a service or on the project.
type Resources struct {
	Memory string  // MemoryMax
	CPUs   float64 // CPUQuota = CPUs*100%
	PIDs   int     // TasksMax
}

// ServiceKeys are the keys a service takes.
var ServiceKeys = serviceMap.names()

func parseService(name string, node *yaml.Node, p *Project) (*Service, error) {
	ctx := "service " + name
	m, err := mapping(node, ctx)
	if err != nil {
		return nil, err
	}
	m = withoutExtensions(m) // allUnknownKeys has checked its keys
	svc := &Service{Name: name, WorkingDir: p.Dir, OnChange: Default("on_change")}

	// The raw form first: `unit: Service: ExecStart:` replaces `command:`
	// for the cases the refusals below cannot spell (a shell, a specifier,
	// a path with a space). Then it is the author's ExecStart, verified by
	// systemd-analyze but not resolved or refused here.
	if n := m.get("unit"); n != nil {
		sched, listen := m.get("schedule") != nil, m.get("listen") != nil
		if svc.Unit, err = parsePassThrough(n, ctx, sched, listen); err != nil {
			return nil, err
		}
	}
	rawExec := len(svc.Unit.Values("Service", "ExecStart")) > 0
	progLine := 0 // where the program word is written: entrypoint's, else command's
	for _, key := range []string{"entrypoint", "command"} {
		n := m.get(key)
		if n == nil {
			continue
		}
		if rawExec {
			return nil, fmt.Errorf("line %d: %s: %s: and unit: Service: ExecStart: both write ExecStart=; keep one", n.Line, ctx, key)
		}
		w, err := parseWords(n, ctx+": "+key)
		if err != nil {
			return nil, err
		}
		if progLine == 0 {
			progLine = n.Line
		}
		if key == "command" {
			svc.Command = w
		} else {
			svc.Entrypoint = w
		}
	}
	prog := svc.Entrypoint
	if prog.Empty() {
		prog = svc.Command
	}
	if prog.Empty() && !rawExec {
		return nil, fmt.Errorf("line %d: %s: command: is required (or entrypoint:, or unit: Service: ExecStart: for the raw form)", node.Line, ctx)
	}
	if !prog.Empty() {
		if err := refuseProgram(prog.program(), ctx, progLine); err != nil {
			return nil, err
		}
	}

	if n := m.get("working_dir"); n != nil {
		s, err := scalar(n, ctx+": working_dir")
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, fmt.Errorf("line %d: %s: working_dir: is empty; leave it out for the yaml's directory", n.Line, ctx)
		}
		if err := literalPath(s, "working_dir", ctx, n.Line); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(s) {
			s = filepath.Join(p.Dir, s)
		}
		svc.WorkingDir = filepath.Clean(s)
	}

	if n := m.get("environment"); n != nil {
		if svc.Environment, err = parseEnvironment(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("env_file"); n != nil {
		if svc.EnvFiles, err = parseEnvFiles(n, ctx, p.Dir); err != nil {
			return nil, err
		}
	}
	if n := m.get("restart"); n != nil {
		if svc.Restart, err = parseRestart(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("depends_on"); n != nil {
		if svc.DependsOn, err = parseDependsOn(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("schedule"); n != nil {
		if svc.Schedule, err = parseSchedule(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("on_change"); n != nil {
		s, err := scalar(n, ctx+": on_change")
		if err != nil {
			return nil, err
		}
		if !slices.Contains(onChange.Words, s) {
			return nil, fmt.Errorf("line %d: %s: on_change: %q; use %s", n.Line, ctx, s, orList(onChange.Words))
		}
		svc.OnChange = s
	}
	if n := m.get("healthcheck"); n != nil {
		if svc.Healthcheck, err = parseHealthcheck(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("oneshot"); n != nil {
		if err := n.Decode(&svc.Oneshot); err != nil {
			return nil, fmt.Errorf("line %d: %s: oneshot: %v", n.Line, ctx, err)
		}
	}
	if n := m.get("build"); n != nil {
		if svc.Build, err = parseBuild(n, ctx); err != nil {
			return nil, err
		}
	}
	if n := m.get("resources"); n != nil {
		if svc.Resources, err = parseResources(n, ctx+": resources"); err != nil {
			return nil, err
		}
	}
	if n := m.get("listen"); n != nil {
		if svc.Listen, err = parseListen(n, ctx, p.Dir); err != nil {
			return nil, err
		}
	}
	if n := m.get("profiles"); n != nil {
		if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
			return nil, fmt.Errorf("line %d: %s: profiles: is a non-empty list of names", n.Line, ctx)
		}
		for _, item := range n.Content {
			s, err := scalar(item, ctx+": profiles")
			if err != nil {
				return nil, err
			}
			if !reProfile.MatchString(s) {
				return nil, fmt.Errorf("line %d: %s: profiles: %q: a profile name is letters, digits, _ . and -, starting with a letter or digit", item.Line, ctx, s)
			}
			svc.Profiles = append(svc.Profiles, s)
		}
	}

	// Cross-key rules within one service.
	if svc.OnChange == "start-only" {
		for _, d := range svc.DependsOn {
			if d.Restart {
				return nil, fmt.Errorf("line %d: %s: on_change: start-only contradicts depends_on: %s: restart: true (PartOf= would restart it anyway); drop one", node.Line, ctx, d.Service)
			}
		}
	}
	if svc.Schedule != nil && svc.Restart != nil && svc.Restart.Policy == "always" {
		return nil, fmt.Errorf("line %d: %s: schedule: with restart: %s would restart a finished job forever; drop one", node.Line, ctx, svc.Restart.Written)
	}
	if svc.Build != nil { // after every key: creates: needs the final working_dir
		if c := svc.Build.Creates; c != "" && !filepath.IsAbs(c) {
			svc.Build.Creates = filepath.Join(svc.WorkingDir, c)
		}
		for _, kv := range svc.Environment {
			if UsesSpecifier(kv.Value) {
				return nil, fmt.Errorf("line %d: %s: build: runs in the service's environment through systemd-run, which does not expand specifiers, and environment: %s uses one", node.Line, ctx, kv.Key)
			}
		}
	}
	if svc.Schedule != nil && svc.Healthcheck != nil {
		return nil, fmt.Errorf("line %d: %s: healthcheck: on a scheduled job has nothing to gate; drop one", node.Line, ctx)
	}
	if svc.Oneshot && svc.Healthcheck != nil {
		// its probe would run only after the program exits, and the start
		// timeout it brings would cut the job short
		return nil, fmt.Errorf("line %d: %s: healthcheck: on a oneshot has nothing to gate: it is done when its program exits (a dependent waits for that with depends_on: {condition: service_completed_successfully}); drop one", node.Line, ctx)
	}
	if svc.Oneshot && svc.Restart != nil && svc.Restart.Policy == "always" {
		return nil, fmt.Errorf("line %d: %s: oneshot: true and restart: always (or unless-stopped) cannot go together: systemd refuses to restart a oneshot that succeeded; use restart: on-failure, or drop oneshot:", node.Line, ctx)
	}
	if len(svc.Listen) > 0 && (svc.Schedule != nil || svc.Oneshot) {
		return nil, fmt.Errorf("line %d: %s: listen: is for a long-running service that accepts its sockets; a scheduled job or a oneshot has none to accept", node.Line, ctx)
	}
	return svc, nil
}

func parseRestart(n *yaml.Node, ctx string) (*Restart, error) {
	r := &Restart{}
	switch n.Kind {
	case yaml.ScalarNode:
		r.Policy = n.Value
	case yaml.MappingNode:
		m, err := mapping(n, ctx+": restart")
		if err != nil {
			return nil, err
		}
		if err := unknownKeys(m, ctx+": restart", restartMap.names()...); err != nil {
			return nil, err
		}
		pn := m.get("policy")
		if pn == nil {
			return nil, fmt.Errorf("line %d: %s: restart: policy: is required in the map form", n.Line, ctx)
		}
		if r.Policy, err = scalar(pn, ctx+": restart: policy"); err != nil {
			return nil, err
		}
		if dn := m.get("delay"); dn != nil {
			if r.Delay, err = duration(dn, ctx+": restart: delay"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("line %d: %s: restart: is a policy or %s", n.Line, ctx, restartMap.braces())
	}
	r.Written = r.Policy
	switch {
	case r.Policy == "unless-stopped":
		// compose's habit, and systemd's always already is it: a unit
		// stopped by hand is never restarted. (At boot the target starts it
		// again, where docker would leave it stopped.)
		r.Policy = "always"
	case slices.Contains(restartPolicy.Words, r.Policy):
	case strings.HasPrefix(r.Policy, "on-failure:"):
		return nil, fmt.Errorf("line %d: %s: restart: %q has no faithful systemd form: docker counts automatic restarts and forgets them on a manual start, systemd's start limit counts every start, manual ones included; write restart: on-failure and set unit: Unit: StartLimitBurst: and StartLimitIntervalSec: yourself", n.Line, ctx, r.Policy)
	default:
		return nil, fmt.Errorf("line %d: %s: restart: %q; use %s", n.Line, ctx, r.Policy, orList(restartPolicy.Words))
	}
	return r, nil
}

func parseBuild(n *yaml.Node, ctx string) (*Build, error) {
	b := &Build{}
	m, err := mapping(n, ctx+": build")
	if err != nil {
		return nil, err
	}
	if err := unknownKeys(m, ctx+": build", buildMap.names()...); err != nil {
		return nil, err
	}
	rn := m.get("run")
	if rn == nil || rn.Kind != yaml.SequenceNode || len(rn.Content) == 0 {
		return nil, fmt.Errorf("line %d: %s: build: run: is a non-empty list of commands", n.Line, ctx)
	}
	for _, item := range rn.Content {
		if item.Kind == yaml.SequenceNode {
			return nil, fmt.Errorf("line %d: %s: build: run: each step is one command written as a string, not a list (a list is command:'s form); for several commands in one step write: sh -c 'a && b'", item.Line, ctx)
		}
		s, err := scalar(item, ctx+": build: run")
		if err != nil {
			return nil, err
		}
		// A step runs through systemd-run, which expands no specifier: a %
		// reaches the program as written (date +%s), so only the shell
		// checks apply.
		first, _, err := FirstWord(s)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: build: run: %v", item.Line, ctx, err)
		}
		if err := refuseProgram(first, ctx+": build: run", item.Line); err != nil {
			return nil, err
		}
		if err := shellSyntax(s, ctx+": build: run", item.Line, "sh -c '...'", "write the full path"); err != nil {
			return nil, err
		}
		b.Run = append(b.Run, s)
	}
	if cn := m.get("creates"); cn != nil {
		if b.Creates, err = scalar(cn, ctx+": build: creates"); err != nil {
			return nil, err
		}
		if b.Creates == "" {
			return nil, fmt.Errorf("line %d: %s: build: creates: is empty; leave it out for a build that runs with build or up --build", cn.Line, ctx)
		}
		if err := literalPath(b.Creates, "build: creates", ctx, cn.Line); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func parseResources(n *yaml.Node, ctx string) (*Resources, error) {
	r := &Resources{}
	m, err := mapping(n, ctx)
	if err != nil {
		return nil, err
	}
	if err := unknownKeys(m, ctx, resourcesMap.names()...); err != nil {
		return nil, err
	}
	if mn := m.get("memory"); mn != nil {
		s, err := scalar(mn, ctx+": memory")
		if err != nil {
			return nil, err
		}
		if !reMemory.MatchString(s) {
			return nil, fmt.Errorf("line %d: %s: memory: %q; use a positive size in bytes with an optional K, M, G or T suffix, e.g. 512M", mn.Line, ctx, s)
		}
		r.Memory = s
	}
	if cn := m.get("cpus"); cn != nil {
		s, err := scalar(cn, ctx+": cpus")
		if err != nil {
			return nil, err
		}
		// CPUQuota= takes hundredths of a percent, below some 214748 CPUs;
		// below a thousandth systemd raises the quota without a word
		k, _ := resourcesMap.key("cpus")
		lo, hi := k.Forms[0].Min, k.Forms[0].Max
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || !(f >= lo && f <= hi) {
			return nil, fmt.Errorf("line %d: %s: cpus: %q; use a number of CPUs from %g to %g, e.g. 0.5 or 2", cn.Line, ctx, s, lo, hi)
		}
		r.CPUs = f
	}
	if pn := m.get("pids"); pn != nil {
		s, err := scalar(pn, ctx+": pids")
		if err != nil {
			return nil, err
		}
		k, _ := resourcesMap.key("pids")
		i, err := strconv.Atoi(s)
		if err != nil || float64(i) < k.Forms[0].Min {
			return nil, fmt.Errorf("line %d: %s: pids: %q; use a positive integer", pn.Line, ctx, s)
		}
		r.PIDs = i
	}
	if r.Memory == "" && r.CPUs == 0 && r.PIDs == 0 {
		return nil, fmt.Errorf("line %d: %s: is empty; set %s", n.Line, ctx, orList(resourcesMap.names()))
	}
	return r, nil
}
