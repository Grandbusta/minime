package archive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/Grandbusta/minime/internal/memory"
)

func TestRender(t *testing.T) {
	w := memory.Work{Title: "Todo CLI", DataKind: "files", Data: json.RawMessage(`{"files":[
		{"path":"main.go","content":"package main\n"},{"path":"./src/../README.md","content":"# Todo\n"},
		{"path":"../../etc/passwd","content":"x"},{"path":"/abs.txt","content":"x"},{"path":"main.go","content":"again"},{"path":"  ","content":"x"}]}`)}
	data, err := Format{}.Render(w, "")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		got[f.Name] = string(b)
	}
	if len(got) != 2 || got["main.go"] != "package main\n" || got["README.md"] != "# Todo\n" {
		t.Errorf("files = %v", got)
	}
}
