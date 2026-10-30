package mind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

const learningJSON = `{"title":"Lagos Rain, verse 2","kind":"lyrics","text":"Rain on the danfo roof...",
	"project_notes":"","project_done":false,"remember":[],
	"lessons":["Start the hook on the downbeat"," ","Two","Three"],"ask_for_review":true,"review_question":"Is the hook too wordy?"}`

func learningMind() (*Mind, *fakeStore, *fakeSystem2, *fakeReplier, memory.Intention) {
	m, s, s2, _, i := activityMind()
	r := &fakeReplier{}
	m.Replier = r
	s2.formats["activity"] = learningJSON
	m.Autonomy.Owner = owner
	s.people[owner] = memory.Person{Key: owner, Name: "Busta", Circle: "family"}
	s.convs = []memory.Conversation{{Person: owner, Source: "test", Thread: "chat-owner", LastActivity: at(28, 9, 0)}}
	s.skills = []memory.Skill{{Name: "lyric writing", Level: 4, ShownBy: []string{"lyrics"}}}
	return m, s, s2, r, i
}

func TestItLearnsFromItsOwnWorkAndAsksForReview(t *testing.T) {
	m, s, s2, r, i := learningMind()
	m.tick(context.Background(), at(29, 10, 0))

	if len(s.lessons) != 2 || s.lessons[0].Text != "Start the hook on the downbeat" || s.lessons[0].Source != memory.LessonFromSelf ||
		s.lessons[0].Skill != "lyric writing" || s.lessons[0].WorkID.IsZero() {
		t.Fatalf("lessons = %+v; want the first two non-empty ones, its own", s.lessons)
	}
	if len(r.sent) != 1 || !strings.HasSuffix(r.sent[0], "Lagos Rain, verse 2\n\nRain on the danfo roof...") {
		t.Fatalf("sent = %q", r.sent)
	}
	if note := s2.requests[len(s2.requests)-1].Note; !strings.Contains(note, "your question: Is the hook too wordy?") {
		t.Errorf("ask note:\n%s", note)
	}
	if len(s.reviewAsked) != 1 {
		t.Errorf("review asked = %v", s.reviewAsked)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone {
		t.Errorf("intention = %+v", got)
	}

	next := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, Text: "Write verse 3",
		DueAt: at(29, 15, 0), Status: memory.IntentionPending}
	s.intentions = append(s.intentions, next)
	sent := len(r.sent)
	m.tick(context.Background(), at(29, 15, 0))
	var practice string
	for _, req := range s2.requests {
		if req.Format != nil && req.Format.Name == "activity" {
			practice = req.Note
		}
	}
	if !strings.Contains(practice, "Things you've learned the hard way") || !strings.Contains(practice, "- Start the hook on the downbeat") {
		t.Errorf("practice note:\n%s", practice)
	}
	if len(r.sent) != sent {
		t.Errorf("asked for review twice in a day")
	}
}

func TestTheOwnersReviewBecomesLessonsOnThatWork(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{
		lessons:  []system2.LessonLearned{{Skill: "Lyric Writing", Text: "Cut the hook to six words"}, {Skill: "juggling", Text: "x"}},
		feedback: []system2.Feedback{{Skill: "lyric writing", Level: 3, About: "the hook"}},
	})
	s.skills = []memory.Skill{{Name: "lyric writing", Level: 4, ShownBy: []string{"lyrics"}}}
	work := memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Hook"}
	s.works = append(s.works, work)
	s.reviewAsked = map[ulid.ULID]time.Time{work.ID: now.Add(-time.Hour)}

	p := percept("1", "The hook is too long, cut it to six words")
	p.Speaker, p.SpeakerName = owner, "Busta"
	m.handle(context.Background(), p)
	if len(s.lessons) != 1 || s.lessons[0].Source != memory.LessonFromOwner || s.lessons[0].WorkID != work.ID || s.lessons[0].Skill != "lyric writing" {
		t.Errorf("lessons = %+v", s.lessons)
	}
	if len(s.evidence) != 1 || s.evidence[0].WorkID != work.ID {
		t.Errorf("evidence = %+v; want the feedback tied to the work it asked about", s.evidence)
	}

	q := percept("2", "Make it longer")
	m.handle(context.Background(), q)
	if len(s.lessons) != 1 {
		t.Errorf("took a lesson from someone who isn't the owner")
	}
}

func TestTheReviewTidiesLessons(t *testing.T) {
	old := memory.Lesson{ID: ulid.Make(), Skill: "music production", Text: "Leave space after the drop", Source: memory.LessonFromOwner}
	review := `{"title":"t","text":"x","goals":[],"new_goals":[],"projects":[],"dreams":[],
		"retire_lessons":["` + old.ID.String() + `","nope"],"new_lessons":[{"skill":"Music Production","lesson":"Build to the drop over eight bars"}]}`
	m, s, s2, _ := reviewMind(review)
	s.skills = []memory.Skill{{Name: "music production", Level: 4}}
	s.lessons = []memory.Lesson{old}
	if err := m.Review(context.Background(), at(25, 20, 0)); err != nil {
		t.Fatal(err)
	}
	if note := s2.requests[0].Note; !strings.Contains(note, "[lesson "+old.ID.String()+"] music production, from your owner's correction: Leave space") {
		t.Errorf("note:\n%s", note)
	}
	if s.lessons[0].RetiredAt.IsZero() || len(s.lessons) != 2 || s.lessons[1].Text != "Build to the drop over eight bars" {
		t.Errorf("lessons = %+v", s.lessons)
	}
}

func TestTheOwnersTaste(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{tastes: []system2.Taste{
		{Kind: "writing", Likes: true, Text: "hooks built on one repeated word"},
		{Kind: "juggling", Likes: false, Text: "long intros"}, // not a kind it makes: about any work
	}})
	work := memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Hook"}
	s.works = append(s.works, work)
	s.reviewAsked = map[ulid.ULID]time.Time{work.ID: now.Add(-time.Hour)}
	p := percept("1", "I love when a hook repeats one word. Not into long intros though")
	p.Speaker, p.SpeakerName = owner, "Busta"
	m.handle(context.Background(), p)
	if len(s.tastes) != 2 || s.tastes[0].Kind != "writing" || !s.tastes[0].Likes || s.tastes[0].WorkID != work.ID ||
		s.tastes[0].Source != memory.TasteFromTelegram || s.tastes[1].Kind != "" || s.tastes[1].Likes {
		t.Fatalf("tastes = %+v", s.tastes)
	}
	m.handle(context.Background(), percept("2", "I like long intros"))
	if len(s.tastes) != 2 {
		t.Error("took someone else's taste")
	}
	note := m.tasteNote(context.Background())
	for _, want := range []string{"What Busta likes and doesn't in your work", "- writing, likes: hooks built on one repeated word",
		"- doesn't like: long intros"} {
		if !strings.Contains(note, want) {
			t.Errorf("note is missing %q:\n%s", want, note)
		}
	}
}
