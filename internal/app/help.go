package app

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"strings"
)

func commandHelpPath(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	depth := 1
	switch args[0] {
	case "profile", "units", "messages", "api":
		if len(args) > 1 {
			depth = 2
		}
	}
	return args[:depth]
}

func bareCommandHelpPath(args []string) []string {
	switch strings.Join(args, " ") {
	case "profile", "units", "messages", "api",
		"profile login", "profile add", "profile use", "profile remove",
		"units get", "units connection", "units create", "units update",
		"messages get", "messages tail", "messages export", "api call":
		return args
	default:
		return nil
	}
}

func newCommandFlagSet(name, helpKey string, opts options) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if opts.agentMode {
		fs.SetOutput(io.Discard)
	} else {
		fs.SetOutput(opts.stderr)
	}
	fs.Usage = func() {
		_ = printCommandHelpForMode(opts.stderr, strings.Fields(helpKey), opts.agentMode, opts.compact)
	}
	return fs
}

func commandError(opts options, helpKey, message string) error {
	if opts.agentMode {
		return fmt.Errorf("%s", message)
	}
	_ = printCommandHelpForMode(opts.stderr, strings.Fields(helpKey), opts.agentMode, opts.compact)
	return fmt.Errorf("%s", message)
}

// invocationName is the executable name the help text should speak about. Both
// binaries share one help catalogue, so the catalogue stores {cmd} and the
// renderer substitutes whichever name the user actually typed.
func invocationName(agentMode bool) string {
	if agentMode {
		return "wlna"
	}
	return "wln"
}

func renderHelp(text string, agentMode bool) string {
	return strings.ReplaceAll(text, "{cmd}", invocationName(agentMode))
}

func printCommandHelpForMode(w io.Writer, path []string, agentMode, compact bool) error {
	if !agentMode {
		return printCommandHelp(w, path, agentMode)
	}
	var rendered bytes.Buffer
	if err := printCommandHelp(&rendered, path, agentMode); err != nil {
		return err
	}
	return writeJSON(w, map[string]any{
		"command": strings.Join(path, " "),
		"help":    strings.TrimSpace(rendered.String()),
	}, compact)
}

func rejectUnexpectedArgs(fs *flag.FlagSet, opts options, helpKey string) error {
	if fs.NArg() == 0 {
		return nil
	}
	return commandError(opts, helpKey, fmt.Sprintf("unexpected positional arguments: %s", strings.Join(fs.Args(), " ")))
}

func printCommandHelp(w io.Writer, path []string, agentMode bool) error {
	key := strings.Join(path, " ")
	if len(path) == 1 && path[0] == "--all" {
		return printAllHelp(w, agentMode)
	}
	help, ok := helpSections[key]
	if !ok {
		return fmt.Errorf("unknown help topic %q; run '%s help' to list topics", key, invocationName(agentMode))
	}
	_, err := fmt.Fprintln(w, renderHelp(strings.TrimSpace(help), agentMode))
	return err
}

func printAllHelp(w io.Writer, agentMode bool) error {
	order := []string{"", "profile", "profile login", "profile add", "profile list", "profile use", "profile remove", "profile check", "units", "units list", "units get", "units status", "units device-types", "units connection", "units create", "units update", "messages", "messages get", "messages tail", "messages export", "doctor", "api", "api call", "update"}
	for i, key := range order {
		if i > 0 {
			if _, err := fmt.Fprintln(w, "\n---"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, renderHelp(strings.TrimSpace(helpSections[key]), agentMode)); err != nil {
			return err
		}
	}
	return nil
}

var helpSections = map[string]string{
	"": `{cmd} — export and inspect data from Wialon Hosting or Wialon Local

USAGE
  {cmd} [GLOBAL OPTIONS] COMMAND [SUBCOMMAND] [ARGUMENTS] [OPTIONS]

COMMANDS
  profile   Log in and manage saved server profiles
  units     List units and inspect connection/position status
  messages  Export, tail, or download messages
  doctor    Validate the selected profile and API access
  api       Call a Remote API service directly
  update    Check for and install the latest release

GLOBAL OPTIONS
  --profile NAME      Override the configured default profile
  --config PATH       Override the configuration file
  --timeout DURATION  HTTP timeout (default: 2m)
  --width N           Override detected terminal width for tables
  --wide              Do not fit tables to the terminal width
  --compact           Emit compact JSON where JSON output is selected
  --version           Print the version

AGENT MODE
  Invoke the same executable as wlna to select compact JSON defaults for
  results, help, version information, and errors. Streaming message tails use
  NDJSON. Explicit --format options override the default; place the global
  --compact=false before the command to request indented JSON:

    wlna --compact=false units get 1001

  Agent mode disables automatic update prompts and writes message downloads to
  stdout by default. Native 'messages export --output -' is the exception: its
  stdout is the requested binary export, not JSON.

HELP
  {cmd} help COMMAND [SUBCOMMAND]
  {cmd} COMMAND [SUBCOMMAND] --help
  {cmd} help --all

CONVENTIONS
  UNIT is an exact Wialon ID, exact unit name, or exact unique ID/IMEI.
  Interval/age durations also accept d (days) and w (weeks): 2h, 7d, 2w.
  Other durations use Go units: 500ms, 30s, 5m, 24h.
  Progress and diagnostics go to stderr. Structured data goes to stdout.
  Existing files are not replaced unless --force is supplied.`,

	"profile": `{cmd} profile — manage authentication profiles

SUBCOMMANDS
  login NAME   Open Wialon login in a browser and save the issued token
  add NAME     Save a token supplied via WLN_TOKEN or stdin
  list         List profiles without displaying tokens
  use NAME     Select the default profile
  remove NAME  Remove a saved profile
  check [NAME] Diagnose a profile

Run '{cmd} help profile SUBCOMMAND' for details.`,

	"profile login": `{cmd} profile login — browser-based Wialon authorization

USAGE
  {cmd} profile login NAME [--server BASE_URL] [OPTIONS]

REQUIRED
  NAME               Local profile name
  --server BASE_URL  Wialon installation URL containing login.html; required
                     for a new profile. Re-login of an existing profile reuses
                     its saved server, access flags, and subuser.
                     https://hst-api.wialon.com is treated as
                     https://hosting.wialon.com.

OPTIONS
  --default                 Make this the default profile
  --operate-as USER         Open API sessions as a subuser
  --user USER               Pre-fill the login name
  --lang CODE               Login page language (default: ru)
  --access FLAGS            Decimal token access flags (default: 768)
  --duration DURATION       Token lifetime; 0 means unlimited
  --callback-timeout DURATION  Authorization timeout (default: 5m)
  --no-open                 Print URL instead of opening a browser
  --allow-http              Permit HTTP for a trusted Wialon Local server

EXAMPLES
  {cmd} profile login hosting --server https://hosting.wialon.com --default
  {cmd} profile login hosting`,

	"profile add": `{cmd} profile add — save an existing access token

USAGE
  WLN_TOKEN=... {cmd} profile add NAME [OPTIONS]
  command-producing-token | {cmd} profile add NAME --token-stdin [OPTIONS]

OPTIONS
  --server URL       Remote API server (default: https://hst-api.wialon.com)
  --operate-as USER  Open sessions as a subuser
  --token-stdin      Read the token from stdin
  --default          Make this the default profile
  --allow-http       Permit HTTP for a trusted Wialon Local server

Tokens are never accepted as command-line arguments or printed.`,

	"profile list": `{cmd} profile list — list saved profiles

USAGE
  {cmd} profile list [--format table|json]

Tokens and session IDs are never included in the output.`,
	"profile use": `{cmd} profile use — select the default profile

USAGE
  {cmd} profile use NAME`,
	"profile remove": `{cmd} profile remove — remove a saved profile

USAGE
  {cmd} profile remove NAME`,
	"profile check": `{cmd} profile check — diagnose a profile

USAGE
  {cmd} profile check [NAME]

Equivalent to '{cmd} doctor' with the selected profile.`,

	"units": `{cmd} units — inspect and manage Wialon units

SUBCOMMANDS
  list    List identity and hardware information
  get     Get one unit and its device connection settings
  status  Show connectivity, last position, point age, and last message
  device-types  List available device types and their TCP/UDP ports
  connection    Show the settings needed to connect a device
  create        Create a unit and assign its device type and unique ID
  update        Change a unit's device type and/or unique ID

Run '{cmd} help units SUBCOMMAND' for details.`,

	"units list": `{cmd} units list — list accessible units

USAGE
  {cmd} units list [--search MASK] [--format table|json|csv] [--fields LIST]

OPTIONS
  --search MASK   Wialon unit-name mask (default: *)
  --format VALUE  table, json, or csv (default: table)
  --fields LIST   Comma-separated JSON fields; requires --format json

EXAMPLES
  {cmd} units list
  {cmd} units list --search 'Truck*' --format json`,

	"units get": `{cmd} units get — get one unit and device connection settings

USAGE
  {cmd} units get UNIT [--format table|json] [--fields LIST]

OPTIONS
  --format VALUE  table or json (default: table; wlna: json)
  --fields LIST   Comma-separated JSON fields; requires --format json

JSON FIELDS
  unit_id,name,unique_id,unique_id2,device_type,device_type_id,
  server_address,tcp_port,udp_port

For a numeric Wialon ID this uses a direct item lookup instead of listing all
units. Exact unit names and unique IDs/IMEIs are also accepted.

EXAMPLE
  wlna units get 1001 --fields unit_id,unique_id,device_type,tcp_port`,

	"units status": `{cmd} units status — inspect last activity and position age

USAGE
  {cmd} units status [UNIT] [OPTIONS]

OPTIONS
  --search MASK       Wialon unit-name mask (default: *)
  --offline           Show only disconnected units
  --stale DURATION    Require the last coordinate to be this old
  --inactive DURATION Require both position and message activity to be this old
  --sort age|name     Sort oldest positions first or by name (default: age)
  --limit N           Maximum rows; 0 means all
  --format VALUE      table, json, or csv (default: table)

EXAMPLES
  {cmd} units status 1001
  {cmd} units status --offline --inactive 30d --sort age

Use --inactive rather than --stale when selecting an unused unit: a unit may
have no recent GPS point while still sending current non-position messages.`,

	"units device-types": `{cmd} units device-types — list available hardware types

USAGE
  {cmd} units device-types [--search TEXT] [--format table|json|csv]

OPTIONS
  --search TEXT   Case-insensitive name substring (default: *)
  --format VALUE  table, json, or csv (default: table)

The result includes the numeric device type ID and its TCP/UDP ports.

EXAMPLES
  {cmd} units device-types --search Teltonika
  {cmd} units device-types --format json`,

	"units connection": `{cmd} units connection — show device connection settings

USAGE
  {cmd} units connection UNIT [--format table|json]

Shows the unit's unique ID, device type, Wialon hardware gateway address, and
the TCP/UDP ports advertised for that device type.

EXAMPLES
  {cmd} units connection 1001
  {cmd} units connection 123456789012345 --format json`,

	"units create": `{cmd} units create — create a Wialon unit

USAGE
  {cmd} units create NAME --device-type TYPE (--unique-id ID | --imei IMEI) [OPTIONS]

OPTIONS
  --device-type TYPE  Numeric device type ID or exact name
  --unique-id ID      Primary device unique ID
  --imei IMEI         Alias for --unique-id
  --creator-id ID     Creator user ID; default is the authenticated user
  --format VALUE      table or json (default: table)

Wialon creates the object first and assigns its unique ID in a second API call.
If the second call fails, the error reports the ID of the object already created.
Editing the unique ID requires a token created with --access 4864 and the
Edit connectivity settings right to the unit.

EXAMPLE
  {cmd} units create "Truck 01" --device-type "Teltonika FMB920" --imei 123456789012345`,

	"units update": `{cmd} units update — change device connectivity identity

USAGE
  {cmd} units update UNIT [--device-type TYPE] [--unique-id ID | --imei IMEI]
                        [--format table|json]

At least one changed value is required. An omitted value is preserved. Wialon
applies the device type and primary unique ID together in one API operation.
This requires a token created with --access 4864 and the Edit connectivity
settings right to the unit.

EXAMPLES
  {cmd} units update 1001 --imei 123456789012345
  {cmd} units update 1001 --device-type "Teltonika FMB920"
  {cmd} units update 1001 --device-type 123 --unique-id 123456789012345`,

	"messages": `{cmd} messages — retrieve Wialon messages

SUBCOMMANDS
  get     Export messages to CSV, JSON, or NDJSON
  tail    Print recent messages and optionally follow new ones
  export  Download a native Wialon format

Run '{cmd} help messages SUBCOMMAND' for details.`,

	"messages get": `{cmd} messages get — export an interval of messages

USAGE
  {cmd} messages get UNIT [INTERVAL] [OPTIONS]

INTERVAL (choose at most one start selector)
  --from RFC3339      Explicit start; default is local midnight
  --last DURATION     Relative interval ending at --to or now
  --today             Local midnight through now
  --yesterday         Previous local calendar day
  --since HH:MM       Today from local time; also accepts RFC3339
  --to RFC3339        Explicit end; default is now

OUTPUT
  --format VALUE      csv, json, or ndjson (default: csv)
  --output PATH       Output file; '-' writes structured data to stdout
  --params LIST       Retain only comma-separated p.* message parameters
  --batch-size N      Messages per API page (default: 10000)
  --all-types         Include non-telemetry message types
  --force             Replace an existing output file

DEFAULT
  With no interval or output options, exports today to
  wialon-UNIQUE_ID-YYYY-MM-DD.csv in the current directory.

EXAMPLES
  {cmd} messages get 1001
  {cmd} messages get 1001 --last 2h --format ndjson --output -
  {cmd} messages get 1001 --yesterday --params temperature,voltage`,

	"messages tail": `{cmd} messages tail — print recent messages

USAGE
  {cmd} messages tail UNIT [OPTIONS]

OPTIONS
  -n N                Number of recent messages (default: 20, max: 10000)
  --follow            Poll until interrupted
  --poll DURATION     Follow polling interval (default: 2s, minimum: 500ms)
  --format VALUE      table, json, or ndjson (default: table)
  --max-params N      Max parameter characters in table output (default: 100)
  --full-params       Do not shorten table parameters
  --all-types         Include non-telemetry message types

EXAMPLES
  {cmd} messages tail 1001 -n 10
  {cmd} messages tail 1001 --follow --format ndjson
  {cmd} messages tail 1001 --max-params 60`,

	"messages export": `{cmd} messages export — download a native Wialon file

USAGE
  {cmd} messages export UNIT [INTERVAL] --format kml|plt|wln|wlb [OPTIONS]

INTERVAL
  --from, --to, --last, --today, --yesterday, and --since work as in
  '{cmd} messages get'. The default interval is today through now.

OPTIONS
  --format VALUE  kml, plt, wln, or wlb (default: wln)
  --compress      Request a ZIP archive
  --output PATH   Output file; '-' writes bytes to stdout
  --force         Replace an existing file

EXAMPLES
  {cmd} messages export 1001 --today --format wln
  {cmd} messages export 1001 --last 24h --format kml
  {cmd} messages export 1001 --yesterday --format wlb --compress`,

	"doctor": `{cmd} doctor — validate configuration and API access

USAGE
  {cmd} [--profile NAME] doctor [--format table|json]

Checks the selected profile, server, login latency, authenticated user, server
time drift, and accessible unit count. Tokens and session IDs are not shown.`,

	"update": `{cmd} update — update the current executable

USAGE
  {cmd} update [--check]

OPTIONS
  --check  Check GitHub Releases without installing the update

The downloaded archive is verified against the release SHA256SUMS before the
executable is replaced. The command also installs or synchronizes the companion
name (wln or wlna) beside the current executable. On Windows, replacement
finishes after {cmd} exits.

Automatic checks run at most once every 24 hours. Set WLN_NO_UPDATE_CHECK=1 to
disable startup checks.

EXAMPLES
  {cmd} update --check
  {cmd} update`,

	"api": `{cmd} api — direct Remote API access

SUBCOMMANDS
  call SERVICE  Execute an authenticated service

Run '{cmd} help api call' for details.`,

	"api call": `{cmd} api call — execute an authenticated Remote API service

USAGE
  {cmd} api call SERVICE [--params JSON|@FILE] [--compact]

OPTIONS
  --params VALUE  JSON object/array or @FILE (default: {})
  --compact       Emit compact JSON instead of indented JSON

EXAMPLES
  {cmd} api call user/get_locale --params '{}'
  {cmd} api call core/search_items --params @request.json

Responses are JSON. Credential-like fields are recursively redacted. Login,
logout, and credential-management services are blocked or managed internally.`,
}
