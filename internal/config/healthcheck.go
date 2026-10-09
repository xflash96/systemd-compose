package config

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Healthcheck is a test the service must pass to count as started; then
// it runs every Interval while the service does, and Retries failed in a
// row make the service unhealthy, as in compose.
type Healthcheck struct {
	Test          []string
	Interval      string // between checks once started
	Timeout       string
	Retries       int
	StartPeriod   string // how long the start waits for a pass
	StartInterval string // between checks while starting
}

// StartTimeout is the TimeoutStartSec= a healthcheck gives its service, in
// seconds: start_period, one test's timeout past it, and 5s to spare.
func (h *Healthcheck) StartTimeout() int {
	sp, _ := Seconds(h.StartPeriod)
	to, _ := Seconds(h.Timeout)
	return sp + to + 5
}

func parseHealthcheck(n *yaml.Node, ctx string) (*Healthcheck, error) {
	retries, _ := strconv.Atoi(Default("healthcheck.retries"))
	h := &Healthcheck{Interval: Default("healthcheck.interval"), Timeout: Default("healthcheck.timeout"), Retries: retries,
		StartPeriod: Default("healthcheck.start_period"), StartInterval: Default("healthcheck.start_interval")}
	m, err := mapping(n, ctx+": healthcheck")
	if err != nil {
		return nil, err
	}
	// Every problem of the block at once: they are independent, and a
	// ported compose healthcheck often has three.
	var problems []string
	for _, f := range composeHealthcheck(m, ctx) {
		problems = append(problems, f.text)
	}
	known := &mapNode{}
	for _, kv := range m.pairs {
		if kv.key.Value != "disable" { // answered above
			known.pairs = append(known.pairs, kv)
		}
	}
	if err := unknownKeys(known, ctx+": healthcheck", healthcheckMap.names()...); err != nil {
		problems = append(problems, err.Error())
	}
	if tn := m.get("test"); tn == nil || composeTest(tn) == "" {
		if err := healthTest(h, m, n, ctx); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if rn := m.get("retries"); rn != nil {
		k, _ := healthcheckMap.key("retries")
		if s, err := scalar(rn, ctx+": healthcheck: retries"); err != nil {
			problems = append(problems, err.Error())
		} else if h.Retries, err = strconv.Atoi(s); err != nil || float64(h.Retries) < k.Forms[0].Min {
			problems = append(problems, fmt.Sprintf("line %d: %s: healthcheck: retries: %q; use a positive integer", rn.Line, ctx, s))
		}
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"interval", &h.Interval}, {"timeout", &h.Timeout}, {"start_period", &h.StartPeriod}, {"start_interval", &h.StartInterval}} {
		if dn := m.get(f.key); dn != nil {
			def := *f.dst
			if *f.dst, err = duration(dn, ctx+": healthcheck: "+f.key); err != nil {
				problems = append(problems, err.Error())
			} else if n, _ := Seconds(*f.dst); n == 0 && f.key != "start_period" {
				// an interval of 0 runs the test as fast as it can spawn,
				// and timeout: 0 kills every test at once
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
	return refuseProgram(h.Test[0], ctx+": healthcheck: test", tn.Line)
}
