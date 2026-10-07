package config

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// composeKeys are compose's keys this tool does not take, with what to do
// instead: a ported compose file meets them first, and they are not typos.
var composeKeys = map[string]string{
	"image":          "there are no images: a service is a program on this host; give it command: (and build: for the steps that make it)",
	"pull_policy":    "there are no images to pull",
	"ports":          "no ports to publish: a host process binds what it binds (listen: is socket activation, not a port mapping)",
	"expose":         "nothing to expose: a host process binds what it binds",
	"volumes":        "no volumes: a service uses this host's filesystem; set working_dir: and write absolute paths",
	"networks":       "no networks: services reach each other on 127.0.0.1",
	"container_name": "the unit is named <project>-<service>",
	"hostname":       "a service runs on this host, under its name",
	"deploy":         "resources: sets memory, cpus and pids",
	"logging":        "the journal keeps the logs: systemd-compose logs",
	"tty":            "a service runs without a terminal; run SERVICE gets one",
	"stdin_open":     "a service runs without a terminal; run SERVICE gets one",
	"labels":         "nothing to label; an x- key holds your own data",
	"configs":        "no configs: a service reads this host's files",
	"secrets":        "no secrets: a service reads this host's files (env_file: for variables)",

	"memswap_limit": "resources: {memory: ...} caps the service; swap is systemd's MemorySwapMax= under unit: Service:",
	"user":          "a service runs as you, on your user instance",
	"platform":      "a service is a program on this host",
}

func init() {
	// What a container sets up, a host process does not have: one answer.
	for _, k := range strings.Fields("init privileged cap_add cap_drop devices sysctls dns dns_search extra_hosts network_mode security_opt read_only tmpfs shm_size ipc pid userns_mode group_add domainname mac_address cgroup_parent links external_links runtime isolation") {
		composeKeys[k] = "no container to set up: a service is a program on this host (systemd's own sandboxing goes under unit: Service:)"
	}
}

// composeAnswer is what to write instead of compose's service key k, with
// the file's own value where it carries over; "" when k is not compose's.
func composeAnswer(k string, v *yaml.Node) string {
	if f := carried[k]; f != nil {
		return f(v)
	}
	return composeKeys[k]
}

// carried words the answer for a compose key whose value carries over, with
// the file's own value in it: advice pasted over a 256m limit must not
// double it.
var carried = map[string]func(*yaml.Node) string{
	"mem_limit": func(n *yaml.Node) string {
		v := strings.ToUpper(strings.TrimSuffix(strings.ToLower(n.Value), "b"))
		if n.Kind != yaml.ScalarNode || !reMemory.MatchString(v) {
			v = "<your value>"
		}
		return "resources: {memory: " + v + "} (cpus: and pids: beside it)"
	},
	"cpus":              func(n *yaml.Node) string { return "resources: {cpus: " + valueOr(n) + "}" },
	"pids_limit":        func(n *yaml.Node) string { return "resources: {pids: " + valueOr(n) + "}" },
	"stop_grace_period": func(n *yaml.Node) string { return "unit: {Service: {TimeoutStopSec: " + valueOr(n) + "}}" },
	"stop_signal":       func(n *yaml.Node) string { return "unit: {Service: {KillSignal: " + valueOr(n) + "}}" },
	"ulimits":           ulimits,
}

// ulimits spells compose's ulimits: as systemd's Limit...= keys, the
// file's own values, soft:hard where both are given.
func ulimits(n *yaml.Node) string {
	generic := "unit: {Service: {LimitNOFILE: ...}}, and systemd's other Limit...= keys"
	m, err := mapping(n, "ulimits")
	if err != nil {
		return generic
	}
	var keys, unknown []string
	for _, kv := range m.pairs {
		name := strings.ToUpper(kv.key.Value)
		if !slices.Contains(strings.Fields("AS CORE CPU DATA FSIZE LOCKS MEMLOCK MSGQUEUE NICE NOFILE NPROC RSS RTPRIO RTTIME SIGPENDING STACK"), name) {
			unknown = append(unknown, kv.key.Value)
			continue
		}
		v := kv.value.Value
		if kv.value.Kind == yaml.MappingNode {
			lim, err := mapping(kv.value, "ulimits")
			if err != nil || lim.get("soft") == nil || lim.get("hard") == nil {
				return generic
			}
			v = unlimited(lim.get("soft").Value) + ":" + unlimited(lim.get("hard").Value)
		} else {
			v = unlimited(v)
		}
		keys = append(keys, "Limit"+name+": "+v)
	}
	out := "unit: {Service: {" + strings.Join(keys, ", ") + "}}"
	if len(unknown) > 0 {
		out += " (" + strings.Join(unknown, ", ") + ": no systemd counterpart)"
	}
	return out
}

// unlimited is compose's -1 (no limit) in systemd's word.
func unlimited(v string) string {
	if v == "-1" {
		return "infinity"
	}
	return v
}

func valueOr(n *yaml.Node) string {
	if n.Kind != yaml.ScalarNode || n.Value == "" {
		return "<your value>"
	}
	return n.Value
}
