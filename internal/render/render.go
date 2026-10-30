// Package render turns a work into a file to send: a PDF of its text, or a
// MIDI file of its score.
package render

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/render/midi"
	"github.com/Grandbusta/minime/internal/render/pdf"
)

const (
	PDF  = "pdf"
	MIDI = "midi"
)

var ErrNoScore = errors.New("render: the work has no score")

type File struct {
	Name string
	Type string // media type
	Data []byte
}

func Work(format string, w memory.Work, author string) (File, error) {
	switch format {
	case PDF:
		data, err := pdf.Render(pdf.Doc{Title: w.Title, Author: author, Text: w.Text, At: w.CreatedAt})
		if err != nil {
			return File{}, err
		}
		return File{Name: FileName(w.Title, "pdf"), Type: "application/pdf", Data: data}, nil
	case MIDI:
		if len(w.Score) == 0 {
			return File{}, ErrNoScore
		}
		data, err := midi.Render(w.Title, w.Score)
		if err != nil {
			return File{}, err
		}
		return File{Name: FileName(w.Title, "mid"), Type: "audio/midi", Data: data}, nil
	}
	return File{}, fmt.Errorf("render: unknown format %q", format)
}

// FileName is title as a file name with ext, like "clinic-outreach-plan.pdf".
func FileName(title, ext string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if b.Len() >= 60 {
			break
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			dash = true
			continue
		}
		if dash && b.Len() > 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
		dash = false
	}
	if b.Len() == 0 {
		return "work." + ext
	}
	return b.String() + "." + ext
}
