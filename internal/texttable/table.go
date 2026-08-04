package texttable

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

type Column struct {
	Header       string
	MinWidth     int
	HidePriority int
	HideIfEmpty  bool
}

func Write(w io.Writer, headers []string, rows [][]string) error {
	if len(headers) == 0 {
		return nil
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		widths[i] = displayWidth(clean(header))
	}
	cleanRows := make([][]string, len(rows))
	for rowIndex, row := range rows {
		cleanRows[rowIndex] = make([]string, len(headers))
		for column := range headers {
			var value string
			if column < len(row) {
				value = clean(row[column])
			}
			cleanRows[rowIndex][column] = value
			if width := displayWidth(value); width > widths[column] {
				widths[column] = width
			}
		}
	}
	if err := border(w, "┌", "┬", "┐", widths); err != nil {
		return err
	}
	if err := line(w, headers, widths); err != nil {
		return err
	}
	if err := border(w, "├", "┼", "┤", widths); err != nil {
		return err
	}
	for _, row := range cleanRows {
		if err := line(w, row, widths); err != nil {
			return err
		}
	}
	return border(w, "└", "┴", "┘", widths)
}

func TerminalWidth(w io.Writer) int {
	file, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return 0
	}
	if width, _, err := term.GetSize(int(file.Fd())); err == nil && width > 0 {
		return width
	}
	if width, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && width > 0 {
		return width
	}
	return 0
}

func WriteAdaptive(w, notice io.Writer, columns []Column, rows [][]string, maxWidth int) error {
	if len(columns) == 0 {
		return nil
	}
	headers := make([]string, len(columns))
	for i, column := range columns {
		headers[i] = clean(column.Header)
	}
	cleanRows := make([][]string, len(rows))
	for rowIndex, row := range rows {
		cleanRows[rowIndex] = make([]string, len(columns))
		for column := range columns {
			if column < len(row) {
				cleanRows[rowIndex][column] = clean(row[column])
			}
		}
	}
	visible := make([]bool, len(columns))
	for i := range visible {
		visible[i] = true
		if maxWidth > 0 && columns[i].HideIfEmpty && columnEmpty(cleanRows, i) {
			visible[i] = false
		}
	}
	natural, minimum := columnWidths(columns, headers, cleanRows)
	hidden := make([]string, 0)
	if maxWidth > 0 {
		candidates := make([]int, 0, len(columns))
		for i, column := range columns {
			if column.HidePriority > 0 && visible[i] {
				candidates = append(candidates, i)
			}
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			return columns[candidates[i]].HidePriority < columns[candidates[j]].HidePriority
		})
		for _, column := range candidates {
			if tableWidth(minimum, visible) <= maxWidth {
				break
			}
			visible[column] = false
			hidden = append(hidden, headers[column])
		}
	}
	widths := append([]int(nil), natural...)
	if maxWidth > 0 {
		shrinkWidths(widths, minimum, visible, maxWidth)
	}
	visibleHeaders, visibleWidths := selectStrings(headers, widths, visible)
	visibleRows := make([][]string, len(cleanRows))
	for i, row := range cleanRows {
		visibleRows[i], _ = selectStrings(row, widths, visible)
		for column := range visibleRows[i] {
			visibleRows[i][column] = truncate(visibleRows[i][column], visibleWidths[column])
		}
	}
	for i := range visibleHeaders {
		visibleHeaders[i] = truncate(visibleHeaders[i], visibleWidths[i])
	}
	if len(hidden) > 0 && notice != nil {
		fmt.Fprintf(notice, "Compact table (%d columns): hidden %s. Use --wide to disable fitting.\n", maxWidth, strings.Join(hidden, ", "))
	}
	if err := border(w, "┌", "┬", "┐", visibleWidths); err != nil {
		return err
	}
	if err := line(w, visibleHeaders, visibleWidths); err != nil {
		return err
	}
	if err := border(w, "├", "┼", "┤", visibleWidths); err != nil {
		return err
	}
	for _, row := range visibleRows {
		if err := line(w, row, visibleWidths); err != nil {
			return err
		}
	}
	return border(w, "└", "┴", "┘", visibleWidths)
}

func columnEmpty(rows [][]string, column int) bool {
	for _, row := range rows {
		if column < len(row) && row[column] != "" {
			return false
		}
	}
	return true
}

func columnWidths(columns []Column, headers []string, rows [][]string) ([]int, []int) {
	natural := make([]int, len(columns))
	minimum := make([]int, len(columns))
	for i, column := range columns {
		natural[i] = displayWidth(headers[i])
		for _, row := range rows {
			if width := displayWidth(row[i]); width > natural[i] {
				natural[i] = width
			}
		}
		minimum[i] = column.MinWidth
		if minimum[i] <= 0 {
			minimum[i] = displayWidth(headers[i])
		}
		if minimum[i] > natural[i] {
			minimum[i] = natural[i]
		}
		if minimum[i] < 1 {
			minimum[i] = 1
		}
	}
	return natural, minimum
}

func tableWidth(widths []int, visible []bool) int {
	total := 1
	for i, width := range widths {
		if visible[i] {
			total += width + 3
		}
	}
	return total
}

func shrinkWidths(widths, minimum []int, visible []bool, maxWidth int) {
	for tableWidth(widths, visible) > maxWidth {
		candidate := -1
		for i := range widths {
			if visible[i] && widths[i] > minimum[i] && (candidate == -1 || widths[i]-minimum[i] > widths[candidate]-minimum[candidate]) {
				candidate = i
			}
		}
		if candidate == -1 {
			for i := range widths {
				if visible[i] && widths[i] > 1 && (candidate == -1 || widths[i] > widths[candidate]) {
					candidate = i
				}
			}
		}
		if candidate == -1 {
			return
		}
		widths[candidate]--
	}
}

func selectStrings(values []string, widths []int, visible []bool) ([]string, []int) {
	selectedValues := make([]string, 0, len(values))
	selectedWidths := make([]int, 0, len(values))
	for i, value := range values {
		if visible[i] {
			selectedValues = append(selectedValues, value)
			selectedWidths = append(selectedWidths, widths[i])
		}
	}
	return selectedValues, selectedWidths
}

func truncate(value string, width int) string {
	if width <= 0 || displayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(value)
	return string(runes[:width-1]) + "…"
}

func border(w io.Writer, left, middle, right string, widths []int) error {
	if _, err := io.WriteString(w, left); err != nil {
		return err
	}
	for i, width := range widths {
		if _, err := io.WriteString(w, strings.Repeat("─", width+2)); err != nil {
			return err
		}
		separator := middle
		if i == len(widths)-1 {
			separator = right
		}
		if _, err := io.WriteString(w, separator); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func line(w io.Writer, values []string, widths []int) error {
	if _, err := io.WriteString(w, "│"); err != nil {
		return err
	}
	for i, width := range widths {
		value := ""
		if i < len(values) {
			value = clean(values[i])
		}
		padding := width - displayWidth(value)
		if _, err := fmt.Fprintf(w, " %s%s │", value, strings.Repeat(" ", padding)); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func clean(value string) string {
	return strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(value)
}

func displayWidth(value string) int {
	return utf8.RuneCountInString(value)
}
