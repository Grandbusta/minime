package questions

import (
	"testing"
)

func TestShippedSetsAreValid(t *testing.T) {
	sets, err := LoadDir("../../../decisions")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := sets["incoming_message"]
	if !ok {
		t.Fatal("incoming_message set missing")
	}
	if s.ID != "incoming_message" || s.Version < 1 || s.Primary != "intent" {
		t.Errorf("set = %+v", s)
	}
	if l, ok := sets["life_area"]; !ok || l.Primary != "" || l.Thresholds.Label == 0 {
		t.Errorf("life_area set = %+v", l)
	}
}
