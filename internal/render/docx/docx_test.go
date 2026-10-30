package docx

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Grandbusta/minime/internal/memory"
)

func TestRender(t *testing.T) {
	w := memory.Work{Title: "Clinic outreach", Text: "Clinic outreach\n\nHi [Name],\nI'm Daniel.\n\n# A plan\n\n1. Call **three** clinics\n   - keep it short\n\n> Ask, don't pitch.\n\n| Day | Cost |\n|-----|------|\n| Mon | ₦0 |\n"}
	data, err := Format{}.Render(w, "Daniel")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, f := range z.File {
		if f.Name == "word/document.xml" {
			r, _ := f.Open()
			b, _ := io.ReadAll(r)
			body = string(b)
		}
	}
	for _, want := range []string{`w:val="Title"`, `w:val="Heading1"`, `w:val="ListParagraph"`, `w:val="List2"`, `w:val="Quote"`, "<w:tbl>", "₦0", "<w:br"} {
		if !strings.Contains(body, want) {
			t.Errorf("document lacks %s", want)
		}
	}
	if strings.Count(body, "Clinic outreach") != 1 {
		t.Error("the title was repeated")
	}
	if dir := os.Getenv("OUT"); dir != "" {
		os.WriteFile(dir+"/sample.docx", data, 0o644)
	}
}
