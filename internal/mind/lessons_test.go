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
	m, s, _, r, i := learningMind()
	m.tick(context.Background(), at(29, 10, 0))

	if len(s.lessons) != 2 || s.lessons[0].Text != "Start the hook on the downbeat" || s.lessons[0].Source != memory.LessonFromSelf ||
		s.lessons[0].Skill != "lyric writing" || s.lessons[0].WorkID.IsZero() {
		t.Fatalf("lessons = %+v; want the first two non-empty ones, its own", s.lessons)
	}
	if len(r.sent) != 1 || !strings.HasSuffix(r.sent[0], "Lagos Rain, verse 2\n\nRain on the danfo roof...") {
		t.Fatalf("sent = %q", r.sent)
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
