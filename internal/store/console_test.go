package store

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/perception"
)

func TestConsoleQueries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// In the past, so these rows never look newest to the other tests.
	base := time.Now().UTC().AddDate(0, 0, -40).Truncate(time.Microsecond)

	p := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: "t", Speaker: "test:x",
		Kind: "message", Text: "hello", At: base}
	if _, err := s.SavePercept(ctx, p); err != nil {
		t.Fatal(err)
	}
	root := decision.Decision{ID: ulid.Make(), At: base, PerceptID: p.ID, SetID: "incoming_message@1", Tier: decision.TierSystem1,
		Action: "escalate", Gate: decision.GateAuto, CostUSD: 0.001, Latency: 200 * time.Millisecond}
	child := decision.Decision{ID: ulid.Make(), ParentID: &root.ID, At: base.Add(time.Second), PerceptID: p.ID, SetID: "reply@x",
		Tier: decision.TierSystem2, Action: "reply", Reply: "hi!", Gate: decision.GateAuto, CostUSD: 0.002, Latency: 2 * time.Second}
	own := decision.Decision{ID: ulid.Make(), At: base.Add(2 * time.Second), SetID: "plan@x", Tier: decision.TierSystem2,
		Action: "plan_day", Gate: decision.GateAuto, CostUSD: 0.0005}
	for _, d := range []decision.Decision{root, child, own} {
		if err := s.SaveDecision(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	log, err := s.DecisionLog(ctx, base.Add(-time.Second), 10, false)
	if err != nil || len(log) != 3 || log[0].ID != root.ID || log[0].Text != "hello" || log[0].Speaker != "test:x" ||
		log[2].Source != "" || !log[2].PerceptID.IsZero() {
		t.Fatalf("log = %+v, %v", log, err)
	}
	if newest, _ := s.DecisionLog(ctx, base.Add(-time.Second), 1, true); len(newest) != 1 || newest[0].ID != own.ID {
		t.Errorf("newest first = %+v", newest)
	}
	chain, err := s.DecisionChain(ctx, child.ID)
	if err != nil || len(chain) != 2 || chain[0].ID != root.ID || chain[1].Reply != "hi!" {
		t.Errorf("chain = %+v, %v", chain, err)
	}
}
