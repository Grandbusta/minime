// Package calendar is data a work carries: dated events, made into an
// iCalendar (.ics) file that adds them to any calendar app.
package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"

	"github.com/Grandbusta/minime/internal/memory"
)

type Event struct {
	Title    string `json:"title"`
	Start    string `json:"start"` // local "2006-01-02T15:04", or "2006-01-02" all day
	End      string `json:"end"`   // the same; "" for an hour after the start, or one day
	Location string `json:"location"`
	Notes    string `json:"notes"`
}

type Events struct {
	Events []Event `json:"events"`
}

const maxEvents = 200

var ErrEmpty = errors.New("calendar: no events with a title and a start")

// Parse reads events as JSON and keeps those with a title and a start it
// can read; an end that isn't after the start is dropped.
func Parse(raw []byte) (Events, error) {
	var in Events
	if err := json.Unmarshal(raw, &in); err != nil {
		return Events{}, fmt.Errorf("calendar: %w", err)
	}
	var out Events
	for _, e := range in.Events {
		e.Title, e.Start, e.End = strings.TrimSpace(e.Title), strings.TrimSpace(e.Start), strings.TrimSpace(e.End)
		e.Location, e.Notes = strings.TrimSpace(e.Location), strings.TrimSpace(e.Notes)
		start, allDay, ok := when(e.Start)
		if e.Title == "" || !ok || len(out.Events) == maxEvents {
			continue
		}
		if end, endAllDay, ok := when(e.End); !ok || endAllDay != allDay || !end.After(start) && !(allDay && end.Equal(start)) {
			e.End = ""
		}
		out.Events = append(out.Events, e)
	}
	if len(out.Events) == 0 {
		return Events{}, ErrEmpty
	}
	return out, nil
}

func when(s string) (t time.Time, allDay, ok bool) {
	if t, err := time.Parse("2006-01-02T15:04", s); err == nil {
		return t, false, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true, true
	}
	return time.Time{}, false, false
}

type Format struct{}

func (Format) Name() string      { return "ics" }
func (Format) Ext() string       { return "ics" }
func (Format) MediaType() string { return "text/calendar" }
func (Format) Describe() string  { return "a calendar file that adds the events to their calendar" }
func (Format) WriteHelp() string { return "" }
func (Format) Needs() string     { return "events" }

func (Format) DataHelp() string {
	return "every dated event in it, each with a short title, its start and end in local time as YYYY-MM-DDTHH:MM (or " +
		"YYYY-MM-DD for all day), where it is if it says, and a line of notes."
}

var schema = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"events"},
	"properties": map[string]any{
		"events": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"title", "start", "end", "location", "notes"},
			"properties": map[string]any{
				"title":    map[string]any{"type": "string"},
				"start":    map[string]any{"type": "string", "description": "local time, YYYY-MM-DDTHH:MM, or YYYY-MM-DD for all day"},
				"end":      map[string]any{"type": "string", "description": "the same, or empty"},
				"location": map[string]any{"type": "string"},
				"notes":    map[string]any{"type": "string"},
			},
		}},
	}}

func (Format) DataSchema() map[string]any { return schema }

func (Format) CleanData(raw json.RawMessage) (json.RawMessage, error) {
	es, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(es)
}

// Render writes the events in floating local time, so they land at the
// hours written wherever the calendar is. Each keeps its UID from the work,
// so opening the file again updates them rather than adding them twice.
func (Format) Render(w memory.Work, author string) ([]byte, error) {
	es, err := Parse(w.Data)
	if err != nil {
		return nil, err
	}
	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.SetProductId("-//minime//" + author + "//EN")
	cal.SetName(w.Title)
	for i, e := range es.Events {
		ev := cal.AddEvent(fmt.Sprintf("%s-%d@minime", w.ID, i))
		ev.SetDtStampTime(w.CreatedAt)
		ev.SetSummary(e.Title)
		if e.Location != "" {
			ev.SetLocation(e.Location)
		}
		if e.Notes != "" {
			ev.SetDescription(e.Notes)
		}
		start, allDay, _ := when(e.Start)
		end, _, ok := when(e.End)
		if allDay {
			if !ok {
				end = start
			}
			ev.SetAllDayStartAt(start)
			ev.SetAllDayEndAt(end.AddDate(0, 0, 1)) // the day after: the end is exclusive
			continue
		}
		if !ok {
			end = start.Add(time.Hour)
		}
		ev.SetProperty(ics.ComponentPropertyDtStart, start.Format("20060102T150405"))
		ev.SetProperty(ics.ComponentPropertyDtEnd, end.Format("20060102T150405"))
	}
	return []byte(cal.Serialize()), nil
}
