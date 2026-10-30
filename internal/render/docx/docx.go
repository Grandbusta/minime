// Package docx writes a work's words as a Word document, laid out from its
// Markdown with Word's own styles (Title, Heading 1 to 9, lists, Quote,
// tables), so it can be edited as one.
package docx

import (
	"bytes"
	"strings"

	"github.com/gomutex/godocx"
	gd "github.com/gomutex/godocx/docx"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/render/markdown"
)

type Format struct{}

func (Format) Name() string { return "docx" }
func (Format) Ext() string  { return "docx" }
func (Format) MediaType() string {
	return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
}
func (Format) Describe() string { return "a Word document they can edit" }
func (Format) Needs() string    { return "" }

func (Format) WriteHelp() string {
	return "write it as one: the title is set above it, so don't repeat it; use Markdown headings, and lists or tables " +
		"where they help; no chat talk around it."
}

func (Format) Render(w memory.Work, _ string) ([]byte, error) {
	doc, err := godocx.NewDocument()
	if err != nil {
		return nil, err
	}
	if t := strings.TrimSpace(w.Title); t != "" {
		if _, err := doc.AddHeading(t, 0); err != nil {
			return nil, err
		}
	}
	for _, b := range markdown.Blocks(w.Title, w.Text) {
		switch b.Kind {
		case markdown.Heading:
			if _, err := doc.AddHeading(strings.ReplaceAll(b.Text, "\n", " "), uint(min(b.Level, 9))); err != nil {
				return nil, err
			}
		case markdown.Paragraph, markdown.Code:
			style := map[bool]string{true: "Quote", false: "Normal"}[b.Quote]
			if b.Kind == markdown.Code {
				style = "MacroText"
			}
			lines(doc.AddEmptyParagraph(), b.Text).Style(style)
		case markdown.Item:
			p := lines(doc.AddEmptyParagraph(), b.Mark+"\t"+b.Text)
			p.Style([]string{"ListParagraph", "List2", "List3"}[min(b.Depth, 2)])
		case markdown.Rule:
			doc.AddEmptyParagraph()
		case markdown.Table:
			t := doc.AddTable()
			t.Style("TableGrid")
			for i, cells := range b.Rows {
				row := t.AddRow()
				for _, c := range cells {
					p := row.AddCell().AddParagraph("")
					r := p.AddText(c)
					if i == 0 {
						r.Bold(true)
					}
				}
			}
			doc.AddEmptyParagraph()
		}
	}
	var buf bytes.Buffer
	if err := doc.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// lines writes text into p, keeping its line breaks.
func lines(p *gd.Paragraph, text string) *gd.Paragraph {
	ls := strings.Split(text, "\n")
	for i, l := range ls {
		if r := p.AddText(l); i < len(ls)-1 {
			r.AddBreak(nil)
		}
	}
	return p
}
