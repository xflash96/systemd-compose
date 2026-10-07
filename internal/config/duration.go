package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func duration(n *yaml.Node, ctx string) (string, error) {
	s, err := scalar(n, ctx)
	if err != nil {
		return "", err
	}
	if _, err := Seconds(s); err != nil {
		return "", fmt.Errorf("line %d: %s: %v", n.Line, ctx, err)
	}
	return s, nil
}

// Seconds parses a systemd time span ("5s", "2min", "1h 30min", "1h30min",
// "1.5s") into whole seconds, rounded up. It is the one grammar for time
// spans in this program; the schema validates by calling it.
func Seconds(span string) (int, error) {
	total, err := spanSeconds(span)
	if err != nil {
		return 0, err
	}
	if total != float64(int(total)) {
		return int(total) + 1, nil
	}
	return int(total), nil
}

// spanSeconds is a systemd time span in seconds, as written.
func spanSeconds(span string) (float64, error) {
	bad := fmt.Errorf("%q is not a systemd time span (e.g. 5s, 2min, 1h 30min)", span)
	s := strings.Trim(span, " ")
	if s == "" {
		return 0, bad
	}
	total := 0.0
	for s != "" {
		i := 0
		for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
			i++
		}
		if i == 0 {
			return 0, bad
		}
		n, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			return 0, bad
		}
		s = strings.TrimLeft(s[i:], " ")
		j := 0
		for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
			j++
		}
		unit := s[:j]
		s = strings.TrimLeft(s[j:], " ")
		mult := 1.0
		switch unit {
		case "", "s", "sec", "second", "seconds":
		case "ms", "msec":
			mult = 0.001
		case "us", "usec":
			mult = 0.000001
		case "m", "min", "minute", "minutes":
			mult = 60
		case "h", "hr", "hour", "hours":
			mult = 3600
		case "d", "day", "days":
			mult = 86400
		case "w", "week", "weeks":
			mult = 604800
		default:
			return 0, bad
		}
		total += n * mult
	}
	return total, nil
}
