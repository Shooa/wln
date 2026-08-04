package exportcsv

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

var preferredColumns = []string{
	"t", "r", "rt", "tp", "f", "i", "o",
	"pos.x", "pos.y", "pos.z", "pos.s", "pos.c", "pos.sc",
}

type Spool struct {
	file    *os.File
	columns map[string]struct{}
	rows    int
}

func NewSpool() (*Spool, error) {
	f, err := os.CreateTemp("", "wln-messages-*.ndjson")
	if err != nil {
		return nil, fmt.Errorf("create message spool: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, fmt.Errorf("secure message spool: %w", err)
	}
	return &Spool{file: f, columns: make(map[string]struct{})}, nil
}

func (s *Spool) Add(message map[string]any) error {
	flat := make(map[string]any)
	flatten("", message, flat)
	for name := range flat {
		s.columns[name] = struct{}{}
	}
	if err := json.NewEncoder(s.file).Encode(message); err != nil {
		return fmt.Errorf("write message spool: %w", err)
	}
	s.rows++
	return nil
}

func (s *Spool) Rows() int { return s.rows }

func (s *Spool) WriteCSV(output string, force bool) error {
	return s.Write(output, force, "csv")
}

func (s *Spool) Write(output string, force bool, format string) error {
	if !force {
		if _, err := os.Stat(output); err == nil {
			return fmt.Errorf("output %s already exists (use --force to replace it)", output)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect output: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind message spool: %w", err)
	}
	out, err := os.CreateTemp(filepath.Dir(output), ".wln-export-*.csv")
	if err != nil {
		return fmt.Errorf("create temporary CSV: %w", err)
	}
	tmpName := out.Name()
	defer os.Remove(tmpName)
	if err := out.Chmod(0o600); err != nil {
		out.Close()
		return fmt.Errorf("secure temporary CSV: %w", err)
	}

	if err := s.WriteTo(out, format); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("sync output: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	if err := os.Rename(tmpName, output); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	return nil
}

func (s *Spool) WriteTo(out io.Writer, format string) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind message spool: %w", err)
	}
	switch format {
	case "csv":
		return s.writeCSVTo(out)
	case "ndjson":
		_, err := io.Copy(out, s.file)
		return err
	case "json":
		return s.writeJSONTo(out)
	default:
		return fmt.Errorf("unsupported output format %q", format)
	}
}

func (s *Spool) writeCSVTo(out io.Writer) error {
	headers := orderedHeaders(s.columns)
	w := csv.NewWriter(out)
	if err := w.Write(headers); err != nil {
		return fmt.Errorf("write CSV header: %w", err)
	}
	scanner := bufio.NewScanner(s.file)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 64*1024*1024)
	for scanner.Scan() {
		var message map[string]any
		decoder := json.NewDecoder(bytesReader(scanner.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&message); err != nil {
			return fmt.Errorf("decode spooled message: %w", err)
		}
		flat := make(map[string]any)
		flatten("", message, flat)
		row := make([]string, len(headers))
		for i, name := range headers {
			row[i] = cell(flat[name])
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("write CSV row: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read message spool: %w", err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("flush CSV: %w", err)
	}
	return nil
}

func (s *Spool) writeJSONTo(out io.Writer) error {
	if _, err := io.WriteString(out, "[\n"); err != nil {
		return err
	}
	scanner := bufio.NewScanner(s.file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	first := true
	for scanner.Scan() {
		if !first {
			if _, err := io.WriteString(out, ",\n"); err != nil {
				return err
			}
		}
		first = false
		if _, err := out.Write(scanner.Bytes()); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(out, "\n]\n")
	return err
}

func (s *Spool) Close() error {
	name := s.file.Name()
	err := s.file.Close()
	removeErr := os.Remove(name)
	if err != nil {
		return err
	}
	if removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	return nil
}

func flatten(prefix string, value any, out map[string]any) {
	object, ok := value.(map[string]any)
	if !ok {
		if prefix != "" {
			out[prefix] = value
		}
		return
	}
	for key, child := range object {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		if _, nested := child.(map[string]any); nested {
			flatten(name, child, out)
		} else {
			out[name] = child
		}
	}
}

func orderedHeaders(columns map[string]struct{}) []string {
	result := make([]string, 0, len(columns))
	seen := make(map[string]bool)
	for _, name := range preferredColumns {
		if _, ok := columns[name]; ok {
			result = append(result, name)
			seen[name] = true
		}
	}
	rest := make([]string, 0, len(columns)-len(result))
	for name := range columns {
		if !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(result, rest...)
}

func cell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

type byteReader struct {
	b []byte
	i int
}

func bytesReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
