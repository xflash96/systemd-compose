package render

import (
	"fmt"
	"strings"

	"github.com/xflash96/systemd-compose/internal/config"
)

func yesno(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

type entry struct {
	key, value string
	owner      string // "" for a default, else the translated key that owns it
	isDefault  bool
	repeat     bool // written by add(): one of several lines, not a single fact
}

type section struct {
	name    string
	entries []entry
}

type unitFile struct {
	sections []*section
	oneshot  bool // Type=oneshot: ExecStart= may be given several times
}

func newUnit() *unitFile { return &unitFile{} }

func (u *unitFile) section(name string) *section {
	for _, s := range u.sections {
		if s.name == name {
			return s
		}
	}
	s := &section{name: name}
	u.sections = append(u.sections, s)
	return s
}

// own writes a directive a translated key owns; owner names that key for
// the collision message (empty for structural directives the tool owns).
func (u *unitFile) own(sec, key, value, owner string) {
	if owner == "" {
		owner = "systemd-compose"
	}
	u.section(sec).entries = append(u.section(sec).entries, entry{key, value, owner, false, false})
}

// add appends a repeatable directive.
func (u *unitFile) add(sec, key, value string) {
	u.section(sec).entries = append(u.section(sec).entries, entry{key, value, "systemd-compose", false, true})
}

// setDefault writes a directive the pass-through may override.
func (u *unitFile) setDefault(sec, key, value string) {
	u.section(sec).entries = append(u.section(sec).entries, entry{key, value, "", true, false})
}

// merge applies one pass-through key under the three rules. A directive
// the tool wrote as a single fact refuses first, whether or not systemd
// would accept a second line of it: `schedule:` owns OnCalendar= even
// though timers may list several.
func (u *unitFile) merge(secName string, k config.PassKey) error {
	s := u.section(secName)
	for _, e := range s.entries {
		if e.key == k.Name && !e.isDefault && !e.repeat {
			return fmt.Errorf("line %d: unit: %s: %s: collides with %s, which already writes %s=; one source per directive", k.Line, secName, k.Name, e.owner, k.Name)
		}
	}
	if repeatable(secName, k.Name) || u.oneshot && secName == "Service" && k.Name == "ExecStart" {
		for _, v := range k.Values {
			// the same address given twice is bound twice, and the socket
			// fails with "address in use"
			if secName == "Socket" && strings.HasPrefix(k.Name, "Listen") {
				for _, e := range s.entries {
					if e.key == k.Name && e.value == v {
						return fmt.Errorf("line %d: unit: Socket: %s: %s is listed already (listen: gives it); give it once", k.Line, k.Name, v)
					}
				}
			}
			s.entries = append(s.entries, entry{k.Name, v, "", false, true})
		}
		return nil
	}
	if len(k.Values) != 1 {
		return fmt.Errorf("line %d: unit: %s: %s: takes one value here", k.Line, secName, k.Name)
	}
	for i, e := range s.entries {
		if e.key == k.Name && e.isDefault {
			s.entries[i] = entry{k.Name, k.Values[0], "", false, false} // a default gives way, in place
			return nil
		}
	}
	s.entries = append(s.entries, entry{k.Name, k.Values[0], "", false, false})
	return nil
}

// text is the unit file, after the one check every rendered line must
// pass: no newline in a key or value, or a value would become directives
// of its own and walk past every refusal above.
func (u *unitFile) text() (string, error) {
	for _, s := range u.sections {
		for _, e := range s.entries {
			if strings.ContainsAny(e.key+e.value, "\n\r") {
				return "", fmt.Errorf("[%s] %s: a newline in a value is refused (it would render as further directives)", s.name, e.key)
			}
			// systemd strips spaces and tabs (not other Unicode blanks), so
			// the unit would read differently; on an Exec line they only
			// separate words (a ${VAR:-} that came out empty), and pass
			if t := strings.Trim(e.value, " \t"); t != e.value && !strings.HasPrefix(e.key, "Exec") {
				return "", fmt.Errorf("[%s] %s: a value with leading or trailing whitespace is refused (systemd strips it, so %q would read as %q)", s.name, e.key, e.value, t)
			}
			// systemd joins a line that ends in a backslash to the next,
			// whose directive is then lost (Slice=, and the limits with it)
			if t := strings.TrimRight(e.value, "\\"); (len(e.value)-len(t))%2 == 1 {
				return "", fmt.Errorf("[%s] %s: a value ending in a backslash is refused (systemd would join the next line to it); write \\\\ for a backslash of its own", s.name, e.key)
			}
		}
	}
	return u.String(), nil
}

// String is the unit file's text, sections in the order first written.
func (u *unitFile) String() string {
	var b strings.Builder
	for i, s := range u.sections {
		if len(s.entries) == 0 {
			continue
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", s.name)
		for _, e := range s.entries {
			fmt.Fprintf(&b, "%s=%s\n", e.key, e.value)
		}
	}
	return b.String()
}

// repeatable says whether systemd accumulates a directive across lines.
// Anything else replaces, so it is single-valued here.
func repeatable(sec, key string) bool {
	if strings.HasPrefix(key, "Condition") || strings.HasPrefix(key, "Assert") {
		return true
	}
	switch sec {
	case "Unit":
		switch key {
		case "After", "Before", "Wants", "Requires", "Requisite", "BindsTo", "PartOf", "Upholds",
			"Conflicts", "OnFailure", "OnSuccess", "PropagatesReloadTo", "ReloadPropagatedFrom",
			"PropagatesStopTo", "StopPropagatedFrom", "JoinsNamespaceOf", "RequiresMountsFor",
			"WantsMountsFor", "Documentation":
			return true
		}
	case "Service":
		switch key {
		case "Environment", "EnvironmentFile", "PassEnvironment", "UnsetEnvironment",
			"ExecStartPre", "ExecStartPost", "ExecCondition", "ExecReload", "ExecStop", "ExecStopPost",
			"ReadWritePaths", "ReadOnlyPaths", "InaccessiblePaths", "ExecPaths", "NoExecPaths",
			"BindPaths", "BindReadOnlyPaths", "TemporaryFileSystem", "ExtensionDirectories",
			"SystemCallFilter", "SystemCallLog", "RestrictAddressFamilies", "RestrictFileSystems",
			"CapabilityBoundingSet", "AmbientCapabilities", "DeviceAllow", "SupplementaryGroups",
			"LogExtraFields", "LogFilterPatterns", "StateDirectory", "CacheDirectory",
			"LogsDirectory", "RuntimeDirectory", "ConfigurationDirectory",
			"SetCredential", "SetCredentialEncrypted", "LoadCredential", "LoadCredentialEncrypted",
			"ImportCredential", "SocketBindAllow", "SocketBindDeny", "RestrictNetworkInterfaces",
			"SuccessExitStatus", "RestartPreventExitStatus", "RestartForceExitStatus":
			return true
		}
	case "Timer":
		switch key {
		case "OnCalendar", "OnActiveSec", "OnBootSec", "OnStartupSec", "OnUnitActiveSec", "OnUnitInactiveSec":
			return true
		}
	case "Socket":
		switch key {
		case "ListenStream", "ListenDatagram", "ListenSequentialPacket", "ListenFIFO", "ListenSpecial",
			"ListenNetlink", "ListenMessageQueue", "ListenUSBFunction", "Symlinks",
			"ExecStartPre", "ExecStartPost", "ExecStopPre", "ExecStopPost":
			return true
		}
	}
	return false
}
