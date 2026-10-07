package config

import "testing"

func TestSeconds_ParsesSystemdTimeSpans(t *testing.T) {
	cases := map[string]int{"5s": 5, "2min": 120, "1h 30min": 5400, "1h30min": 5400, "500ms": 1, "90": 90, "1.5s": 2, "1 0s": 1}
	for in, want := range cases {
		got, err := Seconds(in)
		if err != nil || got != want {
			t.Errorf("seconds(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "", "5 s x", "h", "1.2.3s", "5 m s"} {
		if _, err := Seconds(bad); err == nil {
			t.Errorf("seconds(%q) should fail", bad)
		}
	}
}
