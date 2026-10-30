// Package render turns a work into a file to send. Each kind of file is a
// Format: adding one is a type that implements it and a line in main that
// adds it to the Set; the mind and the console offer whatever the Set has.
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/Grandbusta/minime/internal/memory"
)

type Format interface {
	Name() string // what a promise's "as" and a reply's send.as say, e.g. "pdf"
	Ext() string
	MediaType() string
	// Describe is how the person knows the file, like "a PDF document, to
	// print, attach or pass on"; WriteHelp is how to write a work meant to go
	// as one, or "".
	Describe() string
	WriteHelp() string
	// Needs is the data a work must carry to be this file (memory.Work.Carries);
	// "" if its words are enough. A format that needs data is a DataFormat.
	Needs() string
	Render(w memory.Work, author string) ([]byte, error)
}

// DataFormat is a format made from data a work carries beside its words,
// which it defines: a craft that names the data (life.yaml's data) gets it
// written with each work.
type DataFormat interface {
	Format
	// DataSchema is the data for Structured Outputs; DataHelp tells System 2
	// how to write it.
	DataSchema() map[string]any
	DataHelp() string
	// CleanData keeps what can be used of raw; an error if nothing can.
	CleanData(raw json.RawMessage) (json.RawMessage, error)
}

type File struct {
	Name string
	Type string // media type
	Data []byte
}

var ErrNoData = errors.New("render: the work doesn't carry what this file is made from")

// A nil Set has no formats.
type Set struct {
	byName map[string]Format
	order  []Format
}

func NewSet(formats ...Format) *Set {
	s := &Set{byName: map[string]Format{}}
	for _, f := range formats {
		if _, dup := s.byName[f.Name()]; dup {
			panic("render: two formats named " + f.Name())
		}
		s.byName[f.Name()] = f
		s.order = append(s.order, f)
	}
	return s
}

func (s *Set) Get(name string) (Format, bool) {
	if s == nil {
		return nil, false
	}
	f, ok := s.byName[name]
	return f, ok
}

// All is in the order NewSet was given.
func (s *Set) All() []Format {
	if s == nil {
		return nil
	}
	return s.order
}

// Data is the format that defines data of kind.
func (s *Set) Data(kind string) (DataFormat, bool) {
	for _, f := range s.All() {
		if df, ok := f.(DataFormat); ok && f.Needs() == kind {
			return df, true
		}
	}
	return nil, false
}

// DataKinds is the data its formats are made from, each once, in the order
// NewSet was given.
func (s *Set) DataKinds() []string {
	var out []string
	for _, f := range s.All() {
		if _, ok := f.(DataFormat); ok && !slices.Contains(out, f.Needs()) {
			out = append(out, f.Needs())
		}
	}
	return out
}

// For is the formats w can be made into now.
func (s *Set) For(w memory.Work) []Format {
	var out []Format
	for _, f := range s.All() {
		if w.Carries(f.Needs()) {
			out = append(out, f)
		}
	}
	return out
}

func (s *Set) File(format string, w memory.Work, author string) (File, error) {
	f, ok := s.Get(format)
	if !ok {
		return File{}, fmt.Errorf("render: unknown format %q", format)
	}
	if !w.Carries(f.Needs()) {
		return File{}, ErrNoData
	}
	data, err := f.Render(w, author)
	if err != nil {
		return File{}, fmt.Errorf("render %s: %w", format, err)
	}
	return File{Name: FileName(w.Title, f.Ext()), Type: f.MediaType(), Data: data}, nil
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
