package texttable

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

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
