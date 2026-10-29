package mind

import (
	"context"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"minime/internal/action"
	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

func from(speaker string) perception.Percept {
	p := percept(ulid.Make().String(), "...")
	p.Speaker = speaker
	return p
}

func TestCorrectingFacts(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	ikoyi := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "Sam lives in Ikoyi"}
	golf := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "Sam drives a Golf"}
	age := memory.Fact{ID: ulid.Make(), Subject: memory.SubjectSelf, Text: "Tobi is 19"}
	s.facts = []memory.Fact{ikoyi, golf, age}
	refs := map[string]memory.Fact{"f1": ikoyi, "f2": golf, "f3": age}
	reply := &decision.Decision{ID: ulid.Make()}
	n := names{agent: "Tobi", speaker: "Sam"}

	ds := m.correctFacts(context.Background(), from(sam), reply, refs, []system2.Correction{
		{Fact: "f1", Text: "Sam lives in Lekki"}, {Fact: "f2", Forget: true},
		{Fact: "f3", Text: "Tobi is 18"}, // not Sam's to correct
		{Fact: "f9", Forget: true},       // not shown
	}, n)
	var texts []string
	for _, f := range s.facts {
		texts = append(texts, f.Text)
	}
	if got := strings.Join(texts, "|"); got != "Sam lives in Lekki|Tobi is 19" {
		t.Errorf("facts = %s", got)
	}
	if len(ds) != 2 || ds[0].Action != "correct_fact" || ds[1].Action != "forget_fact" || *ds[0].ParentID != reply.ID ||
		strings.Contains(ds[1].Outcome.Detail, "Golf") {
		t.Errorf("decisions = %+v / %+v", ds[0], ds[1])
	}

	// The owner may correct what it knows about itself.
	ds = m.correctFacts(context.Background(), from(owner), reply, map[string]memory.Fact{"f1": age},
		[]system2.Correction{{Fact: "f1", Text: "Tobi is 18"}}, n)
	if len(ds) != 1 || s.facts[0].Text != "Tobi is 18" || s.facts[0].Subject != memory.SubjectSelf {
		t.Errorf("owner's correction: %d decisions, facts %+v", len(ds), s.facts)
	}
}

func TestTheOwnerChangesPermissionsByChat(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	reply := &decision.Decision{ID: ulid.Make()}
	changes := []system2.Permission{{Class: "reach_out", Verdict: "auto"}, {Class: "spend", Verdict: "auto"},
		{Class: "read_web", Verdict: "ask"}, {Class: "publish", Verdict: "ask"}}

	if ds := m.changePermissions(context.Background(), from(sam), reply, changes); len(ds) != 0 || len(s.rules) != 0 {
		t.Fatal("someone who isn't the owner changed permissions")
	}
	ds := m.changePermissions(context.Background(), from(owner), reply, changes)
	if len(ds) != 1 || ds[0].Outcome.Detail != "reach_out: ask → auto" || s.rules["reach_out"] != "auto" || len(s.rules) != 1 {
		t.Errorf("decisions %+v, rules %v; want only reach_out changed (spend is config-only, read_web can't ask, publish already asks)",
			ds, s.rules)
	}
	if m.Autonomy.Check(action.ReachOut, sam) != action.Auto {
		t.Error("the change didn't apply")
	}

	// It lasts across a restart.
	restarted, _, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	restarted.Store = s
	restarted.loadPermissions(context.Background())
	if restarted.Autonomy.Check(action.ReachOut, sam) != action.Auto || restarted.Autonomy.Check(action.Commit, sam) != action.Ask {
		t.Errorf("after a restart: %v", restarted.Autonomy.Classes)
	}
}

func TestPermissionChangesAreDecisionsAfterTheReply(t *testing.T) {
	s2 := &fakeSystem2{perms: []system2.Permission{{Class: "commit", Verdict: "auto"}}}
	m, s, _ := gatedMind(&fakeReplier{}, s2)
	p := percept("1", "You can say yes to people without asking me")
	p.Speaker, p.SpeakerName = owner, "Busta"
	m.handle(context.Background(), p)

	if note := s2.requests[0].Note; !strings.Contains(note, "commit: ask") || !strings.Contains(note, "reach_out: ask") {
		t.Errorf("the owner's note doesn't say what it may do:\n%s", note)
	}
	if ins := s2.requests[0].Instructions; m.ReplyPrompt.Text != "" && strings.Contains(ins, "{permissions_help}") {
		t.Error("placeholder left in the instructions")
	}
	last, reply := s.decisions[len(s.decisions)-1], s.decisions[len(s.decisions)-2]
	if last.Action != "permission" || reply.Action != "reply" || *last.ParentID != reply.ID || reply.Reply != "a thoughtful reply" {
		t.Errorf("reply %+v, then %+v", reply, last)
	}
}

func TestPermissionsHelpIsOnlyForTheOwner(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	n := names{agent: "Tobi", speaker: "Busta"}
	if help := m.permissionsHelp(s.people[owner], n); !strings.Contains(help, "put it in `permissions`") {
		t.Errorf("owner: %q", help)
	}
	if help := m.permissionsHelp(memory.Person{Key: sam}, n); help != "" || m.permissionsNote(from(sam)) != "" {
		t.Errorf("someone else got the permissions help: %q", help)
	}
}

func TestAMisreadIsLabelledForReplay(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	reply := &decision.Decision{ID: ulid.Make()}
	s.lastRead = ulid.Make()
	if ds := m.labelMisread(context.Background(), from(sam), reply, "question"); len(ds) != 0 {
		t.Error("labelled from someone who isn't the owner")
	}
	if ds := m.labelMisread(context.Background(), from(owner), reply, "fly"); len(ds) != 0 {
		t.Error("labelled with a choice that doesn't exist")
	}
	ds := m.labelMisread(context.Background(), from(owner), reply, "question")
	if len(ds) != 1 || len(s.labels) != 1 || s.labels[0].DecisionID != s.lastRead || s.labels[0].Question != "intent" ||
		s.labels[0].Value != "question" || s.labels[0].Source != memory.LabelFromChat {
		t.Errorf("labels = %+v", s.labels)
	}
}
