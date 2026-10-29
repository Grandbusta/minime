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
		{Commit, "telegram:2", Ask},
		{Commit, "telegram:1", Auto},
		{Publish, "", Ask},
		{Spend, "telegram:1", Forbid}, // forbidden is forbidden, even for the owner
		{Class("unknown"), "telegram:2", Ask},
	} {
		if got := p.Check(c.class, c.person); got != c.want {
			t.Errorf("Check(%s, %q) = %s, want %s", c.class, c.person, got, c.want)
		}
	}
}

func TestZeroPolicyAllowsEverything(t *testing.T) {
	if v := (Policy{}).Check(Spend, "telegram:2"); v != Auto {
		t.Errorf("zero policy: %s", v)
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(Defaults()); err != nil {
		t.Fatal(err)
	}
	for name, classes := range map[string]map[Class]Verdict{
		"unknown class": {"fly": Auto},
		"bad verdict":   {Publish: "maybe"},
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
