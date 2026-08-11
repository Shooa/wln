package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
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

type optionalString struct {
	value string
	set   bool
}

func (s *optionalString) String() string { return s.value }

func (s *optionalString) Set(value string) error {
	s.value = value
	s.set = true
	return nil
}

type unitConnection struct {
	UnitID        int64  `json:"unit_id"`
	Name          string `json:"name"`
	UniqueID      string `json:"unique_id"`
	UniqueID2     string `json:"unique_id2,omitempty"`
	DeviceType    string `json:"device_type"`
	DeviceTypeID  int64  `json:"device_type_id"`
	ServerAddress string `json:"server_address"`
	TCPPort       string `json:"tcp_port,omitempty"`
	UDPPort       string `json:"udp_port,omitempty"`
}

var unitConnectionJSONFields = []string{"unit_id", "name", "unique_id", "unique_id2", "device_type", "device_type_id", "server_address", "tcp_port", "udp_port"}

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

func runUnitsDeviceTypes(ctx context.Context, args []string, opts options) error {
	fs := newCommandFlagSet("units device-types", "units device-types", opts)
	search := fs.String("search", "*", "case-insensitive device type name substring")
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table, json, or csv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "units device-types"); err != nil {
		return err
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		types, err := client.DeviceTypes(ctx)
		if err != nil {
			return err
		}
		needle := strings.ToLower(strings.TrimSpace(*search))
		filtered := types[:0]
		for _, hardware := range types {
			if needle == "" || needle == "*" || strings.Contains(strings.ToLower(hardware.Name), needle) {
				filtered = append(filtered, hardware)
			}
		}
		sort.Slice(filtered, func(i, j int) bool {
			return strings.ToLower(filtered[i].Name) < strings.ToLower(filtered[j].Name)
		})
		return printDeviceTypes(filtered, strings.ToLower(*format), opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
	})
}

func printDeviceTypes(types []wialon.HardwareType, format string, compact bool, out, notice io.Writer, tableWidth int) error {
	switch format {
	case "json":
		return writeJSON(out, types, compact)
	case "csv":
		w := csv.NewWriter(out)
		if err := w.Write([]string{"id", "name", "category", "tcp_port", "udp_port", "second_unique_id"}); err != nil {
			return err
		}
		for _, hardware := range types {
			if err := w.Write([]string{strconv.FormatInt(hardware.ID, 10), hardware.Name, hardware.Category, hardware.TCPPort, hardware.UDPPort, strconv.FormatBool(hardware.SecondUniqueID)}); err != nil {
				return err
			}
		}
		w.Flush()
		return w.Error()
	case "table":
		rows := make([][]string, 0, len(types))
		for _, hardware := range types {
			rows = append(rows, []string{strconv.FormatInt(hardware.ID, 10), hardware.Name, hardware.Category, hardware.TCPPort, hardware.UDPPort})
		}
		return texttable.WriteAdaptive(out, notice, []texttable.Column{
			{Header: "ID", MinWidth: 7},
			{Header: "DEVICE TYPE", MinWidth: 18},
			{Header: "CATEGORY", MinWidth: 9, HidePriority: 1},
			{Header: "TCP PORT", MinWidth: 8},
			{Header: "UDP PORT", MinWidth: 8},
		}, rows, tableWidth)
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func runUnitsConnection(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commandError(opts, "units connection", "UNIT is required")
	}
	unitRef, args := args[0], args[1:]
	fs := newCommandFlagSet("units connection", "units connection", opts)
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "units connection"); err != nil {
		return err
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		connection, err := connectionForUnit(ctx, client, unit)
		if err != nil {
			return err
		}
		return printUnitConnection(connection, strings.ToLower(*format), opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
	})
}

func runUnitsGet(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commandError(opts, "units get", "UNIT is required")
	}
	unitRef, args := args[0], args[1:]
	fs := newCommandFlagSet("units get", "units get", opts)
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table or json")
	fields := fs.String("fields", "", "comma-separated JSON fields")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "units get"); err != nil {
		return err
	}
	selectedFields, err := parseJSONFields(*fields, unitConnectionJSONFields)
	if err != nil {
		return err
	}
	if len(selectedFields) > 0 && !strings.EqualFold(*format, "json") {
		return errors.New("--fields requires --format json")
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		connection := connectionFrom(unit, wialon.HardwareType{ID: unit.HardwareID}, client.SessionInfo().HardwareGatewayIP)
		needsHardware := len(selectedFields) == 0 || slicesContain(selectedFields, "device_type") || slicesContain(selectedFields, "tcp_port") || slicesContain(selectedFields, "udp_port")
		if needsHardware {
			connection, err = connectionForUnit(ctx, client, unit)
			if err != nil {
				return err
			}
		}
		return printUnitConnectionFields(connection, strings.ToLower(*format), *fields, opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
	})
}

func runUnitsUpdate(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commandError(opts, "units update", "UNIT is required")
	}
	unitRef, args := args[0], args[1:]
	fs := newCommandFlagSet("units update", "units update", opts)
	var uniqueID, imei optionalString
	fs.Var(&uniqueID, "unique-id", "new primary unique ID")
	fs.Var(&imei, "imei", "alias for --unique-id")
	deviceType := fs.String("device-type", "", "new device type ID or exact name")
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "units update"); err != nil {
		return err
	}
	newUniqueID, uniqueIDSet, err := selectedUniqueID(uniqueID, imei)
	if err != nil {
		return err
	}
	if uniqueIDSet && len([]rune(newUniqueID)) > 100 {
		return errors.New("--unique-id/--imei must not exceed 100 characters")
	}
	if !uniqueIDSet && strings.TrimSpace(*deviceType) == "" {
		return errors.New("at least one of --unique-id/--imei or --device-type is required")
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		hardwareID := unit.HardwareID
		if strings.TrimSpace(*deviceType) != "" {
			hardware, err := resolveDeviceType(ctx, client, *deviceType, opts.agentMode)
			if err != nil {
				return err
			}
			hardwareID = hardware.ID
		}
		if !uniqueIDSet {
			newUniqueID = unit.UniqueID
		}
		updated, err := client.UpdateDeviceType(ctx, unit.ID, hardwareID, newUniqueID)
		if err != nil {
			return explainConnectivityAccess(err)
		}
		unit.UniqueID, unit.HardwareID = updated.UniqueID, updated.HardwareID
		connection, err := connectionForUnit(ctx, client, unit)
		if err != nil {
			return err
		}
		if !opts.agentMode {
			fmt.Fprintf(opts.stderr, "Updated unit %s (id=%d).\n", unit.Name, unit.ID)
		}
		return printUnitConnection(connection, strings.ToLower(*format), opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
	})
}

func runUnitsCreate(ctx context.Context, args []string, opts options) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commandError(opts, "units create", "NAME is required")
	}
	name, args := args[0], args[1:]
	fs := newCommandFlagSet("units create", "units create", opts)
	var uniqueID, imei optionalString
	fs.Var(&uniqueID, "unique-id", "primary unique ID")
	fs.Var(&imei, "imei", "alias for --unique-id")
	deviceType := fs.String("device-type", "", "device type ID or exact name")
	creatorID := fs.Int64("creator-id", 0, "creator user ID; default is the authenticated user")
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "units create"); err != nil {
		return err
	}
	if count := len([]rune(name)); count < 4 || count > 50 {
		return errors.New("NAME must contain between 4 and 50 characters")
	}
	newUniqueID, uniqueIDSet, err := selectedUniqueID(uniqueID, imei)
	if err != nil {
		return err
	}
	if uniqueIDSet && len([]rune(newUniqueID)) > 100 {
		return errors.New("--unique-id/--imei must not exceed 100 characters")
	}
	if !uniqueIDSet || newUniqueID == "" {
		return errors.New("--unique-id or --imei is required and must not be empty")
	}
	if strings.TrimSpace(*deviceType) == "" {
		return errors.New("--device-type is required")
	}
	if *creatorID < 0 {
		return errors.New("--creator-id must not be negative")
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		hardware, err := resolveDeviceType(ctx, client, *deviceType, opts.agentMode)
		if err != nil {
			return err
		}
		creator := *creatorID
		if creator == 0 {
			creator = client.SessionInfo().UserID
		}
		if creator == 0 {
			return errors.New("Wialon returned no authenticated user ID; specify --creator-id")
		}
		unit, err := client.CreateUnit(ctx, creator, name, hardware.ID)
		if err != nil {
			return err
		}
		updated, err := client.UpdateDeviceType(ctx, unit.ID, hardware.ID, newUniqueID)
		if err != nil {
			return fmt.Errorf("unit %q was created with id=%d, but setting its unique ID failed: %w", unit.Name, unit.ID, explainConnectivityAccess(err))
		}
		unit.UniqueID, unit.HardwareID = updated.UniqueID, updated.HardwareID
		connection := connectionFrom(unit, hardware, client.SessionInfo().HardwareGatewayIP)
		if !opts.agentMode {
			fmt.Fprintf(opts.stderr, "Created unit %s (id=%d).\n", unit.Name, unit.ID)
		}
		return printUnitConnection(connection, strings.ToLower(*format), opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
	})
}

func selectedUniqueID(uniqueID, imei optionalString) (string, bool, error) {
	if uniqueID.set && imei.set {
		return "", false, errors.New("use only one of --unique-id or --imei")
	}
	if uniqueID.set {
		return uniqueID.value, true, nil
	}
	if imei.set {
		return imei.value, true, nil
	}
	return "", false, nil
}

func explainConnectivityAccess(err error) error {
	var apiErr *wialon.APIError
	if errors.As(err, &apiErr) && apiErr.Code == 7 {
		return fmt.Errorf("%w; ensure the profile token was authorized with --access 4864 and the user has the Edit connectivity settings right to this unit", err)
	}
	return err
}

func resolveDeviceType(ctx context.Context, client *wialon.Client, ref string, agentMode bool) (wialon.HardwareType, error) {
	ref = strings.TrimSpace(ref)
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		if id <= 0 {
			return wialon.HardwareType{}, fmt.Errorf("device type ID must be positive, got %q", ref)
		}
		types, err := client.HardwareTypes(ctx, []int64{id})
		if err != nil {
			return wialon.HardwareType{}, err
		}
		if hardware, ok := types[id]; ok {
			return hardware, nil
		}
		return wialon.HardwareType{}, fmt.Errorf("device type ID %d is not available", id)
	}
	types, err := client.DeviceTypes(ctx)
	if err != nil {
		return wialon.HardwareType{}, err
	}
	matches := make([]wialon.HardwareType, 0, 1)
	for _, hardware := range types {
		if strings.EqualFold(hardware.Name, ref) {
			matches = append(matches, hardware)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return wialon.HardwareType{}, fmt.Errorf("device type name %q is ambiguous; use its numeric ID", ref)
	}
	return wialon.HardwareType{}, fmt.Errorf("device type %q is not available; run '%s units device-types --search %q'", ref, invocationName(agentMode), ref)
}

func connectionForUnit(ctx context.Context, client *wialon.Client, unit wialon.Unit) (unitConnection, error) {
	types, err := client.HardwareTypes(ctx, []int64{unit.HardwareID})
	if err != nil {
		return unitConnection{}, err
	}
	hardware, ok := types[unit.HardwareID]
	if !ok {
		return unitConnection{}, fmt.Errorf("device type ID %d for unit %q is not available", unit.HardwareID, unit.Name)
	}
	return connectionFrom(unit, hardware, client.SessionInfo().HardwareGatewayIP), nil
}

func connectionFrom(unit wialon.Unit, hardware wialon.HardwareType, serverAddress string) unitConnection {
	return unitConnection{
		UnitID: unit.ID, Name: unit.Name, UniqueID: unit.UniqueID, UniqueID2: unit.UniqueID2,
		DeviceType: hardware.Name, DeviceTypeID: hardware.ID,
		ServerAddress: serverAddress, TCPPort: hardware.TCPPort, UDPPort: hardware.UDPPort,
	}
}

func printUnitConnection(connection unitConnection, format string, compact bool, out, notice io.Writer, tableWidth int) error {
	return printUnitConnectionFields(connection, format, "", compact, out, notice, tableWidth)
}

func printUnitConnectionFields(connection unitConnection, format, fields string, compact bool, out, notice io.Writer, tableWidth int) error {
	if format == "json" {
		selected, err := selectUnitConnectionFields(connection, fields)
		if err != nil {
			return err
		}
		return writeJSON(out, selected, compact)
	}
	if fields != "" {
		return errors.New("--fields requires --format json")
	}
	if format != "table" {
		return fmt.Errorf("unsupported format %q", format)
	}
	return texttable.WriteAdaptive(out, notice, []texttable.Column{
		{Header: "ID", MinWidth: 8, HidePriority: 2},
		{Header: "NAME", MinWidth: 12},
		{Header: "UNIQUE ID", MinWidth: 15},
		{Header: "DEVICE TYPE", MinWidth: 14},
		{Header: "SERVER", MinWidth: 13},
		{Header: "TCP PORT", MinWidth: 8},
		{Header: "UDP PORT", MinWidth: 8, HidePriority: 1},
	}, [][]string{{
		strconv.FormatInt(connection.UnitID, 10), connection.Name, connection.UniqueID,
		connection.DeviceType, connection.ServerAddress, connection.TCPPort, connection.UDPPort,
	}}, tableWidth)
}

func selectUnitConnectionFields(connection unitConnection, fields string) (any, error) {
	selected, err := parseJSONFields(fields, unitConnectionJSONFields)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return connection, nil
	}
	values := map[string]any{
		"unit_id": connection.UnitID, "name": connection.Name, "unique_id": connection.UniqueID,
		"unique_id2": connection.UniqueID2, "device_type": connection.DeviceType,
		"device_type_id": connection.DeviceTypeID, "server_address": connection.ServerAddress,
		"tcp_port": connection.TCPPort, "udp_port": connection.UDPPort,
	}
	result := make(map[string]any, len(selected))
	for _, field := range selected {
		result[field] = values[field]
	}
	return result, nil
}

func runUnitsStatus(ctx context.Context, args []string, opts options) error {
	unitRef := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		unitRef, args = args[0], args[1:]
	}
	fs := newCommandFlagSet("units status", "units status", opts)
	search := fs.String("search", "*", "Wialon unit name mask")
	offline := fs.Bool("offline", false, "show only disconnected units")
	var stale flexibleDuration
	fs.Var(&stale, "stale", "show units whose last position is at least this old, e.g. 30d")
	var inactive flexibleDuration
	fs.Var(&inactive, "inactive", "show units with no position or message newer than this, e.g. 30d")
	sortBy := fs.String("sort", "age", "sort by age or name")
	limit := fs.Int("limit", 0, "maximum rows; 0 means all")
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table, json, or csv")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "units status"); err != nil {
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
		return printUnitStatuses(filtered, *format, opts.compact, now, opts.stdout, opts.stdout, opts.tableWidth)
	})
}

func printUnitStatuses(statuses []wialon.UnitStatus, format string, compact bool, now time.Time, out, notice io.Writer, tableWidth int) error {
	for i := range statuses {
		if statuses[i].Position.Time != 0 {
			age := now.Sub(time.Unix(statuses[i].Position.Time, 0))
			if age > 0 {
				statuses[i].PointAgeSeconds = int64(age / time.Second)
			}
		}
	}
	if format == "json" {
		return writeJSON(out, statuses, compact)
	}
	rows := make([][]string, 0, len(statuses))
	for _, status := range statuses {
		lastPoint, pointAge := "never", "never"
		if status.Position.Time != 0 {
			pointTime := time.Unix(status.Position.Time, 0).In(now.Location())
			lastPoint, pointAge = pointTime.Format(time.RFC3339), formatAge(now.Sub(pointTime))
		}
		lastMessage, messageAge := "never", "never"
		if status.LastMessageTime != 0 {
			messageTime := time.Unix(status.LastMessageTime, 0).In(now.Location())
			lastMessage = messageTime.Format(time.RFC3339)
			messageAge = formatAge(now.Sub(messageTime))
		}
		state := "offline"
		if status.Online {
			state = "online"
		}
		position := ""
		if status.Position.Time != 0 {
			position = fmt.Sprintf("%.6f,%.6f", status.Position.Latitude, status.Position.Longitude)
		}
		rows = append(rows, []string{strconv.FormatInt(status.ID, 10), status.Name, status.UniqueID, state, lastPoint, pointAge, lastMessage, messageAge, position})
	}
	if format == "table" {
		return texttable.WriteAdaptive(out, notice, []texttable.Column{
			{Header: "ID", MinWidth: 8, HidePriority: 2},
			{Header: "NAME", MinWidth: 11},
			{Header: "UNIQUE ID", MinWidth: 15},
			{Header: "STATUS", MinWidth: 7},
			{Header: "LAST POINT", MinWidth: 16, HidePriority: 3},
			{Header: "POINT AGE", MinWidth: 8},
			{Header: "LAST MESSAGE", MinWidth: 16, HidePriority: 4},
			{Header: "MSG AGE", MinWidth: 7},
			{Header: "POSITION", MinWidth: 19, HidePriority: 1, HideIfEmpty: true},
		}, rows, tableWidth)
	}
	if format == "csv" {
		w := csv.NewWriter(out)
		if err := w.Write([]string{"id", "name", "unique_id", "status", "last_point", "point_age", "last_message", "position"}); err != nil {
			return err
		}
		for _, row := range rows {
			csvRow := append(append([]string(nil), row[:7]...), row[8])
			if err := w.Write(csvRow); err != nil {
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
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commandError(opts, "messages tail", "UNIT is required")
	}
	unitRef, args := args[0], args[1:]
	fs := newCommandFlagSet("messages tail", "messages tail", opts)
	count := fs.Int("n", 20, "number of latest messages")
	follow := fs.Bool("follow", false, "poll for new messages until interrupted")
	poll := fs.Duration("poll", 2*time.Second, "poll interval for --follow")
	format := fs.String("format", defaultFormat(opts, "table", "ndjson"), "table, json, or ndjson")
	allTypes := fs.Bool("all-types", false, "include non-telemetry messages")
	maxParams := fs.Int("max-params", 100, "maximum parameter characters in table output")
	fullParams := fs.Bool("full-params", false, "show complete parameters in table output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "messages tail"); err != nil {
		return err
	}
	if *count < 1 || *count > 10000 {
		return errors.New("-n must be between 1 and 10000")
	}
	if *poll < 500*time.Millisecond {
		return errors.New("--poll must be at least 500ms")
	}
	if *maxParams < 10 {
		return errors.New("--max-params must be at least 10")
	}
	if *follow && *format == "json" {
		return errors.New("--follow supports table or ndjson, not a finite JSON array")
	}
	return withClient(ctx, opts, func(client *wialon.Client) error {
		unit, err := resolveUnit(ctx, client, unitRef)
		if err != nil {
			return err
		}
		if !opts.agentMode {
			fmt.Fprintf(opts.stderr, "Unit: %s (id=%d)\n", unit.Name, unit.ID)
		}
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
				limit := *maxParams
				if *fullParams {
					limit = 0
				}
				return printTailMessages(fresh, *format, limit, opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
			}
			return nil
		}
		if err := loadAndPrint(); err != nil {
			return err
		}
		if !*follow {
			return nil
		}
		if !opts.agentMode {
			fmt.Fprintf(opts.stderr, "Following every %s; press Ctrl-C to stop.\n", *poll)
		}
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

func printTailMessages(messages []map[string]any, format string, maxParams int, compact bool, out, notice io.Writer, tableWidth int) error {
	switch format {
	case "json":
		return writeJSON(out, messages, compact)
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
				params = truncateRunes(string(data), maxParams)
			}
			rows = append(rows, []string{formatted, fmt.Sprint(message["tp"]), lat, lon, speed, params})
		}
		return texttable.WriteAdaptive(out, notice, []texttable.Column{
			{Header: "TIME", MinWidth: 16},
			{Header: "TYPE", MinWidth: 4},
			{Header: "LAT", MinWidth: 8, HidePriority: 2, HideIfEmpty: true},
			{Header: "LON", MinWidth: 8, HidePriority: 2, HideIfEmpty: true},
			{Header: "SPEED", MinWidth: 5, HidePriority: 1, HideIfEmpty: true},
			{Header: "PARAMETERS", MinWidth: 20},
		}, rows, tableWidth)
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
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
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return commandError(opts, "messages export", "UNIT is required")
	}
	unitRef, args := args[0], args[1:]
	fs := newCommandFlagSet("messages export", "messages export", opts)
	fromText := fs.String("from", "", "interval start in RFC3339")
	toText := fs.String("to", "", "interval end in RFC3339")
	var last flexibleDuration
	fs.Var(&last, "last", "relative interval ending now, e.g. 2h or 7d")
	today := fs.Bool("today", false, "today through now")
	yesterday := fs.Bool("yesterday", false, "previous local calendar day")
	since := fs.String("since", "", "interval start as RFC3339 or local HH:MM")
	format := fs.String("format", "wln", "kml, plt, wln, or wlb")
	compress := fs.Bool("compress", false, "request a compressed archive")
	output := fs.String("output", "", "output file path; - writes to stdout")
	force := fs.Bool("force", false, "replace an existing output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "messages export"); err != nil {
		return err
	}
	allowed := map[string]bool{"kml": true, "plt": true, "wln": true, "wlb": true}
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
		if !opts.agentMode {
			fmt.Fprintf(opts.stderr, "Unit: %s (id=%d)\nInterval: %s — %s\n", unit.Name, unit.ID, from.Format(time.RFC3339), to.Format(time.RFC3339))
		}
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
		if opts.agentMode {
			return writeJSON(opts.stdout, map[string]any{"ok": true, "format": *format, "bytes": len(data), "output": abs}, opts.compact)
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
	fs := newCommandFlagSet("doctor", "doctor", opts)
	format := fs.String("format", defaultFormat(opts, "table", "json"), "table or json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectUnexpectedArgs(fs, opts, "doctor"); err != nil {
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
		_ = printChecks(checks, *format, opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
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
	return printChecks(checks, *format, opts.compact, opts.stdout, opts.stdout, opts.tableWidth)
}

func printChecks(checks []doctorCheck, format string, compact bool, out, notice io.Writer, tableWidth int) error {
	if format == "json" {
		return writeJSON(out, checks, compact)
	}
	if format != "table" {
		return fmt.Errorf("unsupported format %q", format)
	}
	rows := make([][]string, 0, len(checks))
	for _, c := range checks {
		rows = append(rows, []string{c.Check, c.Status, c.Details})
	}
	return texttable.WriteAdaptive(out, notice, []texttable.Column{
		{Header: "CHECK", MinWidth: 8},
		{Header: "STATUS", MinWidth: 6},
		{Header: "DETAILS", MinWidth: 20},
	}, rows, tableWidth)
}
