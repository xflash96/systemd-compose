package config

import (
	"strings"
	"testing"
)

// A healthcheck span under a second, or with a fraction, would run as the
// next whole second, so it is refused.
func TestLoad_RefusesFractionalHealthcheckSpans(t *testing.T) {
	for _, span := range []string{"interval: 200ms", "timeout: 1.5s", "start_period: 2500ms"} {
		if _, err := loadYAML(t, "services: {a: {command: x, healthcheck: {test: [x], "+span+"}}}"); err == nil || !strings.Contains(err.Error(), "whole seconds") {
			t.Errorf("%s: %v", span, err)
		}
	}
}

func TestHealthcheck_StartTimeoutIsStartPeriodPlusTimeoutPlus5(t *testing.T) {
	for _, c := range []struct {
		startPeriod, timeout string
		want                 int
	}{{"60s", "5s", 70}, {"1min", "2", 67}, {"0", "1.5s", 7}} {
		if got := (&Healthcheck{StartPeriod: c.startPeriod, Timeout: c.timeout}).StartTimeout(); got != c.want {
			t.Errorf("start_period %s, timeout %s: %d, want %d", c.startPeriod, c.timeout, got, c.want)
		}
	}
}
