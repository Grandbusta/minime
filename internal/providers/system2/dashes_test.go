package system2

import (
	"context"
	"encoding/json"
	"testing"
)

type dashy struct{}

func (dashy) Reason(context.Context, Request) (Response, error) {
	return Response{Text: `{"text":"Lagos traffic—my guy"}`, WhoTheyAre: "Sam—Tobi's cousin",
		Remember:  []Memory{{About: AboutSelf, Text: "Tobi likes jazz—a lot"}},
		Reminders: []Reminder{{When: "x", What: "call mum—today"}},
		Promises:  []Promise{{By: "x", What: "a verse—funny"}}}, nil
}

func TestWithoutEmDashes(t *testing.T) {
	resp, err := WithoutEmDashes(dashy{}).Reason(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Text string }
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil || out.Text != "Lagos traffic, my guy" {
		t.Errorf("text = %q, %v", resp.Text, err)
	}
	if resp.WhoTheyAre != "Sam, Tobi's cousin" || resp.Remember[0].Text != "Tobi likes jazz, a lot" ||
		resp.Reminders[0].What != "call mum, today" || resp.Promises[0].What != "a verse, funny" {
		t.Errorf("resp = %+v", resp)
	}
}
