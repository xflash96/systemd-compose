package config

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// sharedListen refuses two services listening on one address: the second
// socket would fail at start with "Address already in use". A bare port
// listens on every address, so it meets host:port with the same port. An
// address with a specifier is left to systemd.
func sharedListen(p *Project) error {
	exact := map[string]string{}  // address -> service
	bare := map[string]string{}   // a bare port -> service
	onPort := map[string]string{} // the port of any numeric listen -> a service
	for _, svc := range p.EnabledServices() {
		for _, a := range svc.Listen {
			if UsesSpecifier(a) {
				continue // systemd's to expand, not comparable here
			}
			other := exact[a]
			port, isBare := a, true
			if i := strings.LastIndexByte(a, ':'); i >= 0 {
				port, isBare = a[i+1:], false
			}
			if _, err := strconv.Atoi(port); err == nil {
				switch {
				case other != "":
				case isBare && onPort[port] != "":
					other = onPort[port]
				case !isBare && bare[port] != "":
					other = bare[port]
				}
				onPort[port] = svc.Name
				if isBare {
					bare[port] = svc.Name
				}
			}
			if other == svc.Name {
				return fmt.Errorf("service %s: listen: %s: this service already listens there, and two sockets cannot share an address", svc.Name, a)
			}
			if other != "" {
				return fmt.Errorf("service %s: listen: %s: service %s listens there too, and two sockets cannot share an address", svc.Name, a, other)
			}
			exact[a] = svc.Name
		}
	}
	return nil
}

// listenAddress says what is wrong with a listen: address systemd would
// refuse at the verify gate, in a rendered file the user never wrote: a
// socket file without a slash (systemd takes the word for a port), a host
// name (systemd resolves none), a port out of range. "" when it is fine or
// is systemd's to judge.
func listenAddress(a string) string {
	if strings.ContainsAny(a[:1], "@%[") || strings.HasPrefix(a, "vsock:") || strings.Contains(a, "/") {
		return ""
	}
	if n, err := strconv.Atoi(a); err == nil {
		if n < 1 || n > 65535 {
			return "a port is 1 to 65535"
		}
		return ""
	}
	host, port, ok := strings.Cut(a, ":")
	if !ok {
		if net.ParseIP(a) != nil {
			return "an IP address needs a port: " + a + ":8080, say" // systemd would take it for a socket file
		}
		return "a socket file needs a slash (./api.sock, run/api.sock), and an address a port (127.0.0.1:8080); systemd takes a bare word for a port"
	}
	if strings.Count(a, ":") > 1 {
		return "" // an IPv6 address without brackets: systemd judges it
	}
	if net.ParseIP(host) == nil {
		return "systemd resolves no host name in a listen address: write the IP (127.0.0.1:" + port + ")"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "a port is 1 to 65535"
	}
	return ""
}

// parseListen reads listen:, one address or a list, each a ListenStream=
// line of the service's socket: a port, host:port, [v6]:port, @abstract or
// a path. A relative path is relative to the yaml; systemd-analyze verify
// judges the rest, so there is no second parser of addresses here.
func parseListen(n *yaml.Node, ctx, dir string) ([]string, error) {
	var items []*yaml.Node
	switch n.Kind {
	case yaml.ScalarNode:
		items = []*yaml.Node{n}
	case yaml.SequenceNode:
		items = n.Content
	default:
		return nil, fmt.Errorf("line %d: %s: listen: is an address or a list of addresses", n.Line, ctx)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("line %d: %s: listen: is empty", n.Line, ctx)
	}
	var out []string
	for _, item := range items {
		a, err := scalar(item, ctx+": listen")
		if err != nil {
			return nil, err
		}
		a = strings.TrimSpace(a)
		if a == "" {
			return nil, fmt.Errorf("line %d: %s: listen: empty address", item.Line, ctx)
		}
		if err := specifiers(a); err != nil {
			return nil, fmt.Errorf("line %d: %s: listen: %v", item.Line, ctx, err)
		}
		if reListenProto.MatchString(a) {
			return nil, fmt.Errorf("line %d: %s: listen: %q: a listen address takes no protocol: write it without the /%s (systemd listens on TCP); as written, it is a socket file at ./%s", item.Line, ctx, a, a[strings.LastIndex(a, "/")+1:], a)
		}
		if portMapping(a) {
			return nil, fmt.Errorf("line %d: %s: listen: %q is a docker port mapping; listen: takes one address for socket activation (8080, 127.0.0.1:8080, /abs/path, run/api.sock, @abstract), and a program that binds its own port needs no key at all", item.Line, ctx, a)
		}
		if why := listenAddress(a); why != "" {
			return nil, fmt.Errorf("line %d: %s: listen: %q: %s", item.Line, ctx, a, why)
		}
		if strings.Contains(a, "/") && !strings.HasPrefix(a, "/") && !strings.HasPrefix(a, "%") && !strings.HasPrefix(a, "@") {
			a = filepath.Join(UnitPath(dir), a) // a % in the directory is no specifier
		}
		out = append(out, a)
	}
	return out, nil
}

// reListenProto is compose's port with a protocol (8080/tcp, 127.0.0.1:8080/udp).
var reListenProto = regexp.MustCompile(`^([0-9]+|[0-9.]+:[0-9]+|\[[0-9A-Fa-f:.]+\]:[0-9]+)/(tcp|udp|sctp)$`)

// portMapping reports compose's HOST:CONTAINER (or IP:HOST:CONTAINER) port
// spelling, which no socket address looks like.
func portMapping(a string) bool {
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(a, "/tcp"), "/udp"), ":")
	digits := func(s string) bool { return s != "" && strings.Trim(s, "0123456789-") == "" }
	return len(parts) == 2 && digits(parts[0]) && digits(parts[1]) || len(parts) == 3 && digits(parts[1]) && digits(parts[2])
}
