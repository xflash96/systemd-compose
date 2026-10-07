package config

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// UnknownProfile is a --profile that names no profile of the yaml: a typo
// on the command line, not a yaml that fails to load. No verb falls back to
// what is registered for it, as ps does for a yaml that does not load.
type UnknownProfile struct{ msg string }

func (e UnknownProfile) Error() string { return e.msg }

// applyProfiles settles the active profiles and what follows from them:
// a required edge to a disabled service is refused, naming the profile to
// activate, and an optional one is dropped (compose does both), so a
// disabled service is never pulled in by one that is enabled.
func applyProfiles(p *Project, flags []string, dotenv map[string]string) error {
	switch {
	case len(flags) > 0:
		p.Profiles, p.ProfilesFrom = flags, "--profile"
	case os.Getenv(ProfilesVar) != "":
		p.Profiles, p.ProfilesFrom = splitList(os.Getenv(ProfilesVar)), "environment"
	case dotenv[ProfilesVar] != "":
		p.Profiles, p.ProfilesFrom = splitList(dotenv[ProfilesVar]), ".env"
	}
	known := map[string]bool{}
	for _, s := range p.Services {
		for _, n := range s.Profiles {
			known[n] = true
		}
	}
	if p.ProfilesFrom == "environment" || p.ProfilesFrom == ".env" {
		// set once for every project (a shell's rc, a wrapper), as compose's
		// COMPOSE_PROFILES is: a name this file does not declare is not a
		// typo here, and refusing it would break every other project
		p.Profiles = slices.DeleteFunc(p.Profiles, func(n string) bool { return n != "*" && !known[n] })
	}
	for _, n := range p.Profiles {
		if n != "*" && !known[n] {
			var names []string
			for k := range known {
				names = append(names, k)
			}
			sort.Strings(names)
			where := "this file declares no profiles"
			if len(names) > 0 {
				where = "profiles in this file: " + strings.Join(names, ", ")
			}
			return UnknownProfile{fmt.Sprintf("profile %q (from %s) is no service's; %s", n, p.ProfilesFrom, where)}
		}
	}
	for _, s := range p.EnabledServices() {
		var kept []Dependency
		for _, d := range s.DependsOn {
			t := p.Service(d.Service)
			switch {
			case p.Enabled(t):
				kept = append(kept, d)
			case d.Required:
				// refused by the verbs that start or render (up, start,
				// run...); ps, logs, stop and down still load the project
				if p.NeedsProfile == "" {
					p.NeedsProfile = fmt.Sprintf("service %s: depends_on: %s, which is in profile %s, not active; add --profile %s", s.Name, t.Name, strings.Join(t.Profiles, " or "), t.Profiles[0])
				}
			}
		}
		s.DependsOn = kept
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Enabled says whether a service is in this run: it names no profile, or
// one that is active.
func (p *Project) Enabled(s *Service) bool {
	if len(s.Profiles) == 0 {
		return true
	}
	for _, a := range p.Profiles {
		if a == "*" {
			return true
		}
		for _, n := range s.Profiles {
			if n == a {
				return true
			}
		}
	}
	return false
}

// EnabledServices are the services up renders and starts, in file order.
func (p *Project) EnabledServices() []*Service {
	var out []*Service
	for _, s := range p.Services {
		if p.Enabled(s) {
			out = append(out, s)
		}
	}
	return out
}
