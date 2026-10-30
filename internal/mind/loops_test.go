package mind

import (
	"context"
	"testing"
	"time"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// slowPlan holds the day plan until release is closed, closing started when
// it begins.
type slowPlan struct {
	*fakeSystem2
	started, release chan struct{}
}

func (s slowPlan) Reason(ctx context.Context, req system2.Request) (system2.Response, error) {
	if req.Format != nil && req.Format.Name == "day_plan" {
		close(s.started)
		<-s.release
	}
	return s.fakeSystem2.Reason(ctx, req)
}

// replyStore closes replied when a System 2 reply is saved, the last thing
// handle does, so the fakes are never used by both loops at once.
type replyStore struct {
	*fakeStore
	replied chan struct{}
}

func (s replyStore) SaveDecision(ctx context.Context, d decision.Decision) error {
	err := s.fakeStore.SaveDecision(ctx, d)
	if d.Tier == decision.TierSystem2 && d.Action == "reply" {
		close(s.replied)
	}
	return err
}

func TestRepliesWhileItsDayIsBusy(t *testing.T) {
	inner := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[]}`}}
	m, s := lifeMind(inner)
	plan := slowPlan{fakeSystem2: inner, started: make(chan struct{}), release: make(chan struct{})}
	store := replyStore{fakeStore: s, replied: make(chan struct{})}
	ticks, percepts := make(chan time.Time), make(chan perception.Percept)
	m.System2, m.Store, m.Ticks, m.Percepts = plan, store, ticks, percepts

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopped := make(chan error)
	go func() { stopped <- m.Run(ctx) }()

	ticks <- at(29, 9, 0)
	select {
	case <-plan.started:
	case <-ctx.Done():
		t.Fatal("the tick never started planning the day")
	}
	select {
	case percepts <- percept("1", "how's your day going?"):
	case <-time.After(3 * time.Second):
		t.Fatal("the message waited for the day plan")
	}
	select {
	case <-store.replied:
	case <-time.After(3 * time.Second):
		t.Fatal("no reply while the day plan was still being written")
	}
	close(plan.release)
	cancel()
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if r := m.Replier.(*fakeReplier); len(r.sent) != 1 {
		t.Errorf("sent %q", r.sent)
	}
}
