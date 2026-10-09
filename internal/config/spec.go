package config

import "strings"

// The shape of systemd-compose.yaml, stated once: the keys of each map, the
// forms a value may take, and the words, patterns and bounds it must keep,
// with a line of documentation each. The parser takes its key sets, words
// and patterns from here, and config-schema.json is generated from it
// (TestSchema_IsGeneratedFromTheSpec; UPDATE_SCHEMA=1 writes it). What a
// shape cannot say stays in the parser: interpolation, specifiers, and the
// rules between keys.

// A Key is one key of a map, and the forms its value may take.
type Key struct {
	Name     string
	Doc      string // one line, the schema's description
	Required bool
	Default  string // what the parser takes when the key is left out
	Forms    []Form // the value takes any one of them
}

// A Form is one shape a value may take.
type Form struct {
	Kind       Kind
	Words      []string          // Word: the words it may be
	Pattern    string            // Text: what it matches; Map: what each key matches
	MinLen     int               // Text: characters; List: items; Map, Object: keys
	MaxLen     int               // Text: characters, 0 for no limit
	Bounded    bool              // Number, Integer: Min holds
	Min, Max   float64           // Number, Integer: bounds; Max 0 for none
	Items      []Form            // List: each item; Map: each value
	Keys       []Key             // Object: its keys
	Refused    map[string]string // Map: keys it may not have, and what to write instead
	Extensions bool              // Object: an x- key is ignored
	RequireAny []string          // Object: paths (a.b.c), at least one of which is given
}

// Kind is what a Form is.
type Kind int

const (
	Text    Kind = iota // a string
	Word                // one of Words
	Bool                // a boolean
	Number              // a number
	Integer             // an integer
	Scalar              // a string, number or boolean, read as text
	Null                // nothing: a key written alone
	List                // a sequence of Items
	Map                 // a map of the file's own keys to Items
	Object              // a map of Keys
)

// names are the names of an Object's keys.
func (f Form) names() []string {
	var out []string
	for _, k := range f.Keys {
		out = append(out, k.Name)
	}
	return out
}

// key is an Object's key of that name.
func (f Form) key(name string) (Key, bool) {
	for _, k := range f.Keys {
		if k.Name == name {
			return k, true
		}
	}
	return Key{}, false
}

// has reports whether an Object form has the key.
func (f Form) has(name string) bool { _, ok := f.key(name); return ok }

// Default is the default of a service's key, by its path:
// Default("schedule.accuracy") is "10s"; or of a top-level key the
// services lack: Default("registration") is "link". A path the table
// lacks panics.
func Default(path string) string {
	f, k := serviceMap, Key{}
	if first, _, _ := strings.Cut(path, "."); !serviceMap.has(first) {
		f = Spec
	}
	for _, name := range strings.Split(path, ".") {
		var ok bool
		if k, ok = f.key(name); !ok {
			panic("config: no key " + path)
		}
		f = object(k.Forms)
	}
	return k.Default
}

// object is the Object among forms, or among a Map's values.
func object(forms []Form) Form {
	for _, f := range forms {
		if f.Kind == Map {
			f = object(f.Items)
		}
		if f.Kind == Object {
			return f
		}
	}
	return Form{}
}

// braces words an Object's keys as "{a, b}", for a message.
func (f Form) braces() string { return "{" + strings.Join(f.names(), ", ") + "}" }

// orList words a list as "a, b or c".
func orList(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// Names and sizes.
const (
	serviceNamePattern = `^[A-Za-z0-9_][A-Za-z0-9_-]*$` // a leading - would read as a flag on the command line
	projectNamePattern = serviceNamePattern
	profilePattern     = `^[A-Za-z0-9][A-Za-z0-9_.-]*$`
	directivePattern   = `^[A-Za-z][A-Za-z0-9]*$`
	memoryPattern      = `^[0-9]*[1-9][0-9]*[KMGT]?$`
	varNamePattern     = `^[A-Za-z_][A-Za-z0-9_]*$`
	durationPattern    = `^ *(([0-9]+\.?[0-9]*|\.[0-9]+)( *(us|usec|ms|msec|s|sec|second|seconds|m|min|minute|minutes|h|hr|hour|hours|d|day|days|w|week|weeks)| |$) *)+$`
)

// interpolated is a value written as ${VAR}: interpolation fills it in from
// the .env before the parser reads it.
var interpolated = Form{Kind: Text, Pattern: `\$\{?[A-Za-z_]`}

var (
	boolean   = []Form{{Kind: Bool}, interpolated}
	spanForms = []Form{{Kind: Number, Bounded: true}, {Kind: Text, Pattern: durationPattern}, interpolated}
	words     = []Form{{Kind: Text, MinLen: 1}, {Kind: List, MinLen: 1, Items: []Form{{Kind: Scalar}}}}
)

// The words some keys take.
var (
	restartPolicy = Form{Kind: Word, Words: []string{"no", "on-failure", "always", "unless-stopped"}}
	condition     = Form{Kind: Word, Words: []string{"service_started", "service_healthy", "service_completed_successfully"}}
	onChange      = Form{Kind: Word, Words: []string{"restart", "start-only"}}
	registration  = Form{Kind: Word, Words: []string{"link", "copy"}}
)

// The maps some keys take.
var (
	restartMap = Form{Kind: Object, Keys: []Key{
		{Name: "policy", Required: true, Forms: []Form{restartPolicy, interpolated}},
		{Name: "delay", Doc: "RestartSec=.", Forms: spanForms},
	}}
	dependency = Form{Kind: Object, Keys: []Key{
		{Name: "condition", Default: "service_started", Doc: "service_started, service_healthy (waits for its healthcheck) or service_completed_successfully (waits for it to exit 0).", Forms: []Form{condition}},
		{Name: "required", Default: "false", Doc: "Requires= instead of Wants=. A condition other than service_started makes it true.", Forms: boolean},
		{Name: "restart", Doc: "PartOf=: restarting or stopping that service restarts or stops this one.", Forms: boolean},
	}}
	scheduleMap = Form{Kind: Object, Keys: []Key{
		{Name: "calendar", Required: true, Doc: "OnCalendar=, as systemd-analyze calendar takes it.", Forms: []Form{{Kind: Text, MinLen: 1}}},
		{Name: "accuracy", Default: "10s", Doc: "AccuracySec=.", Forms: spanForms},
		{Name: "persistent", Default: "true", Doc: "Persistent=: a run missed while the machine was off happens at the next boot.", Forms: boolean},
		{Name: "randomized_delay", Doc: "RandomizedDelaySec=.", Forms: spanForms},
	}}
	healthcheckMap = Form{Kind: Object, Keys: []Key{
		{Name: "test", Required: true, Doc: "The probe's program and its arguments, with no CMD or CMD-SHELL prefix.", Forms: []Form{{Kind: List, MinLen: 1, Items: []Form{{Kind: Scalar}}}}},
		{Name: "interval", Default: "2s", Doc: "Whole seconds.", Forms: spanForms},
		{Name: "timeout", Default: "5s", Doc: "Whole seconds.", Forms: spanForms},
		{Name: "start_period", Default: "60s", Doc: "How long the probe keeps trying. Whole seconds.", Forms: spanForms},
	}}
	buildMap = Form{Kind: Object, Keys: []Key{
		{Name: "run", Required: true, Doc: "The steps, one command each, with no shell.", Forms: []Form{{Kind: List, MinLen: 1, Items: []Form{{Kind: Text, MinLen: 1}}}}},
		{Name: "creates", Doc: "What the build makes, relative to working_dir. up runs the build when it is missing, and skips a build without creates:.", Forms: []Form{{Kind: Text, MinLen: 1}}},
	}}
	resourcesMap = Form{Kind: Object, MinLen: 1, Keys: []Key{
		{Name: "memory", Doc: "MemoryMax=: bytes, with an optional K, M, G or T.", Forms: []Form{{Kind: Integer, Bounded: true, Min: 1}, {Kind: Text, Pattern: memoryPattern}, interpolated}},
		{Name: "cpus", Doc: "CPUQuota=: a number of CPUs, 0.001 to 100000. Needs the cpu controller delegated to the user manager.", Forms: []Form{{Kind: Number, Bounded: true, Min: 0.001, Max: 100000}, interpolated}},
		{Name: "pids", Doc: "TasksMax=: processes and threads.", Forms: []Form{{Kind: Integer, Bounded: true, Min: 1}, interpolated}},
	}}
	envFileMap = Form{Kind: Object, Keys: []Key{
		{Name: "path", Required: true, Doc: "The file, relative to the yaml.", Forms: []Form{{Kind: Text, MinLen: 1}}},
		{Name: "required", Default: "true", Doc: "Whether the file must exist.", Forms: boolean},
	}}
	envFileEntry = []Form{{Kind: Text, MinLen: 1}, envFileMap}
)

// cgroupIO are the io controller's directives, which no user manager is
// delegated, so they would render and do nothing. IOScheduling*= is
// ioprio(2), which needs no controller, and passes.
var cgroupIO = strings.Fields(`IOAccounting IOWeight StartupIOWeight IODeviceWeight
	IOReadBandwidthMax IOWriteBandwidthMax IOReadIOPSMax IOWriteIOPSMax IODeviceLatencyTargetSec
	BlockIOAccounting BlockIOWeight StartupBlockIOWeight BlockIODeviceWeight
	BlockIOReadBandwidth BlockIOWriteBandwidth`)

// unitSection is a section of unit:, with the directives it refuses.
func unitSection(refused map[string]string) Form {
	r := map[string]string{}
	for _, k := range cgroupIO {
		r[k] = "the io cgroup controller is not delegated to the user manager, so this directive would render fine and do nothing"
	}
	for k, why := range refused {
		r[k] = why
	}
	return Form{Kind: Map, Pattern: directivePattern, Refused: r, Items: []Form{{Kind: Scalar}, {Kind: List, MinLen: 1, Items: []Form{{Kind: Scalar}}}}}
}

// unitMap is unit:, the sections a service carries.
var unitMap = Form{Kind: Object, Keys: []Key{
	{Name: "Unit", Forms: []Form{unitSection(nil)}},
	{Name: "Service", Forms: []Form{unitSection(map[string]string{
		// it would beat environment: unchecked, never be hashed for a
		// restart, and not reach build or run
		"EnvironmentFile": "use env_file: (one named file per entry), which the tool checks against environment:, watches for changes and hands to build and run",
		// it would reach the service but not build, nor a run before the
		// first up, and an env_file would override it unchecked
		"Environment": "use environment: (a map or a list of KEY=value), which build and run get too, and which the tool checks against env_file:",
		// it would move the service, but not build or run
		"WorkingDirectory": "use working_dir:, which build and run use too",
	})}},
	{Name: "Socket", Doc: "Only with listen:.", Forms: []Form{unitSection(nil)}},
	{Name: "Timer", Doc: "Only with schedule:.", Forms: []Form{unitSection(nil)}},
}}

// serviceMap is one entry of services:.
var serviceMap = Form{Kind: Object, Extensions: true, RequireAny: []string{"command", "entrypoint", "unit.Service.ExecStart"}, Keys: []Key{
	{Name: "command", Doc: "The program and its arguments. The program is looked up in ~/.local/bin and PATH when up renders the units.", Forms: words},
	{Name: "working_dir", Doc: "WorkingDirectory=, relative to the yaml. Default: the yaml's directory. It must exist.", Forms: []Form{{Kind: Text, MinLen: 1}}},
	{Name: "environment", Doc: "Variables for the service, as a map or a list of KEY=value. Every key needs a value: nothing comes from your shell.", Forms: []Form{
		{Kind: Map, Pattern: varNamePattern, Items: []Form{{Kind: Scalar}}},
		{Kind: List, Items: []Form{{Kind: Text, Pattern: strings.TrimSuffix(varNamePattern, "$") + "="}}},
	}},
	{Name: "env_file", Doc: "Env files systemd reads for the service: one, or a list. A value from a file wins over environment:.", Forms: append(append([]Form(nil), envFileEntry...), Form{Kind: List, MinLen: 1, Items: envFileEntry})},
	{Name: "restart", Default: "no", Doc: "Restart=; unless-stopped is the same as always. The map form sets the delay too.", Forms: []Form{restartPolicy, interpolated, restartMap}},
	{Name: "depends_on", Doc: "Services this one starts after. A list is the default edge to each (After= and Wants=); the map form sets the condition.", Forms: []Form{
		{Kind: List, Items: []Form{{Kind: Text}}},
		{Kind: Map, Items: []Form{{Kind: Null}, dependency}},
	}},
	{Name: "schedule", Doc: "A timer that starts the service as a oneshot: an OnCalendar= expression, or the map form.", Forms: []Form{{Kind: Text, MinLen: 1}, scheduleMap}},
	{Name: "unit", Doc: "Raw systemd sections, merged into the unit last. A directive a key above writes is an error.", Forms: []Form{unitMap}},
	{Name: "on_change", Default: "restart", Doc: "restart: up restarts the service when it changes. start-only: up only starts it when it is down.", Forms: []Form{onChange, interpolated}},
	{Name: "healthcheck", Doc: "A readiness probe: the service counts as started once test: passes.", Forms: []Form{healthcheckMap}},
	{Name: "oneshot", Doc: "Type=oneshot with RemainAfterExit=yes: the service counts as started once it has exited 0.", Forms: boolean},
	{Name: "build", Doc: "Steps up runs before it starts the service, not rendered.", Forms: []Form{buildMap}},
	{Name: "resources", Doc: "This service's caps.", Forms: []Form{resourcesMap}},
	{Name: "listen", Doc: "Socket activation: a port, IP:port, an absolute path, a relative path with a slash, or @abstract; or a list of them.", Forms: []Form{
		{Kind: Text}, {Kind: Integer},
		{Kind: List, MinLen: 1, Items: []Form{{Kind: Text}, {Kind: Integer}}},
	}},
	{Name: "entrypoint", Doc: "Words put in front of command:.", Forms: words},
	{Name: "profiles", Doc: "The service runs only while one of these profiles is active.", Forms: []Form{{Kind: List, MinLen: 1, Items: []Form{{Kind: Text, Pattern: profilePattern}}}}},
}}

// Spec is the whole file.
var Spec = Form{Kind: Object, Extensions: true, Keys: []Key{
	{Name: "name", Doc: "The project's name, the prefix of every unit. Letters, digits, _ and -, not starting with -; at most 200 characters, a - counting 4 (units spell it \\x2d). Default: the directory's name, with _ for anything else. -p NAME and SYSTEMD_COMPOSE_PROJECT_NAME override it.", Forms: []Form{{Kind: Text, Pattern: projectNamePattern, MaxLen: maxProjectName}, interpolated}},
	{Name: "resources", Doc: "A cap on the whole project, on its slice. A change applies in place and restarts nothing.", Forms: []Form{resourcesMap}},
	{Name: "registration", Default: "link", Doc: "How up registers the units. link: links in your unit directory to the files in .systemd-compose/. copy: copies of those files, which load at boot even while the project's filesystem is not mounted (NFS, FUSE).", Forms: []Form{registration, interpolated}},
	{Name: "services", Required: true, Doc: "The services, by name. Each becomes PROJECT-NAME.service.", Forms: []Form{{Kind: Map, MinLen: 1, Pattern: serviceNamePattern, Items: []Form{serviceMap}}}},
}}
