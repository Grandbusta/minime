package midi

import "github.com/Grandbusta/minime/internal/memory"

type Format struct{}

func (Format) Name() string      { return "midi" }
func (Format) Ext() string       { return "mid" }
func (Format) MediaType() string { return "audio/midi" }
func (Format) Describe() string  { return "a MIDI file that plays in any music app" }
func (Format) WriteHelp() string { return "" }
func (Format) Needs() string     { return memory.DataScore }

func (Format) Render(w memory.Work, _ string) ([]byte, error) { return Render(w.Title, w.Score) }
