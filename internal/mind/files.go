package mind

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/render/pdf"
)

// asPDF is a promise sent as a PDF document instead of a message.
const asPDF = "pdf"

type Filer interface {
	SendFile(ctx context.Context, p perception.Percept, name string, data []byte, caption string) error
}

type file struct {
	name string
	data []byte
}

// formats is what it can send a promise as besides a message.
func (m *Mind) formats() []string {
	if m.Filer == nil {
		return nil
	}
	return []string{asPDF}
}

func (m *Mind) canSendAs(as string) bool { return as != "" && slices.Contains(m.formats(), as) }

func (m *Mind) filesHelp(n names) string {
	if !m.canSendAs(asPDF) {
		return `- You can't send files: leave "as" and send empty.`
	}
	return fmt.Sprintf(`- You can send things as a PDF document. If %[1]s wants, as a PDF, something you already made (the note `+
		`lists your works with refs like [w1]), send it now with your reply: its ref in send.work and "pdf" in send.as. Your `+
		`reply goes with it as its caption, so say what it is, briefly. Promise it instead only if they want it at a later time, with its ref in the promise's "work".`+
		"\n"+`- If %[1]s wants something new made as a PDF, or to print, attach or pass on, promise it with "as": "pdf". `+
		`Otherwise leave "as" and send empty.`, n.speaker)
}

// fileNote tells System 2, making a promise, how it will be sent.
func fileNote(i memory.Intention) string {
	if i.Args["as"] != asPDF {
		return ""
	}
	return "\n\nIt goes to them as a PDF document, so write it as one: the title is printed above it, so don't repeat it; " +
		"use Markdown headings, and lists or tables where they help; no chat talk around it."
}

// fileFor is the file promise i is sent as; nil if it goes as a message.
func (m *Mind) fileFor(i memory.Intention, w memory.Work) (*file, error) {
	return m.makeFile(i.Args["as"], w)
}

// makeFile is w as a file; nil if it can't send it as one.
func (m *Mind) makeFile(as string, w memory.Work) (*file, error) {
	if !m.canSendAs(as) || as != asPDF {
		return nil, nil
	}
	data, err := pdf.Render(pdf.Doc{Title: w.Title, Author: m.Life.Name, Text: w.Text, At: w.CreatedAt})
	if err != nil {
		return nil, fmt.Errorf("pdf: %w", err)
	}
	return &file{name: fileName(w.Title, asPDF), data: data}, nil
}

func (m *Mind) send(ctx context.Context, p perception.Percept, text string, f *file) error {
	if f == nil {
		return m.Replier.Reply(ctx, p, text)
	}
	return m.Filer.SendFile(ctx, p, f.name, f.data, text)
}

// attachedLine marks, in what was said, the file that went with it.
func attachedLine(name string) string { return "\n\n📎 " + name }

// sendWith is the file a reply goes with: a work in its note, by ref; nil if none.
func (m *Mind) sendWith(p perception.Percept, s system2.Send, refs map[string]memory.Work) *file {
	if s.Work == "" {
		return nil
	}
	log := m.Log.With("percept", p.ID.String(), "work", s.Work, "as", s.As)
	w, ok := refs[s.Work]
	if !ok || !m.canSendAs(s.As) {
		log.Warn("asked to send a work it can't; the reply goes alone")
		return nil
	}
	f, err := m.makeFile(s.As, w)
	if err != nil {
		log.Error("making the file failed; the reply goes alone", "err", err)
		return nil
	}
	log.Info("sending a work with the reply", "title", w.Title, "file", f.name)
	return f
}

// fileName is title as a file name, like "clinic-outreach-plan.pdf".
func fileName(title, ext string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= 60 {
			break
		}
	}
	return orDefault(b.String(), "work") + "." + ext
}
