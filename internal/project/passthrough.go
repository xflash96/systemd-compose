package project

import (
	"slices"
	"strings"
)

// changesUnits are the systemctl verbs that pass through and change the
// units they name: inside a project they take its services alone, as
// start, stop and the rest of this program's verbs do.
var changesUnits = strings.Fields(`reset-failed freeze thaw clean enable disable reenable preset
	mask unmask revert edit set-property reload try-restart reload-or-restart
	try-reload-or-restart condreload condrestart condstop force-reload isolate
	add-wants add-requires`)

// systemctlValueFlags are systemctl's flags that take the next word as
// their value. That word names no unit: status -n 5 names none, and is
// refused rather than run on the whole user instance.
var systemctlValueFlags = strings.Fields(`-n --lines -o --output -p --property -P -t --type -s --signal
	-H --host -M --machine --state --what --kill-whom --kill-value --job-mode --root --image
	--preset-mode --timestamp --drop-in`)

// namesAUnit says a passed-through systemctl command line names a unit:
// a word that is no flag, nor a flag's value.
func namesAUnit(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			return i+1 < len(args)
		case slices.Contains(systemctlValueFlags, args[i]):
			i++
		case !strings.HasPrefix(args[i], "-"):
			return true
		}
	}
	return false
}

// changesValueFlags are their flags that take a value: inside a project
// only the one-word form, --what=state.
var changesValueFlags = strings.Fields("--what --drop-in --job-mode --preset-mode")

// Registers are those that register or unregister a unit, which up and
// down own for the project's.
var Registers = strings.Fields("enable disable reenable mask preset")
