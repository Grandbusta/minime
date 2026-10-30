package calendar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

func TestRender(t *testing.T) {
	w := memory.Work{ID: ulid.Make(), Title: "Outreach week", CreatedAt: time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC), DataKind: "events",
		Data: json.RawMessage(`{"events":[
		{"title":"Call Smile Dental","start":"2026-11-02T10:00","end":"2026-11-02T10:30","location":"Lekki","notes":"Ask how bookings work"},
		{"title":"Write up the calls","start":"2026-11-03T09:00","end":"2026-11-03T08:00","location":"","notes":""},
		{"title":"Rest day","start":"2026-11-04","end":"","location":"","notes":""},
		{"title":"","start":"2026-11-05T09:00","end":"","location":"","notes":""},
		{"title":"Someday","start":"next week","end":"","location":"","notes":""}]}`)}
	data, err := Format{}.Render(w, "Daniel")
	if err != nil {
		t.Fatal(err)
	}
	cal, err := ics.ParseCalendar(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	evs := cal.Events()
	if len(evs) != 3 {
		t.Fatalf("%d events", len(evs))
	}
	text := string(data)
	for _, want := range []string{"DTSTART:20261102T100000", "DTEND:20261102T103000", "LOCATION:Lekki",
		"DTSTART:20261103T090000", "DTEND:20261103T100000", // a bad end: an hour
		"DTSTART;VALUE=DATE:20261104", "DTEND;VALUE=DATE:20261105", "UID:" + w.ID.String() + "-0@minime"} {
		if !strings.Contains(text, want) {
			t.Errorf("calendar lacks %s:\n%s", want, text)
		}
	}
}
