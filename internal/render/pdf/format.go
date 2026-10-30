package pdf

import "github.com/Grandbusta/minime/internal/memory"

type Format struct{}

func (Format) Name() string      { return "pdf" }
func (Format) Ext() string       { return "pdf" }
func (Format) MediaType() string { return "application/pdf" }
func (Format) Describe() string  { return "a PDF document, to print, attach or pass on" }
func (Format) Needs() string     { return "" }

func (Format) WriteHelp() string {
	return "write it as one: the title is printed above it, so don't repeat it; use Markdown headings, and lists or " +
		"tables where they help; no chat talk around it"
}

func (Format) Render(w memory.Work, author string) ([]byte, error) {
	return Render(Doc{Title: w.Title, Author: author, Text: w.Text, At: w.CreatedAt})
}
