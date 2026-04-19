package slack

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

const defaultMaxXlsxRows = 200

// parseXlsx reads xlsx bytes and returns a TSV text representation.
// Each non-empty sheet gets a header line. Rows are capped at maxRows per sheet.
func parseXlsx(data []byte, maxRows int) (string, error) {
	if maxRows <= 0 {
		maxRows = defaultMaxXlsxRows
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("open xlsx: %w", err)
	}
	defer f.Close()

	var sb strings.Builder
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			continue
		}
		if len(rows) == 0 {
			continue
		}

		totalRows := len(rows)
		truncated := totalRows > maxRows

		if truncated {
			sb.WriteString(fmt.Sprintf("--- (Sheet: %s, showing first %d of %d rows) ---\n", sheet, maxRows, totalRows))
		} else {
			sb.WriteString(fmt.Sprintf("--- (Sheet: %s, %d rows) ---\n", sheet, totalRows))
		}

		sb.WriteString("```\n")
		limit := totalRows
		if truncated {
			limit = maxRows
		}
		for i := 0; i < limit; i++ {
			sb.WriteString(strings.Join(rows[i], "\t"))
			sb.WriteString("\n")
		}
		if truncated {
			sb.WriteString(fmt.Sprintf("... [truncated, showing first %d of %d rows]\n", maxRows, totalRows))
		}
		sb.WriteString("```\n")
	}

	return sb.String(), nil
}
