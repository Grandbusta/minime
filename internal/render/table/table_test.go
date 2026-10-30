package table

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

var leads = memory.Work{Title: "Clinic leads: Lagos/Island", DataKind: "table", Data: json.RawMessage(`{"columns":["Clinic"," Area ","Fee (NGN)"],
	"rows":[["Smile Dental","Lekki","15000","extra"],["  ","",""],["Yaba Dental","Yaba"]]}`)}

func TestParse(t *testing.T) {
	tb, err := Parse(leads.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 2 || len(tb.Rows[0]) != 3 || tb.Columns[1] != "Area" || tb.Rows[1][2] != "" {
		t.Errorf("table = %+v", tb)
	}
	if _, err := Parse([]byte(`{"columns":[],"rows":[["x"]]}`)); err != ErrEmpty {
		t.Errorf("no columns: %v", err)
	}
}

func TestXLSX(t *testing.T) {
	data, err := XLSX{}.Render(leads, "")
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	sheet := f.GetSheetName(0)
	if sheet != "Clinic leads LagosIsland" {
		t.Errorf("sheet %q", sheet)
	}
	text := func(cell string) bool {
		typ, _ := f.GetCellType(sheet, cell)
		return typ == excelize.CellTypeSharedString || typ == excelize.CellTypeInlineString
	}
	if text("C2") || !text("A2") {
		t.Error("15000 was kept as text, or a name as a number")
	}
	if v, _ := f.GetCellValue(sheet, "A3"); v != "Yaba Dental" {
		t.Errorf("A3 = %q", v)
	}
}

func TestCSV(t *testing.T) {
	data, err := CSV{}.Render(leads, "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\uFEFF")))).ReadAll()
	if err != nil || len(rows) != 3 || rows[0][2] != "Fee (NGN)" || rows[1][2] != "15000" {
		t.Errorf("rows = %v, %v", rows, err)
	}
}
