package pdf

import (
	"bytes"
	"testing"
	"time"
)

const sample = "Hi [Name],\n\nI’m exploring how clinics — in Lagos — book. Fees start at ₦5,000 ✓.\n\n" +
	"# A plan\n\n1. Call **three** clinics\n   - keep it short\n\n| Day | Cost |\n|-----|------|\n| Mon | ₦0 |\n\n" +
	"```\nkick . . . snare\n```\n"

func TestRender(t *testing.T) {
	b, err := Render(Doc{Title: "Clinic outreach: a plan", Author: "Daniel", Text: sample, At: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) || !bytes.Contains(b, []byte("%%EOF")) {
		t.Fatalf("not a pdf: %q", b[:min(20, len(b))])
	}
}
