package probe

import (
	"os"
	"strconv"
	"testing"

	"github.com/xflash96/systemd-compose/internal/config"
	"github.com/xflash96/systemd-compose/internal/testenv"
)

// TestMain makes the test binary stand in for the program: the watch a
// passed start leaves behind is this program again, run with the probe
// verb. It watches the tests' process as the service's program (MAINPID),
// so it ends with them, and keeps no state (no INVOCATION_ID) unless a
// test sets one.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Verb {
		if err := Run(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Setenv("MAINPID", strconv.Itoa(os.Getpid()))
	os.Unsetenv("INVOCATION_ID")
	testenv.Main(m, config.OverrideVars...)
}
