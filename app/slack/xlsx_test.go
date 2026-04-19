package slack

import (
	"os"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func createTestXlsx(t *testing.T, sheets map[string][][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	first := true
	for name, rows := range sheets {
		if first {
			f.SetSheetName("Sheet1", name)
			first = false
		} else {
			f.NewSheet(name)
		}
		for i, row := range rows {
			for j, cell := range row {
				cellName, _ := excelize.CoordinatesToCellName(j+1, i+1)
				f.SetCellValue(name, cellName, cell)
			}
		}
	}
	tmp := t.TempDir() + "/test.xlsx"
	if err := f.SaveAs(tmp); err != nil {
		t.Fatalf("save xlsx: %v", err)
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read xlsx: %v", err)
	}
	return data
}

func TestParseXlsx_SingleSheet(t *testing.T) {
	data := createTestXlsx(t, map[string][][]string{
		"Data": {
			{"Name", "Age"},
			{"Alice", "30"},
			{"Bob", "25"},
		},
	})
	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("expected result to contain 'Alice', got:\n%s", result)
	}
	if !strings.Contains(result, "Sheet: Data") {
		t.Errorf("expected sheet name in header, got:\n%s", result)
	}
}

func TestParseXlsx_Truncation(t *testing.T) {
	var rows [][]string
	for i := 0; i < 300; i++ {
		rows = append(rows, []string{"row", "data"})
	}
	data := createTestXlsx(t, map[string][][]string{"Big": rows})
	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if !strings.Contains(result, "truncated") {
		t.Errorf("expected truncation notice, got:\n%s", result)
	}
}

func TestParseXlsx_MultiSheet(t *testing.T) {
	data := createTestXlsx(t, map[string][][]string{
		"Sheet1": {{"A", "B"}, {"1", "2"}},
		"Sheet2": {{"X", "Y"}, {"3", "4"}},
	})
	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if !strings.Contains(result, "Sheet: Sheet1") {
		t.Errorf("expected Sheet1 header")
	}
	if !strings.Contains(result, "Sheet: Sheet2") {
		t.Errorf("expected Sheet2 header")
	}
}

func TestParseXlsx_EmptySheet(t *testing.T) {
	data := createTestXlsx(t, map[string][][]string{
		"HasData": {{"A"}, {"1"}},
		"Empty":   {},
	})
	result, err := parseXlsx(data, 200)
	if err != nil {
		t.Fatalf("parseXlsx failed: %v", err)
	}
	if strings.Contains(result, "Sheet: Empty") {
		t.Errorf("empty sheet should be skipped")
	}
	if !strings.Contains(result, "Sheet: HasData") {
		t.Errorf("non-empty sheet should be present")
	}
}
