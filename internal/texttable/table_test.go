package texttable

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWriteBoxedTableWithUnicode(t *testing.T) {
	var output bytes.Buffer
	if err := Write(&output, []string{"NAME", "HARDWARE"}, [][]string{
		{"Машина 01", "Tracker X"},
		{"CAN_FINDER", "Unknown (#42)"},
	}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, fragment := range []string{"┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘", "Машина 01"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("table is missing %q:\n%s", fragment, text)
		}
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	wantWidth := utf8.RuneCountInString(lines[0])
	for _, line := range lines {
		if got := utf8.RuneCountInString(line); got != wantWidth {
			t.Errorf("line width = %d, want %d: %q", got, wantWidth, line)
		}
	}
}
