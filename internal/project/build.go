package project

import (
	"fmt"
	"os"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/render"
	"github.com/xflash96/systemd-compose/internal/systemd"
)

func (pr *project) build(args []string) error {
	var todo []*config.Service
	if len(args) == 0 {
		for _, s := range pr.p.EnabledServices() {
			if s.Build != nil {
				todo = append(todo, s)
			}
		}
		if len(todo) == 0 {
			return fmt.Errorf("no enabled service in project %s declares build:", pr.p.Name)
		}
	}
	for _, a := range args {
		s, err := pr.p.Lookup(a)
		if err != nil {
			return err
		}
		if s.Build == nil {
			return fmt.Errorf("service %s declares no build:", a)
		}
		todo = append(todo, s)
	}
	// Before the render, which says "service NAME:" for an env file it
	// cannot use: one problem, one wording, the build's.
	for _, s := range todo {
		if err := checkEnvFiles(s.EnvFiles); err != nil {
			return fmt.Errorf("build %s: %w", s.Name, err)
		}
	}
	if err := pr.render(); err != nil {
		return err
	}
	pr.scopeLine()
	for _, s := range todo {
		if err := pr.runBuild(s); err != nil {
			return fmt.Errorf("build %s: %w", s.Name, err)
		}
	}
	// What runs now is still the build before, as up says it would be.
	var units []string
	for _, s := range todo {
		units = append(units, pr.p.UnitOf(s))
	}
	states, err := pr.m.States(units)
	if err != nil {
		return err
	}
	for _, s := range todo {
		if s.Schedule == nil && states[pr.p.UnitOf(s)].Active() { // a job's next run takes the new one
			fmt.Printf("%s is running its previous build; %s restart %s runs the new one\n", s.Name, pr.cmd(), s.Name)
		}
	}
	return nil
}

// runBuild runs a service's build steps through transient units with the
// service's own environment, so a build that passes proves the environment
// the service will run in. Output streams to the terminal. The caller has
// rendered the project.
func (pr *project) runBuild(s *config.Service) error {
	if err := checkEnvFiles(s.EnvFiles); err != nil {
		return err // the caller says build NAME
	}
	unitDir, err := pr.m.UnitDir()
	if err != nil {
		return err
	}
	slice, err := pr.sliceProp(unitDir)
	if err != nil {
		return err
	}
	for i, step := range s.Build.Run {
		// systemd-run takes the program's path as found: a space or a % in
		// the directory is no unit file's to quote or escape
		_, prog, rest, err := render.ProgramOf(step, s.WorkingDir, pr.opt.SearchPath, "")
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		fmt.Printf("build %s [%d/%d]: %s\n", s.Name, i+1, len(s.Build.Run), prog+rest)
		args := []string{"--wait", "--pipe", "--collect", "--quiet",
			"--description=" + pr.p.Name + ": build " + s.Name,
			"-p", "WorkingDirectory=" + s.WorkingDir}
		args = append(args, slice...)
		args = append(args, limitProps(s.Resources, pr.p.Resources, len(slice) > 0)...)
		eargs, done, err := envProps(literalEnv(s.Environment), s.EnvFiles)
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		args = append(args, eargs...)
		record, rargs, err := endRecord(i + 1)
		if err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		args = append(args, rargs...)
		args = append(args, "--")
		args = append(args, prog) // the program's path as it is: $$ is collapsed in arguments alone
		for _, w := range systemd.SplitWords(rest) {
			args = append(args, render.ExecLiteral(w))
		}
		pr.atExit = done
		err = pr.oneOff("build", i+1, args)
		done()
		end := readEnd(record)
		os.Remove(record)
		switch ending(err, record, end) {
		case endUnknown:
			// else the build would be blamed for the creates: it did not make
			return fmt.Errorf("step %d: the user manager went away while it ran (restarted, or stopped), so whether it finished is unknown; build again", i+1)
		case endInterrupted:
			return fmt.Errorf("step %d was interrupted, so the build did not finish", i+1)
		case endOOM:
			return fmt.Errorf("step %d ran out of memory under its cap (resources: memory) and was killed", i+1)
		case endSignal:
			return fmt.Errorf("step %d was %s", i+1, end.how())
		case endStopped:
			return fmt.Errorf("step %d did not finish: it was stopped or killed (down, or systemctl stop)", i+1)
		}
		if err != nil {
			return fmt.Errorf("step %d failed: %v", i+1, err)
		}
	}
	// A build stopped from elsewhere can end looking finished; what it
	// was to make says whether it did.
	if c := s.Build.Creates; c != "" && !exists(c) {
		return fmt.Errorf("its steps ran, but %s, which creates: names, is not there: the build did not make it", c)
	}
	return nil
}
