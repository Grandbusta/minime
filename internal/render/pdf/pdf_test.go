package pdf

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

const sample = `Option 1
Subject: A quick question about appointment mix-ups

Hi [Name],

I’m exploring how small dental clinics — in Lagos — handle bookings. Fees start at ₦5,000 ✓.

# A plan

## This week

1. Call **three** clinics
2. Write up what they say
   - keep it short

> Ask, don't pitch.

| Day | What | Cost |
|-----|------|------|
| Mon | Calls to the clinics on the Island, then notes | ₦0 |
| Tue | Emails | ₦0 |

---

` + "```\nkick . . . snare . . .\n```\n"

func TestRender(t *testing.T) {
	b, err := Render(Doc{Title: "Clinic outreach: a plan", Author: "Daniel", Text: sample, At: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b, []byte("%%EOF")) {
		t.Fatalf("not a pdf: %q", b[:min(20, len(b))])
	}
	if dir := os.Getenv("PDF_OUT"); dir != "" {
		os.WriteFile(dir+"/sample.pdf", b, 0o644)
	}
}

func TestRenderBreaksPages(t *testing.T) {
	long := strings.Repeat("A paragraph that goes on for a while, about clinics and calls and notes. ", 20) + "\n\n"
	b, err := Render(Doc{Title: "Long", Text: strings.Repeat(long, 12)})
	if err != nil {
		t.Fatal(err)
	}
	if pages := bytes.Count(b, []byte("/Type /Page\n")) + bytes.Count(b, []byte("/Type /Page ")); pages < 3 {
		t.Errorf("%d pages", pages)
	}
}

func TestRenderEmpty(t *testing.T) {
	if _, err := Render(Doc{Title: "Only a title"}); err != nil {
		t.Fatal(err)
	}
}
