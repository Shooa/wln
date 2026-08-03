package exportcsv

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSpoolFlattensDynamicMessageFields(t *testing.T) {
	spool, err := NewSpool()
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	if err := spool.Add(map[string]any{
		"t": float64(10), "r": float64(9), "rt": float64(11), "tp": "ud",
		"pos": map[string]any{"x": 56.1, "y": 57.2},
		"p":   map[string]any{"sensor_value": 100.0, "sample_ms": 250.0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := spool.Add(map[string]any{
		"t": float64(12), "rt": float64(13), "tp": "ud",
		"p": map[string]any{"sensor_value": 101.0, "battery_level": 3.0},
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "messages.csv")
	if err := spool.WriteCSV(path, false); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("CSV rows = %d, want 3", len(rows))
	}
	for _, required := range []string{"t", "r", "rt", "tp", "pos.x", "p.sensor_value", "p.sample_ms", "p.battery_level"} {
		if !slices.Contains(rows[0], required) {
			t.Errorf("missing header %q in %v", required, rows[0])
		}
	}
	if rows[1][indexOf(rows[0], "p.sensor_value")] != "100" {
		t.Fatalf("sensor_value row = %v", rows[1])
	}
}

func indexOf(values []string, wanted string) int {
	for i, value := range values {
		if value == wanted {
			return i
		}
	}
	return -1
}
