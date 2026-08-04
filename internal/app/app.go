package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Shooa/wln/internal/browseropen"
	"github.com/Shooa/wln/internal/config"
	"github.com/Shooa/wln/internal/exportcsv"
	"github.com/Shooa/wln/internal/oauthflow"
	"github.com/Shooa/wln/internal/texttable"
	"github.com/Shooa/wln/internal/wialon"
)

var Version = "0.5.1"

var openBrowser = browseropen.Open

type options struct {
	configPath string
	profile    string
	timeout    time.Duration
	stdout     io.Writer
	stderr     io.Writer
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return printCommandHelp(stdout, nil)
	}
	if len(args) > 0 && args[0] == "help" {
		return printCommandHelp(stdout, args[1:])
	}
	defaultConfig, err := config.DefaultPath()
	if err != nil {
		return err
	}
	global := flag.NewFlagSet("wln", flag.ContinueOnError)
	global.SetOutput(stderr)
	configPath := global.String("config", defaultConfig, "configuration file")
	profile := global.String("profile", "", "profile name (default: configured default)")
	timeout := global.Duration("timeout", 2*time.Minute, "HTTP request timeout")
	version := global.Bool("version", false, "print version")
	global.Usage = func() { printUsage(stderr) }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *version {
		fmt.Fprintf(stdout, "wln %s\n", Version)
		return nil
	}
	rest := global.Args()
	if len(rest) == 0 {
		printUsage(stderr)
		return errors.New("command is required")
	}
	if rest[0] == "help" {
		return printCommandHelp(stdout, rest[1:])
	}
	for i, arg := range rest {
		if arg == "-h" || arg == "--help" {
			return printCommandHelp(stdout, commandHelpPath(rest[:i]))
		}
	}
	opts := options{configPath: *configPath, profile: *profile, timeout: *timeout, stdout: stdout, stderr: stderr}
	switch rest[0] {
	case "profile":
		return runProfile(ctx, rest[1:], opts)
	case "units":
		return runUnits(ctx, rest[1:], opts)
	case "messages":
		return runMessages(ctx, rest[1:], opts)
	case "doctor":
		return runDoctor(ctx, rest[1:], opts)
	case "api":
		return runAPI(ctx, rest[1:], opts)
	default:
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func printUsage(w io.Writer) {
	_ = printCommandHelp(w, nil)
}

func runProfile(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 {
		return errors.New("profile command is required: list, add, login, use, remove, or check")
	}
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("profile list", flag.ContinueOnError)
		fs.SetOutput(opts.stderr)
		format := fs.String("format", "table", "table or json")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return printProfiles(cfg, *format, opts.stdout)
	case "add":
		if len(args) < 2 {
			return errors.New("profile name is required")
		}
		name := args[1]
		if err := validateProfileName(name); err != nil {
			return err
		}
		fs := flag.NewFlagSet("profile add", flag.ContinueOnError)
		fs.SetOutput(opts.stderr)
		server := fs.String("server", config.DefaultServer(), "Wialon server base URL")
		operateAs := fs.String("operate-as", "", "optional subuser name")
		tokenStdin := fs.Bool("token-stdin", false, "read token from standard input")
		makeDefault := fs.Bool("default", false, "make this the default profile")
		allowHTTP := fs.Bool("allow-http", false, "allow an unencrypted HTTP server (Wialon Local only)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		token, err := profileToken(*tokenStdin)
		if err != nil {
			return err
		}
		if err := validateServer(*server, *allowHTTP); err != nil {
			return err
		}
		cfg.Profiles[name] = config.Profile{Server: strings.TrimRight(*server, "/"), Token: token, OperateAs: *operateAs}
		if cfg.DefaultProfile == "" || *makeDefault {
			cfg.DefaultProfile = name
		}
		if err := cfg.Save(opts.configPath); err != nil {
			return err
		}
		fmt.Fprintf(opts.stdout, "Profile %q saved; token was not printed.\n", name)
		return nil
	case "login":
		if len(args) < 2 {
			return errors.New("profile name is required")
		}
		name := args[1]
		if err := validateProfileName(name); err != nil {
			return err
		}
		fs := flag.NewFlagSet("profile login", flag.ContinueOnError)
		fs.SetOutput(opts.stderr)
		server := fs.String("server", "", "base URL of the Wialon installation")
		operateAs := fs.String("operate-as", "", "optional subuser name used by API sessions")
		user := fs.String("user", "", "pre-fill the Wialon login name")
		language := fs.String("lang", "ru", "Wialon login page language")
		access := fs.Int64("access", oauthflow.DefaultAccess, "token access flags in decimal")
		duration := fs.Duration("duration", 0, "token lifetime; 0 means unlimited")
		callbackTimeout := fs.Duration("callback-timeout", 5*time.Minute, "time to wait for browser authorization")
		makeDefault := fs.Bool("default", false, "make this the default profile")
		allowHTTP := fs.Bool("allow-http", false, "allow an unencrypted HTTP server (trusted Wialon Local only)")
		noOpen := fs.Bool("no-open", false, "print the login URL without opening a browser")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *server == "" {
			return errors.New("--server is required and must be the base URL of the Wialon installation")
		}
		if err := validateServer(*server, *allowHTTP); err != nil {
			return err
		}
		if *access <= 0 {
			return errors.New("--access must be a positive decimal token flag combination")
		}
		if *callbackTimeout <= 0 {
			return errors.New("--callback-timeout must be positive")
		}
		openLogin := func(target string) error {
			fmt.Fprintf(opts.stderr, "Wialon login URL: %s\n", target)
			if *noOpen {
				fmt.Fprintln(opts.stderr, "Open the URL manually; waiting for the local callback...")
				return nil
			}
			if err := openBrowser(target); err != nil {
				fmt.Fprintf(opts.stderr, "Could not open the browser automatically: %v\nOpen the URL manually; waiting for the local callback...\n", err)
			}
			return nil
		}
		result, err := oauthflow.Authorize(ctx, oauthflow.Options{
			BaseURL: *server, ClientID: "wln", Access: *access,
			Duration: *duration, Language: *language, User: *user,
			CallbackLimit: *callbackTimeout,
		}, openLogin)
		if err != nil {
			return err
		}
		if err := validateServer(result.SDKURL, *allowHTTP); err != nil {
			return fmt.Errorf("Wialon callback API URL: %w", err)
		}
		client, err := wialon.New(result.SDKURL, opts.timeout)
		if err != nil {
			return err
		}
		if err := client.Login(ctx, result.Token, *operateAs); err != nil {
			return fmt.Errorf("validate issued token: %w", err)
		}
		if err := client.Logout(context.WithoutCancel(ctx)); err != nil {
			return fmt.Errorf("validate issued token logout: %w", err)
		}
		cfg.Profiles[name] = config.Profile{Server: strings.TrimRight(result.SDKURL, "/"), Token: result.Token, OperateAs: *operateAs}
		if cfg.DefaultProfile == "" || *makeDefault {
			cfg.DefaultProfile = name
		}
		if err := cfg.Save(opts.configPath); err != nil {
			return err
		}
		if result.UserName != "" {
			fmt.Fprintf(opts.stdout, "Profile %q saved for Wialon user %q.\n", name, result.UserName)
		} else {
			fmt.Fprintf(opts.stdout, "Profile %q saved.\n", name)
		}
		fmt.Fprintf(opts.stdout, "API server: %s\n", result.SDKURL)
		return nil
	case "use":
		if len(args) != 2 {
			return errors.New("usage: wln profile use NAME")
		}
		if _, ok := cfg.Profiles[args[1]]; !ok {
			return fmt.Errorf("profile %q not found", args[1])
		}
		cfg.DefaultProfile = args[1]
		if err := cfg.Save(opts.configPath); err != nil {
			return err
		}
		fmt.Fprintf(opts.stdout, "Default profile: %s\n", args[1])
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: wln profile remove NAME")
		}
		if _, ok := cfg.Profiles[args[1]]; !ok {
			return fmt.Errorf("profile %q not found", args[1])
		}
		delete(cfg.Profiles, args[1])
		if cfg.DefaultProfile == args[1] {
			cfg.DefaultProfile = ""
			if names := cfg.Names(); len(names) > 0 {
				cfg.DefaultProfile = names[0]
			}
		}
		if err := cfg.Save(opts.configPath); err != nil {
			return err
		}
		fmt.Fprintf(opts.stdout, "Profile %q removed.\n", args[1])
		return nil
	case "check":
		if len(args) > 2 {
			return errors.New("usage: wln profile check [NAME]")
		}
		if len(args) == 2 {
			opts.profile = args[1]
		}
		return runDoctor(ctx, nil, opts)
	default:
		return fmt.Errorf("unknown profile command %q", args[0])
	}
}

func profileToken(readStdin bool) (string, error) {
	if token := strings.TrimSpace(os.Getenv("WLN_TOKEN")); token != "" {
		return token, nil
	}
	if !readStdin {
		return "", errors.New("token is required: set WLN_TOKEN or pass --token-stdin (the token is never accepted as a command-line argument)")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 16*1024))
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("empty token")
	}
	return token, nil
}

func validateProfileName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n/") {
		return fmt.Errorf("invalid profile name %q", name)
	}
	return nil
}

func validateServer(server string, allowHTTP bool) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid server URL %q", server)
	}
	if u.Scheme == "http" && !allowHTTP {
		return errors.New("HTTP exposes the Wialon token; use HTTPS or explicitly pass --allow-http for a trusted Wialon Local server")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("server URL must use HTTPS, got %q", u.Scheme)
	}
	if u.User != nil {
		return errors.New("server URL must not contain credentials")
	}
	return nil
}

func printProfiles(cfg *config.File, format string, out io.Writer) error {
	type publicProfile struct {
		Name      string `json:"name"`
		Default   bool   `json:"default"`
		Server    string `json:"server"`
		OperateAs string `json:"operate_as,omitempty"`
	}
	profiles := make([]publicProfile, 0, len(cfg.Profiles))
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		profiles = append(profiles, publicProfile{Name: name, Default: name == cfg.DefaultProfile, Server: p.Server, OperateAs: p.OperateAs})
	}
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(profiles)
	case "table":
		rows := make([][]string, 0, len(profiles))
		for _, p := range profiles {
			rows = append(rows, []string{p.Name, strconv.FormatBool(p.Default), p.Server, p.OperateAs})
		}
		return texttable.Write(out, []string{"NAME", "DEFAULT", "SERVER", "OPERATE AS"}, rows)
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func runUnits(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 {
		return errors.New("usage: wln units list|status")
	}
	switch args[0] {
	case "list":
		return runUnitsList(ctx, args[1:], opts)
	case "status":
		return runUnitsStatus(ctx, args[1:], opts)
	default:
		return fmt.Errorf("unknown units command %q", args[0])
	}
}

func runUnitsList(ctx context.Context, args []string, opts options) error {
	fs := flag.NewFlagSet("units list", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	search := fs.String("search", "*", "Wialon unit name mask")
	format := fs.String("format", "table", "table, json, or csv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		units, err := client.Units(ctx, *search)
		if err != nil {
			return err
		}
		hardwareIDs := make([]int64, 0, len(units))
		for _, unit := range units {
			hardwareIDs = append(hardwareIDs, unit.HardwareID)
		}
		hardwareTypes, err := client.HardwareTypes(ctx, hardwareIDs)
		if err != nil {
			fmt.Fprintf(opts.stderr, "Warning: %v; showing hardware IDs as fallback.\n", err)
		}
		for i := range units {
			if hardware, ok := hardwareTypes[units[i].HardwareID]; ok && hardware.Name != "" {
				units[i].Hardware = hardware.Name
			} else if units[i].HardwareID != 0 {
				units[i].Hardware = fmt.Sprintf("Unknown (#%d)", units[i].HardwareID)
			}
		}
		return printUnits(units, *format, opts.stdout)
	})
}

func printUnits(units []wialon.Unit, format string, out io.Writer) error {
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(units)
	case "csv":
		w := csv.NewWriter(out)
		if err := w.Write([]string{"id", "name", "unique_id", "unique_id2", "hardware", "hardware_id"}); err != nil {
			return err
		}
		for _, unit := range units {
			if err := w.Write([]string{strconv.FormatInt(unit.ID, 10), unit.Name, unit.UniqueID, unit.UniqueID2, unit.Hardware, strconv.FormatInt(unit.HardwareID, 10)}); err != nil {
				return err
			}
		}
		w.Flush()
		return w.Error()
	case "table":
		rows := make([][]string, 0, len(units))
		for _, unit := range units {
			rows = append(rows, []string{strconv.FormatInt(unit.ID, 10), unit.Name, unit.UniqueID, unit.Hardware})
		}
		return texttable.Write(out, []string{"ID", "NAME", "UNIQUE ID", "HARDWARE"}, rows)
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func runMessages(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 {
		return errors.New("usage: wln messages get|tail|export UNIT [options]")
	}
	switch args[0] {
	case "get":
		return runMessagesGet(ctx, args, opts)
	case "tail":
		return runMessagesTail(ctx, args[1:], opts)
	case "export":
		return runMessagesExport(ctx, args[1:], opts)
	default:
		return fmt.Errorf("unknown messages command %q", args[0])
	}
}

func runMessagesGet(ctx context.Context, args []string, opts options) error {
	if len(args) < 2 {
		return errors.New("usage: wln messages get UNIT [--from RFC3339] [--to RFC3339] [--output FILE]")
	}
	unitRef := args[1]
	fs := flag.NewFlagSet("messages get", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	fromText := fs.String("from", "", "interval start in RFC3339 with an explicit offset (default: today at 00:00 local time)")
	toText := fs.String("to", "", "interval end in RFC3339 with an explicit offset (default: now)")
	var last flexibleDuration
	fs.Var(&last, "last", "relative interval ending now, for example 2h or 7d")
	today := fs.Bool("today", false, "today from local midnight through now")
	yesterday := fs.Bool("yesterday", false, "previous local calendar day")
	since := fs.String("since", "", "interval start as RFC3339 or local HH:MM")
	batchSize := fs.Int("batch-size", 10000, "messages per API response")
	output := fs.String("output", "", "output path; - writes data to stdout")
	format := fs.String("format", "csv", "csv, json, or ndjson")
	paramsFilter := fs.String("params", "", "comma-separated message parameters to retain")
	allTypes := fs.Bool("all-types", false, "include non-telemetry messages")
	force := fs.Bool("force", false, "replace an existing output file")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	from, to, err := resolveMessageInterval(*fromText, *toText, last.Duration, *today, *yesterday, *since, time.Now())
	if err != nil {
		return err
	}
	if !to.After(from) {
		return errors.New("--to must be after --from")
	}
	if *batchSize < 1 || *batchSize > 100000 {
		return errors.New("--batch-size must be between 1 and 100000")
	}
	*format = strings.ToLower(*format)
	if *format != "csv" && *format != "json" && *format != "ndjson" {
		return errors.New("--format must be csv, json, or ndjson")
	}

	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		outputPath := *output
		if outputPath == "" {
			outputPath = defaultMessageOutputFormat(unit, from, *format)
		}
		absOutput := outputPath
		if outputPath != "-" {
			absOutput, err = filepath.Abs(outputPath)
			if err != nil {
				return fmt.Errorf("resolve output path: %w", err)
			}
		}
		fmt.Fprintf(opts.stderr, "Unit: %s (id=%d, unique_id=%s)\n", unit.Name, unit.ID, unit.UniqueID)
		fmt.Fprintf(opts.stderr, "Interval: %s — %s\n", from.Format(time.RFC3339), to.Format(time.RFC3339))
		printMessagesCommand(opts.stderr, unitRef, from, to, outputPath, *format, *paramsFilter, *batchSize, *allTypes, *force)

		spool, err := exportcsv.NewSpool()
		if err != nil {
			return err
		}
		defer spool.Close()

		loaded, err := client.LoadMessages(ctx, unit.ID, from.Unix(), to.Unix(), *batchSize, *allTypes)
		if err != nil {
			var apiErr *wialon.APIError
			if errors.As(err, &apiErr) && apiErr.Code == 1001 {
				return fmt.Errorf("no messages for unit %s in the selected interval", unit.Name)
			}
			return err
		}
		defer client.UnloadMessages(context.WithoutCancel(ctx))
		if loaded.Count == 0 {
			return fmt.Errorf("no messages for unit %s in the selected interval", unit.Name)
		}
		if len(loaded.Messages) > loaded.Count {
			return fmt.Errorf("invalid Wialon response: received %d initial messages for count %d", len(loaded.Messages), loaded.Count)
		}
		for _, message := range loaded.Messages {
			message = filterMessageParams(message, *paramsFilter)
			if err := spool.Add(message); err != nil {
				return err
			}
		}
		for index := len(loaded.Messages); index < loaded.Count; {
			end := index + *batchSize
			if end > loaded.Count {
				end = loaded.Count
			}
			messages, err := client.GetMessages(ctx, index, end)
			if err != nil {
				return err
			}
			if len(messages) == 0 {
				return fmt.Errorf("Wialon returned an empty batch at index %d of %d", index, loaded.Count)
			}
			for _, message := range messages {
				message = filterMessageParams(message, *paramsFilter)
				if err := spool.Add(message); err != nil {
					return err
				}
			}
			index += len(messages)
			fmt.Fprintf(opts.stderr, "Fetched %d/%d messages\r", index, loaded.Count)
		}
		if loaded.Count > len(loaded.Messages) {
			fmt.Fprintln(opts.stderr)
		}
		if spool.Rows() != loaded.Count {
			return fmt.Errorf("row count mismatch: Wialon reported %d, fetched %d", loaded.Count, spool.Rows())
		}
		if outputPath == "-" {
			if err := spool.WriteTo(opts.stdout, *format); err != nil {
				return err
			}
			fmt.Fprintf(opts.stderr, "Exported %d messages to stdout.\n", spool.Rows())
		} else {
			if err := spool.Write(absOutput, *force, *format); err != nil {
				return err
			}
			fmt.Fprintf(opts.stdout, "Exported %d messages to %s\n", spool.Rows(), absOutput)
		}
		return nil
	})
}

func messageInterval(fromText, toText string, now time.Time) (time.Time, time.Time, error) {
	return resolveMessageInterval(fromText, toText, 0, false, false, "", now)
}

func resolveMessageInterval(fromText, toText string, last time.Duration, today, yesterday bool, since string, now time.Time) (time.Time, time.Time, error) {
	shortcuts := 0
	if last != 0 {
		shortcuts++
	}
	if today {
		shortcuts++
	}
	if yesterday {
		shortcuts++
	}
	if since != "" {
		shortcuts++
	}
	if shortcuts > 1 || (shortcuts > 0 && fromText != "") {
		return time.Time{}, time.Time{}, errors.New("use only one of --from, --last, --today, --yesterday, or --since")
	}
	if (today || yesterday) && toText != "" {
		return time.Time{}, time.Time{}, errors.New("--today and --yesterday cannot be combined with --to")
	}
	if last < 0 {
		return time.Time{}, time.Time{}, errors.New("--last must be positive")
	}
	to := now
	var err error
	if toText != "" {
		to, err = parseTime(toText)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--to: %w", err)
		}
	}
	from := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, to.Location())
	if last > 0 {
		from = to.Add(-last)
	}
	if yesterday {
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		from = to.AddDate(0, 0, -1)
	}
	if today {
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		to = now
	}
	if since != "" {
		if parsed, parseErr := time.ParseInLocation("15:04", since, now.Location()); parseErr == nil {
			from = time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, now.Location())
		} else {
			from, err = parseTime(since)
			if err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("--since: expected HH:MM or RFC3339: %w", err)
			}
		}
	}
	if fromText != "" {
		from, err = parseTime(fromText)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--from: %w", err)
		}
	}
	return from, to, nil
}

func defaultMessageOutput(unit wialon.Unit, from time.Time) string {
	return defaultMessageOutputFormat(unit, from, "csv")
}

func defaultMessageOutputFormat(unit wialon.Unit, from time.Time, format string) string {
	identifier := unit.UniqueID
	if identifier == "" {
		identifier = strconv.FormatInt(unit.ID, 10)
	}
	return fmt.Sprintf("wialon-%s-%s.%s", safeFilenamePart(identifier), from.Format("2006-01-02"), format)
}

func safeFilenamePart(value string) string {
	var result strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			result.WriteRune(r)
		default:
			result.WriteByte('_')
		}
	}
	return result.String()
}

func printMessagesCommand(w io.Writer, unitRef string, from, to time.Time, output, format, params string, batchSize int, allTypes, force bool) {
	fmt.Fprintln(w, "Repeat command:")
	fmt.Fprintf(w, "  wln messages get %s \\\n", shellQuote(unitRef))
	fmt.Fprintf(w, "    --from %s \\\n", shellQuote(from.Format(time.RFC3339)))
	fmt.Fprintf(w, "    --to %s \\\n", shellQuote(to.Format(time.RFC3339)))
	fmt.Fprintf(w, "    --batch-size %d \\\n", batchSize)
	fmt.Fprintf(w, "    --format %s \\\n", shellQuote(format))
	if params != "" {
		fmt.Fprintf(w, "    --params %s \\\n", shellQuote(params))
	}
	fmt.Fprintf(w, "    --output %s", shellQuote(output))
	if allTypes {
		fmt.Fprint(w, " \\\n    --all-types")
	}
	if force {
		fmt.Fprint(w, " \\\n    --force")
	}
	fmt.Fprintln(w)
}

func filterMessageParams(message map[string]any, filter string) map[string]any {
	if strings.TrimSpace(filter) == "" {
		return message
	}
	wanted := make(map[string]bool)
	for _, name := range strings.Split(filter, ",") {
		name = strings.TrimSpace(strings.TrimPrefix(name, "p."))
		if name != "" {
			wanted[name] = true
		}
	}
	copyMessage := make(map[string]any, len(message))
	for key, value := range message {
		copyMessage[key] = value
	}
	if params, ok := message["p"].(map[string]any); ok {
		selected := make(map[string]any)
		for name, value := range params {
			if wanted[name] {
				selected[name] = value
			}
		}
		copyMessage["p"] = selected
	}
	return copyMessage
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("value is required")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected RFC3339 timestamp with offset, for example 2026-08-03T10:00:00+05:00: %w", err)
	}
	return t, nil
}

func resolveUnit(ctx context.Context, client *wialon.Client, ref string) (wialon.Unit, error) {
	units, err := client.Units(ctx, "*")
	if err != nil {
		return wialon.Unit{}, err
	}
	matches := make([]wialon.Unit, 0, 1)
	for _, unit := range units {
		if strconv.FormatInt(unit.ID, 10) == ref || unit.Name == ref || unit.UniqueID == ref || (unit.UniqueID2 != "" && unit.UniqueID2 == ref) {
			matches = append(matches, unit)
		}
	}
	if len(matches) == 0 {
		return wialon.Unit{}, fmt.Errorf("unit %q not found by exact ID, name, or unique ID", ref)
	}
	if len(matches) > 1 {
		return wialon.Unit{}, fmt.Errorf("unit reference %q is ambiguous (%d exact matches)", ref, len(matches))
	}
	return matches[0], nil
}

func runAPI(ctx context.Context, args []string, opts options) error {
	if len(args) < 2 || args[0] != "call" {
		return errors.New("usage: wln api call SERVICE [--params JSON|@FILE]")
	}
	service := args[1]
	if strings.HasPrefix(service, "token/") || service == "core/logout" || service == "core/create_auth_hash" {
		return fmt.Errorf("service %q is blocked because it can expose credentials or is managed internally by wln", service)
	}
	fs := flag.NewFlagSet("api call", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	paramsText := fs.String("params", "{}", "JSON object/array or @file")
	compact := fs.Bool("compact", false, "emit compact JSON")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	data := []byte(*paramsText)
	if strings.HasPrefix(*paramsText, "@") {
		path := strings.TrimPrefix(*paramsText, "@")
		if path == "" {
			return errors.New("empty @file parameter")
		}
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read params file: %w", err)
		}
	}
	var params any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		return fmt.Errorf("parse --params JSON: %w", err)
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		raw, err := client.CallRaw(ctx, service, params)
		if err != nil {
			return err
		}
		raw, err = sanitizeJSON(raw)
		if err != nil {
			return fmt.Errorf("sanitize API response: %w", err)
		}
		if *compact {
			_, err = fmt.Fprintln(opts.stdout, string(raw))
			return err
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			return fmt.Errorf("format API response: %w", err)
		}
		_, err = fmt.Fprintln(opts.stdout, pretty.String())
		return err
	})
}

func sanitizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	sanitizeValue(value)
	return json.Marshal(value)
}

func sanitizeValue(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch strings.ToLower(key) {
			case "token", "access_token", "sid", "eid", "gis_sid", "th", "password", "passw", "psw", "authorization":
				v[key] = "<redacted>"
			default:
				sanitizeValue(child)
			}
		}
	case []any:
		for _, child := range v {
			sanitizeValue(child)
		}
	}
}

func withClient(ctx context.Context, opts options, fn func(*wialon.Client) error) error {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	_, profile, err := cfg.Resolve(opts.profile)
	if err != nil {
		return err
	}
	client, err := wialon.New(profile.Server, opts.timeout)
	if err != nil {
		return err
	}
	if err := client.Login(ctx, profile.Token, profile.OperateAs); err != nil {
		return err
	}
	defer client.Logout(context.WithoutCancel(ctx))
	return fn(client)
}
