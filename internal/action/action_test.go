package action

import "testing"

func TestCheck(t *testing.T) {
	p := Policy{Owner: "telegram:1", Classes: Defaults()}
	for _, c := range []struct {
		class  Class
		person string
		want   Verdict
	}{
		{Reply, "telegram:2", Auto},
		{ReachOut, "telegram:2", Ask},
		{ReachOut, "telegram:1", Auto}, // the owner needn't approve what's for them
		{Publish, "", Ask},
		{Spend, "telegram:1", Forbid},
		{Class("unknown"), "telegram:2", Ask},
	} {
		if got := p.Check(c.class, c.person); got != c.want {
			t.Errorf("Check(%s, %q) = %s, want %s", c.class, c.person, got, c.want)
		}
	}
}

func TestWithChatRules(t *testing.T) {
	p := Policy{Owner: "telegram:1", Classes: Defaults()}.WithChatRules(map[string]string{
		"reach_out": "auto", "spend": "auto", "read_web": "ask", "fly": "auto"})
	if p.Check(ReachOut, "x") != Auto || p.Check(Spend, "x") != Forbid || p.Check(ReadWeb, "x") != Auto {
		t.Errorf("classes = %v", p.Classes)
	}
	if d := Defaults(); d[ReachOut] != Ask {
		t.Error("changed the defaults")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(Defaults()); err != nil {
		t.Fatal(err)
	}
	for name, classes := range map[string]map[Class]Verdict{
		"replies off":   {Reply: Forbid},
		"memory asks":   {Remember: Ask},
		"read web asks": {ReadWeb: Ask},
	} {
		if err := Validate(classes); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := Validate(map[Class]Verdict{ReadWeb: Forbid, Publish: Auto}); err != nil {
		t.Errorf("valid policy: %v", err)
	}
}
