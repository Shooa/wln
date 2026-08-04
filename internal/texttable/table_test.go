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

func TestWriteAdaptiveFitsAndHidesLowPriorityColumns(t *testing.T) {
	var output bytes.Buffer
	columns := []Column{
		{Header: "ID", MinWidth: 2, HidePriority: 1},
		{Header: "NAME", MinWidth: 10},
		{Header: "UNIQUE ID", MinWidth: 15},
		{Header: "HARDWARE", MinWidth: 10, HidePriority: 2},
	}
	rows := [][]string{{"12345678", "A very long unit name", "123456789012345", "A very long hardware name"}}
	if err := WriteAdaptive(&output, &output, columns, rows, 48); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if !strings.HasPrefix(text, "Compact table (48 columns): hidden ID.") {
		t.Fatalf("missing compact notice:\n%s", text)
	}
	if strings.Contains(text, "│ ID ") {
		t.Fatalf("hidden ID column remains in output:\n%s", text)
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n")[1:] {
		if got := utf8.RuneCountInString(line); got > 48 {
			t.Errorf("line width = %d, want <= 48: %q", got, line)
		}
	}
}

func TestWriteAdaptiveOmitsEmptyColumnsWithoutNotice(t *testing.T) {
	var output, notice bytes.Buffer
	columns := []Column{
		{Header: "NAME", MinWidth: 5},
		{Header: "POSITION", MinWidth: 8, HidePriority: 1, HideIfEmpty: true},
	}
	if err := WriteAdaptive(&output, &notice, columns, [][]string{{"unit", ""}}, 20); err != nil {
		t.Fatal(err)
	}
	if notice.Len() != 0 {
		t.Fatalf("unexpected notice: %s", notice.String())
	}
	if strings.Contains(output.String(), "POSITION") {
		t.Fatalf("empty column remains:\n%s", output.String())
	}
}
