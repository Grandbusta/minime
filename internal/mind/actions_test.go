package mind

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// fakeDoer writes a file: its args need a path.
type fakeDoer struct {
	name        string
	class       action.Class
	destructive bool
	done        []string
}

func (f *fakeDoer) Name() string        { return f.name }
func (f *fakeDoer) Describe() string    { return "write a file" }
func (f *fakeDoer) Class() action.Class { return f.class }
func (f *fakeDoer) Destructive() bool   { return f.destructive }
func (f *fakeDoer) Check(args string) error {
	var a struct{ Path string }
	if json.Unmarshal([]byte(args), &a) != nil || a.Path == "" {
		return errors.New("needs a path")
	}
	return nil
}
func (f *fakeDoer) Do(_ context.Context, args string, _ time.Time) (string, error) {
	f.done = append(f.done, args)
	return "Wrote plan.md", nil
}

func fromOwner(text string) perception.Percept {
	return perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: "chat-owner",
		Speaker: owner, SpeakerName: "Busta", Kind: "message", Text: text, At: now}
}

func actionMind(d *fakeDoer, acts ...system2.Act) (*Mind, *fakeStore, *fakeAsker, *fakeReplier, *fakeSystem2) {
	r, s2 := &fakeReplier{}, &fakeSystem2{acts: acts}
	m, s, asker := gatedMind(r, s2)
	m.Doers, m.ActionsPerDay = []Doer{d}, 5
	m.ReplyPrompt.Text += "\n{actions}"
	return m, s, asker, r, s2
}

var save = system2.Act{Tool: "notes.write_file", Args: `{"path":"plan.md","content":"the plan"}`, Why: "Busta asked me to save the plan"}

func TestAnApprovedActionRunsOnTheNextTick(t *testing.T) {
	d := &fakeDoer{name: "notes.write_file", class: action.Write}
	m, s, asker, r, s2 := actionMind(d, save)
	m.handle(context.Background(), fromOwner("Save that plan to my folder"))

	if ins := s2.requests[0].Instructions; !strings.Contains(ins, "notes.write_file: write a file") || !strings.Contains(ins, "never say it's done") {
		t.Errorf("not told about its tools:\n%s", ins)
	}
	if len(s.actions) != 1 || s.actions[0].Status != memory.ActionProposed || len(d.done) != 0 {
		t.Fatalf("actions = %+v, done %d", s.actions, len(d.done))
	}
	if len(asker.asked) != 1 || !strings.Contains(asker.asked[0], "Can I use notes.write_file?") || !strings.Contains(asker.asked[0], `"path":"plan.md"`) {
		t.Fatalf("asked = %q", asker.asked)
	}

	m.handle(context.Background(), tap(owner, asker.ids[0], true))
	if len(d.done) != 0 || s.actions[0].Status != memory.ActionApproved {
		t.Fatalf("ran while the reply loop handled the tap, or not approved: %+v", s.actions[0])
	}
	m.tick(context.Background(), now.Add(time.Minute))
	if len(d.done) != 1 || s.actions[0].Status != memory.ActionDone || s.actions[0].Result != "Wrote plan.md" {
		t.Fatalf("after the tick: done %d, %+v", len(d.done), s.actions[0])
	}
	if last := r.sent[len(r.sent)-1]; !strings.HasPrefix(last, "✅ Done: notes.write_file (Busta asked me to save the plan)") {
		t.Errorf("owner told %q", last)
	}
	m.tick(context.Background(), now.Add(2*time.Minute))
	if len(d.done) != 1 {
		t.Error("ran twice")
	}
}

func TestADeniedActionNeverRuns(t *testing.T) {
	d := &fakeDoer{name: "notes.write_file", class: action.Write}
	m, s, asker, _, _ := actionMind(d, save)
	m.handle(context.Background(), fromOwner("Save that plan"))
	m.handle(context.Background(), tap(owner, asker.ids[0], false))
	m.tick(context.Background(), now.Add(time.Minute))
	if len(d.done) != 0 || s.actions[0].Status != memory.ActionDenied {
		t.Errorf("done %d, %+v", len(d.done), s.actions[0])
	}
}

func TestTheGateDecidesWhatAsks(t *testing.T) {
	for name, c := range map[string]struct {
		verdict     action.Verdict
		destructive bool
		asked, ran  bool
	}{
		"auto runs":              {action.Auto, false, false, true},
		"destructive still asks": {action.Auto, true, true, false},
		"forbidden is not used":  {action.Forbid, false, false, false},
	} {
		d := &fakeDoer{name: "notes.write_file", class: action.Write, destructive: c.destructive}
		m, s, asker, _, s2 := actionMind(d, save)
		m.Autonomy.Classes[action.Write] = c.verdict
		m.handle(context.Background(), fromOwner("Save that plan"))
		m.tick(context.Background(), now.Add(time.Minute))
		if (len(asker.asked) > 0) != c.asked || (len(d.done) > 0) != c.ran {
			t.Errorf("%s: asked %d, ran %d, actions %+v", name, len(asker.asked), len(d.done), s.actions)
		}
		if c.verdict == action.Forbid && strings.Contains(s2.requests[0].Instructions, "notes.write_file") {
			t.Errorf("%s: offered a forbidden tool", name)
		}
	}
}

func TestOnlyGoodProposalsFromTheOwnerAreKept(t *testing.T) {
	d := &fakeDoer{name: "notes.write_file", class: action.Write}
	bad := []system2.Act{{Tool: "notes.delete_all", Args: `{}`}, {Tool: "notes.write_file", Args: `{"content":"no path"}`}}
	m, s, _, _, s2 := actionMind(d, bad...)
	m.handle(context.Background(), fromOwner("Save that plan"))
	if len(s.actions) != 0 {
		t.Errorf("kept %+v", s.actions)
	}

	s2.acts = []system2.Act{save}
	m.handle(context.Background(), percept("x", "Save my plan to Busta's folder")) // Sam, not the owner
	if len(s.actions) != 0 {
		t.Errorf("someone else had it act: %+v", s.actions)
	}

	m.ActionsPerDay = 1
	s2.acts = []system2.Act{save, save}
	m.handle(context.Background(), fromOwner("Save it twice"))
	if len(s.actions) != 1 {
		t.Errorf("over the cap: %d actions", len(s.actions))
	}
}
