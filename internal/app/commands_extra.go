package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Shooa/wln/internal/config"
	"github.com/Shooa/wln/internal/texttable"
	"github.com/Shooa/wln/internal/wialon"
)

type doctorCheck struct {
	Check   string `json:"check"`
	Status  string `json:"status"`
	Details string `json:"details"`
}

type flexibleDuration struct{ time.Duration }

func (d *flexibleDuration) String() string { return d.Duration.String() }

func (d *flexibleDuration) Set(value string) error {
	parsed, err := parseFlexibleDuration(value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func parseFlexibleDuration(value string) (time.Duration, error) {
	if len(value) > 1 {
		multiplier := time.Duration(0)
		switch value[len(value)-1] {
		case 'd':
			multiplier = 24 * time.Hour
		case 'w':
			multiplier = 7 * 24 * time.Hour
		}
		if multiplier != 0 {
			number, err := strconv.ParseFloat(value[:len(value)-1], 64)
			if err != nil || number < 0 {
				return 0, fmt.Errorf("invalid duration %q", value)
			}
			return time.Duration(number * float64(multiplier)), nil
		}
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", value, err)
	}
	return parsed, nil
}

func runUnitsStatus(ctx context.Context, args []string, opts options) error {
	unitRef := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		unitRef, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("units status", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	search := fs.String("search", "*", "Wialon unit name mask")
	offline := fs.Bool("offline", false, "show only disconnected units")
	var stale flexibleDuration
	fs.Var(&stale, "stale", "show units whose last position is at least this old, e.g. 30d")
	var inactive flexibleDuration
	fs.Var(&inactive, "inactive", "show units with no position or message newer than this, e.g. 30d")
	sortBy := fs.String("sort", "age", "sort by age or name")
	limit := fs.Int("limit", 0, "maximum rows; 0 means all")
	format := fs.String("format", "table", "table, json, or csv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if stale.Duration < 0 || inactive.Duration < 0 || *limit < 0 {
		return errors.New("--stale, --inactive, and --limit must not be negative")
	}
	if *sortBy != "age" && *sortBy != "name" {
		return errors.New("--sort must be age or name")
	}
	now := time.Now()
	return withClient(ctx, opts, func(client *wialon.Client) error {
		statuses, err := client.UnitStatuses(ctx, *search)
		if err != nil {
			return err
		}
		filtered := statuses[:0]
		for _, status := range statuses {
			if unitRef != "" && strconv.FormatInt(status.ID, 10) != unitRef && status.Name != unitRef && status.UniqueID != unitRef {
				continue
			}
			if *offline && status.Online {
				continue
			}
			if stale.Duration > 0 && status.Position.Time != 0 && now.Sub(time.Unix(status.Position.Time, 0)) < stale.Duration {
				continue
			}
			if inactive.Duration > 0 {
				latest := status.Position.Time
				if status.LastMessageTime > latest {
					latest = status.LastMessageTime
				}
				if latest != 0 && now.Sub(time.Unix(latest, 0)) < inactive.Duration {
					continue
				}
			}
			filtered = append(filtered, status)
		}
		if unitRef != "" && len(filtered) == 0 {
			return fmt.Errorf("unit %q not found or excluded by filters", unitRef)
		}
		if *sortBy == "name" {
			sort.Slice(filtered, func(i, j int) bool { return strings.ToLower(filtered[i].Name) < strings.ToLower(filtered[j].Name) })
		} else {
			sort.SliceStable(filtered, func(i, j int) bool {
				a, b := filtered[i].Position.Time, filtered[j].Position.Time
				if a == b {
					return strings.ToLower(filtered[i].Name) < strings.ToLower(filtered[j].Name)
				}
				if a == 0 {
					return true
				}
				if b == 0 {
					return false
				}
				return a < b
			})
		}
		if *limit > 0 && len(filtered) > *limit {
			filtered = filtered[:*limit]
		}
		return printUnitStatuses(filtered, *format, now, opts.stdout)
	})
}

func printUnitStatuses(statuses []wialon.UnitStatus, format string, now time.Time, out io.Writer) error {
	for i := range statuses {
		if statuses[i].Position.Time != 0 {
			age := now.Sub(time.Unix(statuses[i].Position.Time, 0))
			if age > 0 {
				statuses[i].PointAgeSeconds = int64(age / time.Second)
			}
		}
	}
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(statuses)
	}
	rows := make([][]string, 0, len(statuses))
	for _, status := range statuses {
		lastPoint, pointAge := "never", "never"
		if status.Position.Time != 0 {
			pointTime := time.Unix(status.Position.Time, 0).In(now.Location())
			lastPoint, pointAge = pointTime.Format(time.RFC3339), formatAge(now.Sub(pointTime))
		}
		lastMessage := "never"
		if status.LastMessageTime != 0 {
			lastMessage = time.Unix(status.LastMessageTime, 0).In(now.Location()).Format(time.RFC3339)
		}
		state := "offline"
		if status.Online {
			state = "online"
		}
		position := ""
		if status.Position.Time != 0 {
			position = fmt.Sprintf("%.6f,%.6f", status.Position.Latitude, status.Position.Longitude)
		}
		rows = append(rows, []string{strconv.FormatInt(status.ID, 10), status.Name, status.UniqueID, state, lastPoint, pointAge, lastMessage, position})
	}
	if format == "table" {
		return texttable.Write(out, []string{"ID", "NAME", "UNIQUE ID", "STATUS", "LAST POINT", "POINT AGE", "LAST MESSAGE", "POSITION"}, rows)
	}
	if format == "csv" {
		w := csv.NewWriter(out)
		if err := w.Write([]string{"id", "name", "unique_id", "status", "last_point", "point_age", "last_message", "position"}); err != nil {
			return err
		}
		for _, row := range rows {
			if err := w.Write(row); err != nil {
				return err
			}
		}
		w.Flush()
		return w.Error()
	}
	return fmt.Errorf("unsupported format %q", format)
}

func formatAge(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	days := int(duration / (24 * time.Hour))
	duration %= 24 * time.Hour
	hours := int(duration / time.Hour)
	minutes := int(duration % time.Hour / time.Minute)
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if minutes == 0 {
		return fmt.Sprintf("%ds", int(duration/time.Second))
	}
	return fmt.Sprintf("%dm", minutes)
}

func runMessagesTail(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 {
		return errors.New("usage: wln messages tail UNIT [-n COUNT] [--follow]")
	}
	unitRef, args := args[0], args[1:]
	fs := flag.NewFlagSet("messages tail", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	count := fs.Int("n", 20, "number of latest messages")
	follow := fs.Bool("follow", false, "poll for new messages until interrupted")
	poll := fs.Duration("poll", 2*time.Second, "poll interval for --follow")
	format := fs.String("format", "table", "table, json, or ndjson")
	allTypes := fs.Bool("all-types", false, "include non-telemetry messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *count < 1 || *count > 10000 {
		return errors.New("-n must be between 1 and 10000")
	}
	if *poll < 500*time.Millisecond {
		return errors.New("--poll must be at least 500ms")
	}
	if *follow && *format == "json" {
		return errors.New("--follow supports table or ndjson, not a finite JSON array")
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		fmt.Fprintf(opts.stderr, "Unit: %s (id=%d)\n", unit.Name, unit.ID)
		defer client.UnloadMessages(context.WithoutCancel(ctx))
		seen := make(map[string]bool)
		loadAndPrint := func() error {
			loaded, err := client.LoadLast(ctx, unit.ID, time.Now().Unix(), *count, *allTypes)
			if err != nil {
				return err
			}
			fresh := make([]map[string]any, 0, len(loaded.Messages))
			for _, message := range loaded.Messages {
				keyData, _ := json.Marshal(message)
				key := string(keyData)
				if !seen[key] {
					seen[key] = true
					fresh = append(fresh, message)
				}
			}
			if len(fresh) > 0 {
				return printTailMessages(fresh, *format, opts.stdout)
			}
			return nil
		}
		if err := loadAndPrint(); err != nil {
			return err
		}
		if !*follow {
			return nil
		}
		fmt.Fprintf(opts.stderr, "Following every %s; press Ctrl-C to stop.\n", *poll)
		ticker := time.NewTicker(*poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := loadAndPrint(); err != nil {
					return err
				}
			}
		}
	})
}

func printTailMessages(messages []map[string]any, format string, out io.Writer) error {
	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(messages)
	case "ndjson":
		enc := json.NewEncoder(out)
		for _, message := range messages {
			if err := enc.Encode(message); err != nil {
				return err
			}
		}
		return nil
	case "table":
		rows := make([][]string, 0, len(messages))
		for _, message := range messages {
			timestamp := int64Value(message["t"])
			formatted := ""
			if timestamp != 0 {
				formatted = time.Unix(timestamp, 0).Local().Format(time.RFC3339)
			}
			lat, lon, speed := "", "", ""
			if pos, ok := message["pos"].(map[string]any); ok {
				lat = numberText(pos["y"])
				lon = numberText(pos["x"])
				speed = numberText(pos["s"])
			}
			params := ""
			if p, ok := message["p"]; ok {
				data, _ := json.Marshal(p)
				params = string(data)
			}
			rows = append(rows, []string{formatted, fmt.Sprint(message["tp"]), lat, lon, speed, params})
		}
		return texttable.Write(out, []string{"TIME", "TYPE", "LAT", "LON", "SPEED", "PARAMETERS"}, rows)
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func int64Value(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func numberText(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

func runMessagesExport(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 {
		return errors.New("usage: wln messages export UNIT [interval] --format txt|kml|plt|wln|wlb")
	}
	unitRef, args := args[0], args[1:]
	fs := flag.NewFlagSet("messages export", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	fromText := fs.String("from", "", "interval start in RFC3339")
	toText := fs.String("to", "", "interval end in RFC3339")
	var last flexibleDuration
	fs.Var(&last, "last", "relative interval ending now, e.g. 2h or 7d")
	today := fs.Bool("today", false, "today through now")
	yesterday := fs.Bool("yesterday", false, "previous local calendar day")
	since := fs.String("since", "", "interval start as RFC3339 or local HH:MM")
	format := fs.String("format", "wln", "txt, kml, plt, wln, or wlb")
	compress := fs.Bool("compress", false, "request a compressed archive")
	output := fs.String("output", "", "output file path; - writes to stdout")
	force := fs.Bool("force", false, "replace an existing output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	allowed := map[string]bool{"txt": true, "kml": true, "plt": true, "wln": true, "wlb": true}
	*format = strings.ToLower(*format)
	if !allowed[*format] {
		return fmt.Errorf("unsupported native format %q", *format)
	}
	from, to, err := resolveMessageInterval(*fromText, *toText, last.Duration, *today, *yesterday, *since, time.Now())
	if err != nil {
		return err
	}
	if !to.After(from) {
		return errors.New("interval end must be after start")
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		outputPath := *output
		if outputPath == "" {
			identifier := unit.UniqueID
			if identifier == "" {
				identifier = strconv.FormatInt(unit.ID, 10)
			}
			ext := *format
			if *compress {
				ext = "zip"
			}
			outputPath = fmt.Sprintf("wialon-%s-%s.%s", safeFilenamePart(identifier), from.Format("2006-01-02"), ext)
		}
		fmt.Fprintf(opts.stderr, "Unit: %s (id=%d)\nInterval: %s — %s\n", unit.Name, unit.ID, from.Format(time.RFC3339), to.Format(time.RFC3339))
		data, _, err := client.Download(ctx, "exchange/export_messages", map[string]any{
			"itemId": unit.ID, "timeFrom": from.Unix(), "timeTo": to.Unix(), "format": *format, "compress": *compress,
		})
		if err != nil {
			return fmt.Errorf("export native messages: %w", err)
		}
		if outputPath == "-" {
			_, err = opts.stdout.Write(data)
			return err
		}
		abs, err := filepath.Abs(outputPath)
		if err != nil {
			return err
		}
		if err := writeAtomic(abs, data, *force); err != nil {
			return err
		}
		fmt.Fprintf(opts.stdout, "Exported %s to %s\n", *format, abs)
		return nil
	})
}

func writeAtomic(path string, data []byte, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("output %s already exists (use --force)", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wln-download-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func runDoctor(ctx context.Context, args []string, opts options) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(opts.stderr)
	format := fs.String("format", "table", "table or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		return err
	}
	name, profile, err := cfg.Resolve(opts.profile)
	if err != nil {
		return err
	}
	checks := []doctorCheck{{"config", "OK", opts.configPath}, {"profile", "OK", name}, {"server", "OK", profile.Server}}
	client, err := wialon.New(profile.Server, opts.timeout)
	if err != nil {
		return err
	}
	started := time.Now()
	if err := client.Login(ctx, profile.Token, profile.OperateAs); err != nil {
		checks = append(checks, doctorCheck{"login", "FAIL", err.Error()})
		_ = printChecks(checks, *format, opts.stdout)
		return err
	}
	defer client.Logout(context.WithoutCancel(ctx))
	checks = append(checks, doctorCheck{"login", "OK", fmt.Sprintf("%s", time.Since(started).Round(time.Millisecond))})
	info := client.SessionInfo()
	if info.UserName != "" {
		checks = append(checks, doctorCheck{"user", "OK", info.UserName})
	}
	if info.ServerTime != 0 {
		drift := time.Since(time.Unix(info.ServerTime, 0)).Round(time.Second)
		checks = append(checks, doctorCheck{"server time", "OK", fmt.Sprintf("%s (drift %s)", time.Unix(info.ServerTime, 0).Format(time.RFC3339), drift)})
	}
	units, unitErr := client.Units(ctx, "*")
	if unitErr != nil {
		checks = append(checks, doctorCheck{"units", "FAIL", unitErr.Error()})
	} else {
		checks = append(checks, doctorCheck{"units", "OK", fmt.Sprintf("%d accessible", len(units))})
	}
	return printChecks(checks, *format, opts.stdout)
}

func printChecks(checks []doctorCheck, format string, out io.Writer) error {
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(checks)
	}
	if format != "table" {
		return fmt.Errorf("unsupported format %q", format)
	}
	rows := make([][]string, 0, len(checks))
	for _, c := range checks {
		rows = append(rows, []string{c.Check, c.Status, c.Details})
	}
	return texttable.Write(out, []string{"CHECK", "STATUS", "DETAILS"}, rows)
}
