package backup

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var dumpCOPYHeader = regexp.MustCompile(`^COPY (.+?) \((.*)\) FROM stdin;$`)

// filterDump removes historical parameter audits without changing live data.
func filterDump(dst io.Writer, src io.Reader) error {
	reader := bufio.NewReader(src)
	tableData, inCOPY := false, false
	otherIndex, columnCount := -1, 0
	row := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("read pg_dump output: %w", readErr)
		}
		if len(line) == 0 {
			if inCOPY {
				return fmt.Errorf("unterminated COPY data")
			}
			return nil
		}
		record := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		if inCOPY {
			if bytes.Equal(record, []byte(`\.`)) {
				inCOPY = false
			} else if otherIndex >= 0 {
				row++
				fields := bytes.Split(record, []byte("\t"))
				if len(fields) != columnCount {
					return fmt.Errorf("logs COPY row %d: column count mismatch", row)
				}
				other, changed, err := filterLogOther(fields[otherIndex])
				if err != nil {
					return fmt.Errorf("logs COPY row %d: %w", row, err)
				}
				if changed {
					fields[otherIndex] = other
					line = append(bytes.Join(fields, []byte("\t")), line[len(record):]...)
				}
			}
		} else if bytes.HasPrefix(record, []byte("-- Data for Name: ")) {
			tableData = bytes.Contains(record, []byte("; Type: TABLE DATA;"))
		} else if bytes.HasPrefix(record, []byte("-- Name: ")) {
			tableData = false
		} else if tableData {
			if header := dumpCOPYHeader.FindSubmatch(record); header != nil {
				tableData, inCOPY = false, true
				otherIndex, columnCount, row = -1, 0, 0
				switch string(header[1]) {
				case "public.logs", `"public".logs`, `public."logs"`, `"public"."logs"`:
					columns := csv.NewReader(strings.NewReader(string(header[2])))
					columns.TrimLeadingSpace = true
					names, err := columns.Read()
					if err != nil {
						return fmt.Errorf("invalid logs COPY columns")
					}
					columnCount = len(names)
					for i, name := range names {
						if name == "other" {
							otherIndex = i
						}
					}
				}
			}
		}
		if n, err := dst.Write(line); err != nil {
			return fmt.Errorf("write filtered dump: %w", err)
		} else if n != len(line) {
			return io.ErrShortWrite
		}
	}
}

func filterLogOther(field []byte) ([]byte, bool, error) {
	if len(field) == 0 || bytes.Equal(field, []byte(`\N`)) {
		return field, false, nil
	}
	decoded, err := decodeCOPYText(field)
	if err != nil {
		return nil, false, err
	}
	trimmed := bytes.TrimSpace(decoded)
	if len(trimmed) == 0 || !utf8.Valid(trimmed) {
		return nil, false, fmt.Errorf("invalid JSON in logs.other")
	}
	if trimmed[0] != '{' {
		if !json.Valid(trimmed) {
			return nil, false, fmt.Errorf("invalid JSON in logs.other")
		}
		return field, false, nil
	}
	var other map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &other); err != nil {
		return nil, false, fmt.Errorf("invalid JSON object in logs.other")
	}
	if _, exists := other["po"]; !exists {
		return field, false, nil
	}
	delete(other, "po")
	encoded, err := json.Marshal(other)
	if err != nil {
		return nil, false, fmt.Errorf("encode logs.other: %w", err)
	}
	return encodeCOPYText(encoded), true, nil
}

func decodeCOPYText(field []byte) ([]byte, error) {
	decoded := make([]byte, 0, len(field))
	for i := 0; i < len(field); i++ {
		if field[i] != '\\' {
			decoded = append(decoded, field[i])
			continue
		}
		i++
		if i == len(field) {
			return nil, fmt.Errorf("incomplete COPY escape in logs.other")
		}
		switch field[i] {
		case 'b':
			decoded = append(decoded, '\b')
		case 'f':
			decoded = append(decoded, '\f')
		case 'n':
			decoded = append(decoded, '\n')
		case 'r':
			decoded = append(decoded, '\r')
		case 't':
			decoded = append(decoded, '\t')
		case 'v':
			decoded = append(decoded, '\v')
		case 'x':
			start := i + 1
			for i+1 < len(field) && i+1-start < 2 && strings.ContainsRune("0123456789abcdefABCDEF", rune(field[i+1])) {
				i++
			}
			if i < start {
				return nil, fmt.Errorf("invalid COPY hex escape in logs.other")
			}
			value, err := strconv.ParseUint(string(field[start:i+1]), 16, 8)
			if err != nil {
				return nil, fmt.Errorf("invalid COPY hex escape in logs.other")
			}
			decoded = append(decoded, byte(value))
		default:
			if field[i] >= '0' && field[i] <= '7' {
				value := field[i] - '0'
				for digits := 1; digits < 3 && i+1 < len(field) && field[i+1] >= '0' && field[i+1] <= '7'; digits++ {
					i++
					value = value*8 + field[i] - '0'
				}
				decoded = append(decoded, value)
			} else {
				decoded = append(decoded, field[i])
			}
		}
	}
	return decoded, nil
}

func encodeCOPYText(value []byte) []byte {
	escaper := strings.NewReplacer("\\", "\\\\", "\b", "\\b", "\f", "\\f", "\n", "\\n", "\r", "\\r", "\t", "\\t", "\v", "\\v")
	return []byte(escaper.Replace(string(value)))
}
