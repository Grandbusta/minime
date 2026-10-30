package mind

import (
	"context"
	"fmt"
	"strings"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/render"
)

type Filer interface {
	SendFile(ctx context.Context, p perception.Percept, name string, data []byte, caption string) error
}

// canSendAs is true for a PDF of any work, and MIDI if it makes music.
func (m *Mind) canSendAs(as string) bool {
	return m.Filer != nil && (as == render.PDF || as == render.MIDI && len(m.scored()) > 0)
}

func (m *Mind) filesHelp(n names) string {
	if !m.canSendAs(render.PDF) {
		return `- You can't send files: leave "as" and send empty.`
	}
	formats := `as a PDF document ("pdf")`
	if m.canSendAs(render.MIDI) {
		formats += fmt.Sprintf(`, and your music (%s) as a MIDI file ("midi") that plays in any music app`, strings.Join(m.scored(), ", "))
	}
	return fmt.Sprintf(`- You can send things %[2]s. If %[1]s wants one of those files of something you already made (the note `+
		`lists your works with refs like [w1]), send it now with your reply: its ref in send.work and the format in send.as. Your `+
		`reply goes with it as its caption, so say what it is, briefly. Promise it instead only if they want it at a later time, `+
		`with its ref in the promise's "work".`+
		"\n"+`- If %[1]s wants something new made as one of those files, or a PDF to print, attach or pass on, promise it with `+
		`the format in "as". Otherwise leave "as" and send empty.`, n.speaker, formats)
}

func (m *Mind) fileNote(i memory.Intention) string {
	switch i.Args["as"] {
	case render.PDF:
		return "\n\nIt goes to them as a PDF document, so write it as one: the title is printed above it, so don't repeat it; " +
			"use Markdown headings, and lists or tables where they help; no chat talk around it."
	case render.MIDI:
		return fmt.Sprintf("\n\nIt goes to them as a MIDI file, so make it a %s and write its score in full.",
			strings.Join(m.scored(), " or "))
	}
	return ""
}

// makeFile is w as a file; nil if it can't send it as one, or it is music
// not scored yet (scoreOldMusic scores it, never a reply).
func (m *Mind) makeFile(as string, w memory.Work) (*render.File, error) {
	if !m.canSendAs(as) || as == render.MIDI && (!m.isMusic(w.Kind) || len(w.Score) == 0) {
		return nil, nil
	}
	f, err := render.Work(as, w, m.Life.Name)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func (m *Mind) send(ctx context.Context, p perception.Percept, text string, f *render.File) error {
	if f == nil {
		return m.Replier.Reply(ctx, p, text)
	}
	return m.Filer.SendFile(ctx, p, f.Name, f.Data, text)
}

// attachedLine marks, in what was said, the file that went with it.
func attachedLine(name string) string { return "\n\n📎 " + name }

func (m *Mind) sendWith(p perception.Percept, s system2.Send, refs map[string]memory.Work) *render.File {
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
	switch {
	case err != nil:
		log.Error("making the file failed; the reply goes alone", "err", err)
		return nil
	case f == nil:
		log.Warn("asked to send a work as a file it can't be yet (not music, or not scored); the reply goes alone", "kind", w.Kind)
		return nil
	}
	log.Info("sending a work with the reply", "title", w.Title, "file", f.Name)
	return f
}
