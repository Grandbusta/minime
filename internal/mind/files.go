package mind

import (
	"context"
	"errors"
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

// canSendAs is false for a format made from data no craft of its life carries.
func (m *Mind) canSendAs(as string) bool {
	f, ok := m.Formats.Get(as)
	return ok && m.Filer != nil && (f.Needs() == "" || len(m.Life.Carrying(f.Needs())) > 0)
}

func (m *Mind) filesHelp(n names) string {
	var kinds []string
	for _, f := range m.Formats.All() {
		if !m.canSendAs(f.Name()) {
			continue
		}
		k := fmt.Sprintf(`%s ("%s")`, f.Describe(), f.Name())
		if f.Needs() != "" {
			k += ", for your " + strings.Join(m.Life.Carrying(f.Needs()), ", ")
		}
		kinds = append(kinds, k)
	}
	if len(kinds) == 0 {
		return `- You can't send files: leave "as" and send empty.`
	}
	return fmt.Sprintf(`- You can send things as files: %[2]s. If %[1]s wants one of those files of something you already made (the note `+
		`lists your works with refs like [w1]), send it now with your reply: its ref in send.work and the file's name in send.as. Your `+
		`reply goes with it as its caption, so say what it is, briefly. Promise it instead only if they want it at a later time, `+
		`with its ref in the promise's "work".`+
		"\n"+`- If %[1]s wants something new made as one of those files, promise it with the file's name in "as". `+
		`Otherwise leave "as" and send empty.`, n.speaker, strings.Join(kinds, "; "))
}

func (m *Mind) fileNote(i memory.Intention) string {
	f, ok := m.Formats.Get(i.Args["as"])
	if !ok {
		return ""
	}
	var how []string
	if h := f.WriteHelp(); h != "" {
		how = append(how, h)
	}
	if f.Needs() != "" {
		how = append(how, fmt.Sprintf("make it a %s and write its %s in full", strings.Join(m.Life.Carrying(f.Needs()), " or "), f.Needs()))
	}
	note := "\n\nIt goes to them as " + f.Describe()
	if len(how) > 0 {
		note += ", so " + strings.Join(how, "; and ")
	}
	return note + "."
}

// makeFile is w as a file; nil if it can't send it as one, or w doesn't
// carry what that file is made from yet (scoreOldMusic fills scores in,
// never a reply).
func (m *Mind) makeFile(as string, w memory.Work) (*render.File, error) {
	if !m.canSendAs(as) {
		return nil, nil
	}
	f, err := m.Formats.File(as, w, m.Life.Name)
	if errors.Is(err, render.ErrNoData) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// dataFile is w as the first format, in the Set's order, made from data it
// carries (like a beat's MIDI, so it can be heard); nil if there is none.
func (m *Mind) dataFile(w memory.Work) (*render.File, error) {
	for _, f := range m.Formats.For(w) {
		if f.Needs() != "" {
			return m.makeFile(f.Name(), w)
		}
	}
	return nil, nil
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
		log.Warn("asked to send a work as a file it can't be yet; the reply goes alone", "kind", w.Kind)
		return nil
	}
	log.Info("sending a work with the reply", "title", w.Title, "file", f.Name)
	return f
}
