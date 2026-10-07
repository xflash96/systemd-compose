package config

import (
	"testing"
)

// service_healthy on a dependency whose type signals readiness: notify,
// notify-reload and dbus alike.
func TestValidateGraph_ServiceHealthyTakesAReadinessType(t *testing.T) {
	for _, typ := range []string{"notify", "notify-reload", "dbus"} {
		_, err := loadYAML(t, "services:\n  a: {command: /bin/true, depends_on: {b: {condition: service_healthy}}}\n  b: {command: /bin/true, unit: {Service: {Type: "+typ+", BusName: org.example.B}}}\n")
		if err != nil {
			t.Errorf("Type=%s: %v", typ, err)
		}
	}
	if _, err := loadYAML(t, "services:\n  a: {command: /bin/true, depends_on: {b: {condition: service_healthy}}}\n  b: {command: /bin/true}\n"); err == nil {
		t.Error("service_healthy on a plain service passed")
	}
}
