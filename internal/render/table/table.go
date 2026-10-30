// Package table is data a work carries: a table of rows and columns, made
// into a spreadsheet (XLSX) or a CSV file. Both are made from the same data.
package table

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

type Table struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

const (
	maxColumns = 50
	maxRows    = 2000
	maxCell    = 2000 // runes
)

var ErrEmpty = errors.New("table: no columns")

// Parse reads a table as JSON and cleans it: cells trimmed and cut at
// maxCell, rows fitted to the columns, empty rows dropped. A table with
// headings and no rows is a template to fill in, so it's kept.
func Parse(raw []byte) (Table, error) {
	var t Table
	if err := json.Unmarshal(raw, &t); err != nil {
		return Table{}, fmt.Errorf("table: %w", err)
	}
	cell := func(s string) string {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) > maxCell {
			s = string([]rune(s)[:maxCell])
		}
		return s
	}
	out := Table{}
	for _, c := range t.Columns[:min(len(t.Columns), maxColumns)] {
		out.Columns = append(out.Columns, cell(c))
	}
	for _, r := range t.Rows {
		if len(out.Rows) == maxRows {
			break
		}
		row := make([]string, len(out.Columns))
		empty := true
		for i := range row {
			if i < len(r) {
				row[i] = cell(r[i])
				empty = empty && row[i] == ""
			}
		}
		if !empty {
			out.Rows = append(out.Rows, row)
		}
	}
	if slices.IndexFunc(out.Columns, func(c string) bool { return c != "" }) < 0 {
		return Table{}, ErrEmpty
	}
	return out, nil
}

// data is what both formats share: the table's definition.
type data struct{}

func (data) Needs() string     { return "table" }
func (data) WriteHelp() string { return "" }

func (data) DataHelp() string {
	return "its column headings, then every row, each cell as text in the columns' order. Write numbers as plain digits " +
		"(1500, 12.5), with no currency signs or thousands commas, so they can be added up; name the unit in the heading. " +
		"A template to fill in is its headings with no rows; never make up rows to fill it."
}

var schema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"columns", "rows"},
	"properties": map[string]any{
		"columns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"rows":    map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
	}}

func (data) DataSchema() map[string]any { return schema }

func (data) CleanData(raw json.RawMessage) (json.RawMessage, error) {
	t, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(t)
}

type XLSX struct{ data }

func (XLSX) Name() string { return "xlsx" }
func (XLSX) Ext() string  { return "xlsx" }
func (XLSX) MediaType() string {
	return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
}
func (XLSX) Describe() string { return "a spreadsheet for Excel or Google Sheets" }

func (XLSX) Render(w memory.Work, _ string) ([]byte, error) {
	t, err := Parse(w.Data)
	if err != nil {
		return nil, err
	}
	f := excelize.NewFile()
	defer f.Close()
	sheet := sheetName(w.Title)
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return nil, err
	}
	header := make([]any, len(t.Columns))
	widths := make([]int, len(t.Columns))
	for i, c := range t.Columns {
		header[i], widths[i] = c, utf8.RuneCountInString(c)
	}
	if err := f.SetSheetRow(sheet, "A1", &header); err != nil {
		return nil, err
	}
	for r, row := range t.Rows {
		cells := make([]any, len(row))
		for i, c := range row {
			cells[i] = c
			if n, err := strconv.ParseFloat(c, 64); err == nil { // a number, so it adds up
				cells[i] = n
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
		cell, _ := excelize.CoordinatesToCellName(1, r+2)
		if err := f.SetSheetRow(sheet, cell, &cells); err != nil {
			return nil, err
		}
	}
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, err
	}
	last, _ := excelize.CoordinatesToCellName(len(t.Columns), 1)
	if err := f.SetCellStyle(sheet, "A1", last, bold); err != nil {
		return nil, err
	}
	for i, wd := range widths {
		col, _ := excelize.ColumnNumberToName(i + 1)
		if err := f.SetColWidth(sheet, col, col, float64(min(max(wd, 8), 60)+2)); err != nil {
			return nil, err
		}
	}
	if err := f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return nil, err
	}
	end, _ := excelize.CoordinatesToCellName(len(t.Columns), len(t.Rows)+1)
	if err := f.AutoFilter(sheet, "A1:"+end, nil); err != nil {
		return nil, err
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sheetName is title as Excel allows a sheet's name: 31 characters, none of
// []:*?/\.
func sheetName(title string) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return -1
		}
		return r
	}, strings.TrimSpace(title))
	if r := []rune(name); len(r) > 31 {
		name = string(r[:31])
	}
	return strings.Trim(name, "' ")
}

type CSV struct{ data }

func (CSV) Name() string      { return "csv" }
func (CSV) Ext() string       { return "csv" }
func (CSV) MediaType() string { return "text/csv" }
func (CSV) Describe() string  { return "a CSV file, plain rows any program opens" }

func (CSV) Render(w memory.Work, _ string) ([]byte, error) {
	t, err := Parse(w.Data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("\uFEFF") // so Excel reads it as UTF-8
	cw := csv.NewWriter(&buf)
	cw.Write(t.Columns)
	cw.WriteAll(t.Rows)
	return buf.Bytes(), cw.Error()
}
