package project

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

// runOneOff runs a command in a service's environment through a transient
// unit, as build steps run: its working directory, environment, env files
// and, while the project is registered, its slice (sliceProp); with no
// command, run runs the service's own. A service this project registered
// lends what systemd runs it with, read over D-Bus (specifiers expanded);
// one never brought up lends the yaml's, which then may hold no specifier,
// since systemd-run -p passes values literally. No dependency is started
// (compose's --no-deps); the exit code is the command's.
func (pr *project) runOneOff(verb string, args []string) error {
	var extra []string // -e KEY=VAL
	workdir, pipe := "", false
	i := 0
flags:
	for ; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
				return v, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s: %s needs a value", verb, a)
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "-e" || a == "--env" || strings.HasPrefix(a, "--env="):
			v, err := value()
			if err != nil {
				return err
			}
			if k, _, ok := strings.Cut(v, "="); !ok || !config.IsVarName(k) {
				return fmt.Errorf("%s: -e %q: KEY=value", verb, v)
			}
			extra = append(extra, v)
		case a == "-w" || a == "--workdir" || strings.HasPrefix(a, "--workdir="):
			v, err := value()
			if err != nil {
				return err
			}
			workdir = v
		case a == "-T" || a == "--no-TTY":
			pipe = true
		case a == "--rm" || a == "--no-deps" || a == "-i" || a == "--interactive":
			// compose's flags for what happens here anyway
		case a == "--":
			i++
			break flags
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("%s: unknown flag %q (known: -e KEY=VAL, -w DIR, -T)", verb, a)
		default:
			break flags
		}
	}
	if i >= len(args) {
		return fmt.Errorf("%s needs a service (services: %s)", verb, strings.Join(pr.p.ServiceNames(), ", "))
	}
	s, err := pr.p.Lookup(args[i])
	if err != nil {
		return err
	}
	cmd := args[i+1:]
	if len(cmd) > 0 && cmd[0] == "--" {
		cmd = cmd[1:] // systemd-run's habit; the service name already ended the flags
	}
	if verb == "exec" && len(cmd) == 0 {
		return fmt.Errorf("exec needs a command (run SERVICE with none runs the service's own)")
	}
	// systemd-run fails on it with exit 1 and not a word, which would read
	// as the command's own failure
	if err := checkEnvFiles(s.EnvFiles); err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	if s.Schedule != nil && len(cmd) == 0 {
		// the timer's runs never overlap, but this one is no run of the timer's
		if st, err := pr.m.States([]string{pr.p.ServiceUnit(s)}); err == nil && st[pr.p.ServiceUnit(s)].ActiveState == "activating" {
			fmt.Fprintf(os.Stderr, "note: %s is running its job now, on its timer; this run is a second copy beside it\n", s.Name)
		}
	}
	if workdir == "" {
		workdir = s.WorkingDir
	} else if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(s.WorkingDir, workdir)
	}
	// Checked here, because systemd answers a missing one with exit 200 and,
	// under --quiet, not a word about why.
	st, err := os.Stat(workdir)
	if err != nil {
		return fmt.Errorf("%s: working directory: %v", verb, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s: working directory %s: not a directory", verb, workdir)
	}

	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	opt, err := render.DefaultRenderOptions() // the search path alone; run renders nothing
	if err != nil {
		return err
	}
	unit := pr.p.ServiceUnit(s)
	var env, argv []string
	switch r := pr.registrationOf(unitDir, unit); r.kind {
	case "ours":
		if env, argv, err = pr.m.ServiceRun(unit); err != nil {
			return err
		}
	case "none":
		for _, kv := range s.Environment {
			if config.UsesSpecifier(kv.Value) {
				return fmt.Errorf("%s: environment: %s uses a specifier, which only systemd expands; up registers %s, and %s then reads its environment from systemd", verb, kv.Key, s.Name, verb)
			}
		}
		env = literalEnv(s.Environment)
		if len(cmd) == 0 {
			if s.Command.Empty() && s.Entrypoint.Empty() {
				return fmt.Errorf("run: %s has only a raw ExecStart=; give the command (or up it first)", s.Name)
			}
			line, err := render.ExecLine(s.Entrypoint, s.Command, s.WorkingDir, opt.SearchPath, "")
			if err != nil {
				return err
			}
			if config.UsesSpecifier(line) {
				return fmt.Errorf("run: %s's command uses a specifier, which only systemd expands; up it first, or give the command", s.Name)
			}
			argv = systemd.SplitWords(strings.ReplaceAll(line, "%%", "%")) // already written $$ for systemd
		}
	default:
		return fmt.Errorf("%s: %s is %s; this project does not own it", verb, unit, r.owner)
	}
	if len(cmd) > 0 {
		prog, err := render.ResolveWord(cmd[0], workdir, opt.SearchPath)
		if err != nil {
			return fmt.Errorf("%s: %w", verb, err)
		}
		if strings.Contains(cmd[0], "%") { // the word's own: one from the directory is no specifier
			return fmt.Errorf("%s: %q: systemd-run does not expand specifiers", verb, cmd[0])
		}
		argv = []string{prog}
		for _, w := range cmd[1:] {
			argv = append(argv, render.ExecLiteral(w)) // the manager expands $VAR in a transient ExecStart
		}
	}
	if len(argv) == 0 {
		return fmt.Errorf("run: %s has no command to run", s.Name)
	}

	runArgs := []string{"--wait", "--collect", "--quiet",
		"--description=" + pr.p.Name + ": " + verb + " " + s.Name,
		"-p", "WorkingDirectory=" + workdir}
	slice, err := pr.sliceProp(unitDir)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	runArgs = append(runArgs, slice...)
	runArgs = append(runArgs, limitProps(s.Resources, pr.p.Resources, len(slice) > 0)...)
	if pipe || !terminal(os.Stdin) || !terminal(os.Stdout) {
		runArgs = append(runArgs, "--pipe")
	} else {
		runArgs = append(runArgs, "--pty")
	}
	eargs, done, err := envProps(env, s.EnvFiles)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	defer done()
	pr.atExit = done
	runArgs = append(runArgs, eargs...)
	record, rargs, err := endRecord(0)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	defer os.Remove(record)
	runArgs = append(runArgs, rargs...)
	runArgs = append(runArgs, "--")
	if len(extra) > 0 {
		// EnvironmentFile= overrides Environment= whatever the order they
		// are given in, so -e goes through env(1), which runs after
		// systemd has assembled the unit's environment: compose's
		// precedence, where -e wins over the service's env_file.
		prog, err := render.ResolveWord("env", workdir, opt.SearchPath)
		if err != nil {
			return fmt.Errorf("%s: -e needs env: %w", verb, err)
		}
		runArgs = append(runArgs, prog, "--")
		for _, e := range extra {
			runArgs = append(runArgs, render.ExecLiteral(e))
		}
	}
	runArgs = append(runArgs, argv...)
	err = pr.oneOff("run", 0, runArgs)
	end := readEnd(record)
	if err != nil && end.signal == "INT" {
		err = errInterrupted // a ^C at its terminal, which reached the command, not this client
	}
	switch ending(err, record, end) {
	case endUnknown:
		fmt.Fprintf(os.Stderr, "systemd-compose: %s: the user manager went away while the command ran (restarted, or stopped), so whether it finished is unknown\n", verb)
		return systemd.ExitError{Code: 1}
	case endInterrupted:
		fmt.Fprintf(os.Stderr, "systemd-compose: %s: interrupted; the command was stopped\n", verb)
		return systemd.ExitError{Code: 130}
	case endOOM:
		fmt.Fprintf(os.Stderr, "systemd-compose: %s: the command ran out of memory under its cap (resources: memory) and was killed\n", verb)
		return systemd.ExitError{Code: 137}
	case endSignal:
		fmt.Fprintf(os.Stderr, "systemd-compose: %s: the command was %s\n", verb, end.how())
		return systemd.ExitError{Code: 128 + end.number()}
	case endStopped:
		fmt.Fprintf(os.Stderr, "systemd-compose: %s: exit 255: the command was stopped or killed before it finished (down, systemctl stop), or exited 255 itself\n", verb)
	}
	return err
}

// The files a run keeps in scratchDir while it works, each named
// <kind>-<pid>-... (scratchPrefix).
const (
	verifyScratch = "verify" // up's staging copy of the render
	envScratch    = "env"    // a one-off's environment
	endScratch    = "end"    // a one-off's end record
)

// scratchPrefix starts the name of this process's scratch file of a kind.
func scratchPrefix(kind string) string { return fmt.Sprintf("%s-%d-", kind, os.Getpid()) }

// scratchPath is where scratchDir keeps the files, and the projects' locks.
func scratchPath() string { return systemd.RuntimeDir() }

// scratchDir is this user's private directory for the files a run keeps
// while it works. A run killed before it cleans up leaves them behind;
// those of a pid that is gone are swept on the way in.
func scratchDir() (string, error) {
	dir := scratchPath()
	if err := systemd.PrivateDir(dir); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		kind, rest, ok := strings.Cut(e.Name(), "-")
		if !ok || !slices.Contains([]string{verifyScratch, envScratch, endScratch}, kind) {
			continue
		}
		pid, _, _ := strings.Cut(rest, "-")
		if n, err := strconv.Atoi(pid); err == nil && n > 0 && syscall.Kill(n, 0) == syscall.ESRCH {
			os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return dir, nil
}

// endRecord is the systemd-run arguments that have a one-off's unit record
// how its command ended, and the file it writes. systemd-run cannot tell:
// it exits 255 for a ^C at the command's terminal as for a stop from
// elsewhere, and 1 for a command killed out of memory or by SIGSEGV, as if
// it had exited 1. The file is this user's alone, beside the env file; no
// record when the path cannot be written into a unit line.
func endRecord(step int) (string, []string, error) {
	dir, err := scratchDir()
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, scratchPrefix(endScratch)+strconv.Itoa(step))
	if strings.ContainsAny(path, "'\\%$") {
		return "", nil, nil
	}
	return path, []string{"-p", `ExecStopPost=/bin/sh -c 'echo "$$SERVICE_RESULT $$EXIT_CODE $$EXIT_STATUS" >` + path + `'`}, nil
}

// oneOffEnd is how a one-off's command ended, as its unit recorded it.
type oneOffEnd struct {
	recorded bool   // the unit wrote its record: it ended under a manager that saw it end
	oom      bool   // SERVICE_RESULT oom-kill: its memory cap
	signal   string // the signal that killed it (INT, KILL, SEGV...), if one did
	dumped   bool   // and it dumped core
}

func readEnd(path string) oneOffEnd {
	b, _ := os.ReadFile(path)
	f := strings.Fields(string(b))
	if len(f) != 3 {
		return oneOffEnd{}
	}
	e := oneOffEnd{recorded: true, oom: f[0] == "oom-kill", dumped: f[1] == "dumped"}
	if f[1] == "killed" || f[1] == "dumped" {
		e.signal = f[2]
	}
	return e
}

// How a one-off ended, beyond its exit status, as run and build tell it;
// the first that holds wins.
const (
	endPlain       = iota // nothing more than err says
	endUnknown            // systemd-run exited 0, but the manager went away mid-run
	endInterrupted        // a ^C stopped it
	endOOM                // its memory cap killed it
	endSignal             // a signal other than TERM killed it
	endStopped            // exit 255: stopped or killed from elsewhere, or its own 255
)

// ending is how a one-off ended: err is systemd-run's, record the path of
// its end record ("" for none), end what the record says.
func ending(err error, record string, end oneOffEnd) int {
	switch {
	case err == nil && record != "" && !end.recorded:
		return endUnknown
	case errors.Is(err, errInterrupted):
		return endInterrupted
	case err != nil && end.oom:
		return endOOM
	case err != nil && end.signal != "" && end.signal != "TERM":
		return endSignal
	case err == (systemd.ExitError{Code: 255}):
		return endStopped
	}
	return endPlain
}

func (e oneOffEnd) how() string {
	if e.dumped {
		return "killed by SIG" + e.signal + " (core dumped)"
	}
	return "killed by SIG" + e.signal
}

// number is the signal's number, HUP to TERM, for the 128+N a shell
// reports; 127 for any other (255 is the stop's).
func (e oneOffEnd) number() int {
	if n := slices.Index(signalNames, e.signal) + 1; n >= 1 && n <= 15 {
		return n
	}
	return 127
}

// flushInput discards what the terminal holds unread. systemd-run --pty
// that starts with an end of input already waiting (ssh -t host CMD
// </dev/null, a pty whose input has ended) stalls once the command has
// ended, until a key. It does so most times when it starts half a second
// late, and never when it starts at once; this program renders first, so
// it is always late. Keys typed in that half second are lost with it.
func flushInput(f *os.File) {
	const tcflsh, tciflush = 0x540B, 0 // TCFLSH, TCIFLUSH (Linux; syscall lacks them on amd64)
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), tcflsh, tciflush)
}

// interrupted is a one-off that this client stopped on a signal: a ^C, a
// closed terminal, a kill. It exits 130, as a shell does for ^C.
var errInterrupted = errors.New("interrupted")

// oneOff runs systemd-run for a one-off unit of this project, named
// <kind>-<project>-<pid>[-<step>] so ps shows it and down stops it. It is
// Type=oneshot: a simple unit stopped from elsewhere (down, systemctl stop)
// ends "success" and systemd-run --wait exits 0 as if the command had
// finished, while a oneshot's stop is a failure, exit 255, and a command's
// own exit status still comes through. When this client is interrupted
// the unit is stopped with it, as systemd-run --wait leaves it running,
// and the error is errInterrupted; a second signal ends the client at
// once.
func (pr *project) oneOff(kind string, step int, args []string) error {
	unit := pr.oneOffPrefix(kind) + strconv.Itoa(os.Getpid())
	if step > 0 {
		unit += fmt.Sprintf("-%d", step)
	}
	// The stop is queued before this returns: a handler that set "stopped"
	// and only then ran systemctl could lose to the exit it allowed, and a
	// ^C'd build would run on to completion after "the build did not
	// finish".
	stop := func() { pr.m.Cmd("systemctl", "stop", "--no-block", unit+".service").Run() }
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	c := pr.m.Cmd("systemd-run", append([]string{"--unit=" + unit, "-p", "Type=oneshot"}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if slices.Contains(args, "--pty") {
		flushInput(os.Stdin)
	}
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	var err error
	select {
	case err = <-done:
		// A terminal's ^C reaches systemd-run too, and select takes either
		// ready case, while the signal's delivery to sig can lag the
		// child's exit: a systemd-run killed by a signal is that ^C (or a
		// kill), and its unit outlives it, so it is stopped all the same.
		// Otherwise a ^C'd build would run on, reported as "exit -1".
		if killedBySignal(err) {
			stop()
			return errInterrupted
		}
		select {
		case <-sig: // the signal raced the child's exit
			stop()
			return errInterrupted
		default:
		}
	case <-sig:
		stop()
		go func() {
			<-sig
			if pr.atExit != nil {
				pr.atExit() // the private env file, which would be left behind
			}
			os.Exit(130) // asked twice: the unit's stop is already queued
		}()
		<-done // systemd-run ends with its unit, or on the signal itself
		return errInterrupted
	}
	return oneOffOutcome(err, pr.m.Reachable)
}

// killedBySignal says a child ended on a signal rather than exiting.
func killedBySignal(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}

// oneOffOutcome is a one-off's result: systemd-run exits 0 when the
// manager goes away under it (restarted, exited, killed), so a command
// cut short would read as one that succeeded; a manager no longer there
// says the run is unknown.
func oneOffOutcome(err error, reachable func() error) error {
	if err == nil && reachable() != nil {
		return fmt.Errorf("the user manager went away while the command ran, so whether it finished is unknown")
	}
	return systemd.AsExit(err)
}

// oneOffPrefix starts the name of this project's one-off units of a kind
// (run, build): <kind>-<project>-<pid>, the project spelt as in its units.
func (pr *project) oneOffPrefix(kind string) string {
	return kind + "-" + config.EscapeName(pr.p.Name) + "-"
}

// oneOffs are this project's run and build units still loaded: a one-off
// whose client went away while it ran.
func (pr *project) oneOffs() ([]string, error) {
	// systemd matches these with FNM_NOESCAPE: the \ of an escaped dash
	// is a plain character
	out, err := pr.m.Cmd("systemctl", "list-units", "--all", "--plain", "--no-legend", "--full", pr.oneOffPrefix("run")+"*", pr.oneOffPrefix("build")+"*").Output()
	if err != nil {
		return nil, systemd.CmdErr("systemctl list-units", err)
	}
	var units []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			units = append(units, f[0])
		}
	}
	if len(units) == 0 {
		return nil, nil
	}
	// A one-off is a transient unit: a project named run or build renders
	// units that match run-<project>-*, which another project's down
	// would otherwise stop as its own.
	st, err := pr.m.States(units)
	if err != nil {
		return nil, err
	}
	// One that is loaded but done is no one-off running: ps would call it
	// still running, and down would say it stopped it.
	return slices.DeleteFunc(units, func(u string) bool {
		return !strings.Contains(st[u].FragmentPath, "/systemd/transient/") || !st[u].Active()
	}), nil
}

// sliceProp puts a build or run unit in the project's slice once the slice
// is registered, so the project's limits apply to it (up registers it ahead
// of its builds). Before that, the slice would be one systemd makes on
// demand, which nothing ever stops: it stays active and empty after the
// run, and it carries no limits anyway. A slice another owner
// holds is refused, as every verb refuses a unit that is not ours.
func (pr *project) sliceProp(unitDir string) ([]string, error) {
	switch r := pr.registrationOf(unitDir, pr.p.SliceName()); r.kind {
	case "ours":
		return []string{"-p", "Slice=" + pr.p.SliceName()}, nil
	case "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("%s is %s; this project does not own it", pr.p.SliceName(), r.owner)
	}
}

// limitProps spells the limits of a one-off (run, exec, a build step) as
// systemd-run -p arguments: the service's own resources:, as its unit has
// them. In the project's slice the project's resources: apply from the
// slice; outside it (before the first up, after a down) they are the
// one-off's own too, the tighter of the two where both set one. Without
// them, a one-off of a service capped at 32M would run uncapped.
func limitProps(svc, project *config.Resources, inSlice bool) []string {
	r := config.Resources{}
	if svc != nil {
		r = *svc
	}
	if project != nil && !inSlice {
		if project.Memory != "" && (r.Memory == "" || memBytes(project.Memory) < memBytes(r.Memory)) {
			r.Memory = project.Memory
		}
		if project.CPUs > 0 && (r.CPUs == 0 || project.CPUs < r.CPUs) {
			r.CPUs = project.CPUs
		}
		if project.PIDs > 0 && (r.PIDs == 0 || project.PIDs < r.PIDs) {
			r.PIDs = project.PIDs
		}
	}
	var out []string
	for _, l := range render.Limits(&r) {
		out = append(out, "-p", l.Directive+"="+l.Value)
	}
	return out
}

// memBytes is a resources: memory value (digits and an optional K, M, G or
// T, systemd's powers of 1024) in bytes.
func memBytes(s string) uint64 {
	mult := uint64(1)
	if i := strings.IndexAny(s, "KMGT"); i >= 0 {
		mult = 1 << (10 * (1 + strings.IndexByte("KMGT", s[i])))
		s = s[:i]
	}
	n, _ := strconv.ParseUint(s, 10, 64)
	return n * mult
}

// literalEnv is a service's environment: as KEY=value lines, with %% as
// the % it stands for: what systemd-run takes is literal, where a unit's
// Environment= expands specifiers.
func literalEnv(kvs []config.KV) []string {
	env := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		env = append(env, kv.Key+"="+strings.ReplaceAll(kv.Value, "%%", "%"))
	}
	return env
}

// envProps spells a service's environment and env files as systemd-run -p
// arguments, for the two verbs that run a command in it (build and run).
// The environment goes through a file only this user can read: on
// systemd-run's command line, as -p Environment=, every value would be
// there for any user's ps. It comes first, so the env files win, as
// EnvironmentFile= beats Environment= in the unit. env holds KEY=value
// lines. done removes the file.
func envProps(env []string, files []config.EnvFile) (out []string, done func(), err error) {
	done = func() {}
	if len(env) > 0 {
		dir, err := scratchDir()
		if err != nil {
			return nil, done, err
		}
		f, err := os.CreateTemp(dir, scratchPrefix(envScratch))
		if err != nil {
			return nil, done, err
		}
		done = func() { os.Remove(f.Name()) }
		var b strings.Builder
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			fmt.Fprintf(&b, "%s=\"%s\"\n", k, envFileQuote(v))
		}
		_, werr := f.WriteString(b.String())
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			done()
			return nil, func() {}, werr
		}
		out = append(out, "-p", "EnvironmentFile="+f.Name())
	}
	for _, ef := range files {
		p := ef.Path
		if !ef.Required {
			p = "-" + p
		}
		out = append(out, "-p", "EnvironmentFile="+p)
	}
	return out, done, nil
}

// envFileQuote escapes a value for a double-quoted line of an environment
// file, as systemd reads one: a backslash keeps the next of \ " ` $.
func envFileQuote(v string) string {
	var b strings.Builder
	for _, r := range v {
		if strings.ContainsRune("\\\"`$", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// runtimeDir is this user's runtime directory.
func terminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
