package system2

import (
	"context"
	"strings"
)

// WithoutEmDashes wraps r so nothing it writes has an em dash (—): the
// agent's replies, what it makes and remembers, and its profile page. The
// prompts ask for none; this catches the ones a model writes anyway.
func WithoutEmDashes(r Reasoner) Reasoner { return noEmDashes{r} }

type noEmDashes struct{ Reasoner }

func (n noEmDashes) Reason(ctx context.Context, req Request) (Response, error) {
	resp, err := n.Reasoner.Reason(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.Text = RemoveEmDashes(resp.Text)
	resp.WhoTheyAre = RemoveEmDashes(resp.WhoTheyAre)
	for i := range resp.Remember {
		resp.Remember[i].Text = RemoveEmDashes(resp.Remember[i].Text)
	}
	for i := range resp.Reminders {
		resp.Reminders[i].What = RemoveEmDashes(resp.Reminders[i].What)
	}
	for i := range resp.Corrections {
		resp.Corrections[i].Text = RemoveEmDashes(resp.Corrections[i].Text)
	}
	for i := range resp.Promises {
		resp.Promises[i].What = RemoveEmDashes(resp.Promises[i].What)
	}
	return resp, nil
}

// RemoveEmDashes rewrites em dashes the way a person would punctuate
// without them: a comma between words ("Sure—funny" → "Sure, funny"), just
// a space after other punctuation ("Done!— next" → "Done! next"), and
// nothing at the start or end of a line. JSON stays valid: an em dash is
// never part of its syntax.
func RemoveEmDashes(s string) string {
	if !strings.Contains(s, "—") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		parts := strings.Split(line, "—")
		out := parts[0]
		for _, p := range parts[1:] {
			out = strings.TrimRight(out, " \t")
			p = strings.TrimLeft(p, " \t")
			switch {
			case p == "":
				// a dash at the end of the line, or two in a row
			case strings.TrimSpace(out) == "":
				out += p // at the start of the line
			case strings.ContainsAny(out[len(out)-1:], ".,;:!?(\"'"):
				out += " " + p
			default:
				out += ", " + p
			}
		}
		lines[i] = out
	}
	return strings.Join(lines, "\n")
}
