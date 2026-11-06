package probe

import (
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// TestMain makes the test binary stand in for the program: the watch a
// passed start leaves behind is this program again, run with the probe
// verb. It watches the tests' process as the service's program (MAINPID),
// so it ends with them, and keeps no state (no INVOCATION_ID) unless a
// test sets one. With PROBE_TEST_COLLECT set, the collector runs again and
// again from half a second after the start, as it does in a watch that has
// run a while.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Verb {
		if os.Getenv("PROBE_TEST_COLLECT") != "" {
			go func() {
				for time.Sleep(500 * time.Millisecond); ; time.Sleep(50 * time.Millisecond) {
					runtime.GC()
				}
			}()
		}
		if err := Run(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Setenv("MAINPID", strconv.Itoa(os.Getpid()))
	os.Unsetenv("INVOCATION_ID")
	testenv.Main(m, config.OverrideVars...)
}
