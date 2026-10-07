package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Healthcheck is a readiness probe: the service counts as started once
// Test passes.
type Healthcheck struct {
	Test        []string
	Interval    string
	Timeout     string
	StartPeriod string
}

// StartTimeout is the TimeoutStartSec= a healthcheck gives its service, in
// seconds: start_period, one test's timeout past it, and 5s to spare.
func (h *Healthcheck) StartTimeout() int {
	sp, _ := Seconds(h.StartPeriod)
	to, _ := Seconds(h.Timeout)
	return sp + to + 5
}

func parseHealthcheck(n *yaml.Node, ctx string) (*Healthcheck, error) {
	h := &Healthcheck{Interval: Default("healthcheck.interval"), Timeout: Default("healthcheck.timeout"), StartPeriod: Default("healthcheck.start_period")}
	m, err := mapping(n, ctx+": healthcheck")
	if err != nil {
		return nil, err
	}
	// Every problem of the block at once: they are independent, and a
	// ported compose healthcheck often has three.
	var problems []string
	known := &mapNode{}
	for _, kv := range m.pairs {
		// compose's keys with no counterpart, answered in its terms
		why := map[string]string{
			"retries":        "the probe runs again every interval until start_period has passed (" + Default("healthcheck.start_period") + " unless set); a shorter start_period fails sooner",
			"start_interval": "the probe runs every interval from the start",
			"disable":        "leave out the healthcheck: key instead",
		}[kv.key.Value]
		if why != "" {
			problems = append(problems, fmt.Sprintf("line %d: %s: healthcheck: %s: %s", kv.key.Line, ctx, kv.key.Value, why))
		} else {
			known.pairs = append(known.pairs, kv)
		}
	}
	if err := unknownKeys(known, ctx+": healthcheck", healthcheckMap.names()...); err != nil {
		problems = append(problems, err.Error())
	}
	if err := healthTest(h, m, n, ctx); err != nil {
		problems = append(problems, err.Error())
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"interval", &h.Interval}, {"timeout", &h.Timeout}, {"start_period", &h.StartPeriod}} {
		if dn := m.get(f.key); dn != nil {
			def := *f.dst
			if *f.dst, err = duration(dn, ctx+": healthcheck: "+f.key); err != nil {
				problems = append(problems, err.Error())
			} else if n, _ := Seconds(*f.dst); n == 0 && f.key != "start_period" {
				// interval: 0 runs the test as fast as it can spawn, and
				// timeout: 0 kills every test at once
				problems = append(problems, fmt.Sprintf("line %d: %s: healthcheck: %s: must be more than 0 (leave it out for the default, %s)", dn.Line, ctx, f.key, def))
			} else if x, _ := spanSeconds(*f.dst); x != float64(n) {
				// the probe counts whole seconds: 200ms would run as 1s, and
				// 1.5s as 2s
				whole := fmt.Sprintf("%ds", n)
				if n > 1 {
					whole = fmt.Sprintf("%ds or %ds", n-1, n)
				}
				problems = append(problems, fmt.Sprintf("line %d: %s: healthcheck: %s: %s; the probe counts in whole seconds, so write %s", dn.Line, ctx, f.key, *f.dst, whole))
			}
		}
	}
	if len(problems) > 0 {
		sort.SliceStable(problems, func(i, j int) bool { return lineOf(problems[i]) < lineOf(problems[j]) })
		return nil, errors.New(strings.Join(problems, "\n  "))
	}
	return h, nil
}

// healthTest reads healthcheck: test:, the probe's argv.
func healthTest(h *Healthcheck, m *mapNode, n *yaml.Node, ctx string) error {
	tn := m.get("test")
	if tn == nil || tn.Kind != yaml.SequenceNode || len(tn.Content) == 0 {
		return fmt.Errorf("line %d: %s: healthcheck: test: is a non-empty list of words (argv)", n.Line, ctx)
	}
	for _, item := range tn.Content {
		s, err := scalar(item, ctx+": healthcheck: test")
		if err != nil {
			return err
		}
		if err := specifiers(s); err != nil {
			return fmt.Errorf("line %d: %s: healthcheck: test: %v", item.Line, ctx, err)
		}
		h.Test = append(h.Test, s)
	}
	// test: is the probe's argv, run without a shell: compose's prefixes
	// name a form, not a program.
	switch h.Test[0] {
	case "CMD":
		return fmt.Errorf("line %d: %s: healthcheck: test: drop compose's \"CMD\": test: is the program and its arguments, e.g. [curl, -sf, http://127.0.0.1:8080/health]", tn.Line, ctx)
	case "CMD-SHELL":
		return fmt.Errorf("line %d: %s: healthcheck: test: compose's \"CMD-SHELL x\" is [sh, -c, x] here", tn.Line, ctx)
	case "NONE":
		return fmt.Errorf("line %d: %s: healthcheck: test: compose's \"NONE\": leave out the healthcheck: key instead", tn.Line, ctx)
	}
	return refuseProgram(h.Test[0], ctx+": healthcheck: test", tn.Line)
}
