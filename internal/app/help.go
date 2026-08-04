package app

import (
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
		"messages get", "messages tail", "messages export", "api call":
		return args
	default:
		return nil
	}
}

func newCommandFlagSet(name, helpKey string, opts options) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	fs.Usage = func() { _ = printCommandHelp(opts.stderr, strings.Fields(helpKey)) }
	return fs
}

func commandError(opts options, helpKey, message string) error {
	_ = printCommandHelp(opts.stderr, strings.Fields(helpKey))
	return fmt.Errorf("%s", message)
}

func rejectUnexpectedArgs(fs *flag.FlagSet, opts options, helpKey string) error {
	if fs.NArg() == 0 {
		return nil
	}
	return commandError(opts, helpKey, fmt.Sprintf("unexpected positional arguments: %s", strings.Join(fs.Args(), " ")))
}

func printCommandHelp(w io.Writer, path []string) error {
	key := strings.Join(path, " ")
	if len(path) == 1 && path[0] == "--all" {
		return printAllHelp(w)
	}
	help, ok := helpSections[key]
	if !ok {
		return fmt.Errorf("unknown help topic %q; run 'wln help' to list topics", key)
	}
	_, err := fmt.Fprintln(w, strings.TrimSpace(help))
	return err
}

func printAllHelp(w io.Writer) error {
	order := []string{"", "profile", "profile login", "profile add", "profile list", "profile use", "profile remove", "profile check", "units", "units list", "units status", "messages", "messages get", "messages tail", "messages export", "doctor", "api", "api call", "update"}
	for i, key := range order {
		if i > 0 {
			if _, err := fmt.Fprintln(w, "\n---"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, strings.TrimSpace(helpSections[key])); err != nil {
			return err
		}
	}
	return nil
}

var helpSections = map[string]string{
	"": `wln — export and inspect data from Wialon Hosting or Wialon Local

USAGE
  wln [GLOBAL OPTIONS] COMMAND [SUBCOMMAND] [ARGUMENTS] [OPTIONS]

COMMANDS
  profile   Log in and manage saved server profiles
  units     List units and inspect connection/position status
  messages  Export, tail, or download messages
  doctor    Validate the selected profile and API access
  api       Call a Remote API service directly
  update    Check for and install the latest wln release

GLOBAL OPTIONS
  --profile NAME      Override the configured default profile
  --config PATH       Override the configuration file
  --timeout DURATION  HTTP timeout (default: 2m)
  --width N           Override detected terminal width for tables
  --wide              Do not fit tables to the terminal width
  --version           Print the wln version

HELP
  wln help COMMAND [SUBCOMMAND]
  wln COMMAND [SUBCOMMAND] --help
  wln help --all

CONVENTIONS
  UNIT is an exact Wialon ID, exact unit name, or exact unique ID/IMEI.
  Interval/age durations also accept d (days) and w (weeks): 2h, 7d, 2w.
  Other durations use Go units: 500ms, 30s, 5m, 24h.
  Progress and diagnostics go to stderr. Structured data goes to stdout.
  Existing files are not replaced unless --force is supplied.`,

	"profile": `wln profile — manage authentication profiles

SUBCOMMANDS
  login NAME   Open Wialon login in a browser and save the issued token
  add NAME     Save a token supplied via WLN_TOKEN or stdin
  list         List profiles without displaying tokens
  use NAME     Select the default profile
  remove NAME  Remove a saved profile
  check [NAME] Diagnose a profile

Run 'wln help profile SUBCOMMAND' for details.`,

	"profile login": `wln profile login — browser-based Wialon authorization

USAGE
  wln profile login NAME --server BASE_URL [OPTIONS]

REQUIRED
  NAME               Local profile name
  --server BASE_URL  Wialon installation URL containing login.html

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

EXAMPLE
  wln profile login hosting --server https://hosting.wialon.com --default`,

	"profile add": `wln profile add — save an existing access token

USAGE
  WLN_TOKEN=... wln profile add NAME [OPTIONS]
  command-producing-token | wln profile add NAME --token-stdin [OPTIONS]

OPTIONS
  --server URL       Remote API server (default: https://hst-api.wialon.com)
  --operate-as USER  Open sessions as a subuser
  --token-stdin      Read the token from stdin
  --default          Make this the default profile
  --allow-http       Permit HTTP for a trusted Wialon Local server

Tokens are never accepted as command-line arguments or printed.`,

	"profile list": `wln profile list — list saved profiles

USAGE
  wln profile list [--format table|json]

Tokens and session IDs are never included in the output.`,
	"profile use": `wln profile use — select the default profile

USAGE
  wln profile use NAME`,
	"profile remove": `wln profile remove — remove a saved profile

USAGE
  wln profile remove NAME`,
	"profile check": `wln profile check — diagnose a profile

USAGE
  wln profile check [NAME]

Equivalent to 'wln doctor' with the selected profile.`,

	"units": `wln units — inspect accessible Wialon units

SUBCOMMANDS
  list    List identity and hardware information
  status  Show connectivity, last position, point age, and last message

Run 'wln help units SUBCOMMAND' for details.`,

	"units list": `wln units list — list accessible units

USAGE
  wln units list [--search MASK] [--format table|json|csv]

OPTIONS
  --search MASK   Wialon unit-name mask (default: *)
  --format VALUE  table, json, or csv (default: table)

EXAMPLES
  wln units list
  wln units list --search 'Truck*' --format json`,

	"units status": `wln units status — inspect last activity and position age

USAGE
  wln units status [UNIT] [OPTIONS]

OPTIONS
  --search MASK       Wialon unit-name mask (default: *)
  --offline           Show only disconnected units
  --stale DURATION    Require the last coordinate to be this old
  --inactive DURATION Require both position and message activity to be this old
  --sort age|name     Sort oldest positions first or by name (default: age)
  --limit N           Maximum rows; 0 means all
  --format VALUE      table, json, or csv (default: table)

EXAMPLES
  wln units status 1001
  wln units status --offline --inactive 30d --sort age

Use --inactive rather than --stale when selecting an unused unit: a unit may
have no recent GPS point while still sending current non-position messages.`,

	"messages": `wln messages — retrieve Wialon messages

SUBCOMMANDS
  get     Export messages to CSV, JSON, or NDJSON
  tail    Print recent messages and optionally follow new ones
  export  Download a native Wialon format

Run 'wln help messages SUBCOMMAND' for details.`,

	"messages get": `wln messages get — export an interval of messages

USAGE
  wln messages get UNIT [INTERVAL] [OPTIONS]

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
  wln messages get 1001
  wln messages get 1001 --last 2h --format ndjson --output -
  wln messages get 1001 --yesterday --params temperature,voltage

The resolved RFC3339 command is printed to stderr before export.`,

	"messages tail": `wln messages tail — print recent messages

USAGE
  wln messages tail UNIT [OPTIONS]

OPTIONS
  -n N                Number of recent messages (default: 20, max: 10000)
  --follow            Poll until interrupted
  --poll DURATION     Follow polling interval (default: 2s, minimum: 500ms)
  --format VALUE      table, json, or ndjson (default: table)
  --max-params N      Max parameter characters in table output (default: 100)
  --full-params       Do not shorten table parameters
  --all-types         Include non-telemetry message types

EXAMPLES
  wln messages tail 1001 -n 10
  wln messages tail 1001 --follow --format ndjson
  wln messages tail 1001 --max-params 60`,

	"messages export": `wln messages export — download a native Wialon file

USAGE
  wln messages export UNIT [INTERVAL] --format kml|plt|wln|wlb [OPTIONS]

INTERVAL
  --from, --to, --last, --today, --yesterday, and --since work as in
  'wln messages get'. The default interval is today through now.

OPTIONS
  --format VALUE  kml, plt, wln, or wlb (default: wln)
  --compress      Request a ZIP archive
  --output PATH   Output file; '-' writes bytes to stdout
  --force         Replace an existing file

EXAMPLES
  wln messages export 1001 --today --format wln
  wln messages export 1001 --last 24h --format kml
  wln messages export 1001 --yesterday --format wlb --compress`,

	"doctor": `wln doctor — validate configuration and API access

USAGE
  wln [--profile NAME] doctor [--format table|json]

Checks the selected profile, server, login latency, authenticated user, server
time drift, and accessible unit count. Tokens and session IDs are not shown.`,

	"update": `wln update — update the current executable

USAGE
  wln update [--check]

OPTIONS
  --check  Check GitHub Releases without installing the update

The downloaded archive is verified against the release SHA256SUMS before the
executable is replaced. On Windows, replacement finishes after wln exits.

Automatic checks run at most once every 24 hours. Set WLN_NO_UPDATE_CHECK=1 to
disable startup checks.

EXAMPLES
  wln update --check
  wln update`,

	"api": `wln api — direct Remote API access

SUBCOMMANDS
  call SERVICE  Execute an authenticated service

Run 'wln help api call' for details.`,

	"api call": `wln api call — execute an authenticated Remote API service

USAGE
  wln api call SERVICE [--params JSON|@FILE] [--compact]

OPTIONS
  --params VALUE  JSON object/array or @FILE (default: {})
  --compact       Emit compact JSON instead of indented JSON

EXAMPLES
  wln api call user/get_locale --params '{}'
  wln api call core/search_items --params @request.json

Responses are JSON. Credential-like fields are recursively redacted. Login,
logout, and credential-management services are blocked or managed internally.`,
}
