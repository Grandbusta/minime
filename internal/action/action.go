// Package action is the autonomy gate: what the agent may do on its own.
// Everything it does belongs to a Class; the Policy says, for each class,
// whether it acts on its own (Auto), asks its owner first (Ask), or never
// does it (Forbid). Anything that only involves the owner is Auto unless its
// class is forbidden: the owner can't need to approve what is for them.
package action

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Class is a kind of thing the agent does.
type Class string

const (
	Remember        Class = "remember"         // keep facts about itself and people
	Reply           Class = "reply"            // answer a message
	ReadWeb         Class = "read_web"         // an ability that fetches from the web
	ReachOut        Class = "reach_out"        // message someone first: reminders, check-ins, deliveries
	Commit          Class = "commit"           // promise to make something for someone
	Publish         Class = "publish"          // put a work on its public profile page
	Dream           Class = "dream"            // add, reword or drop one of its dreams
	Spend           Class = "spend"            // spend money (later)
	ExternalMessage Class = "external_message" // email or post outside the chat (later)
)

// Verdict is what the gate does with an action.
type Verdict string

const (
	Auto   Verdict = "auto"
	Ask    Verdict = "ask"
	Forbid Verdict = "forbid"
)

// Policy is the autonomy policy.
type Policy struct {
	Owner      string            // who approves, e.g. "telegram:123"
	Classes    map[Class]Verdict // a class not listed is Ask
	AskExpires time.Duration     // an unanswered request is denied after this
}

// Defaults is the starting policy: on its own for what is private or only
// answers, asking first for what reaches or commits to others or goes public,
// and never for money or the world outside the chat.
func Defaults() map[Class]Verdict {
	return map[Class]Verdict{
		Remember: Auto, Reply: Auto, ReadWeb: Auto,
		ReachOut: Ask, Commit: Ask, Publish: Ask, Dream: Ask,
		Spend: Forbid, ExternalMessage: Forbid,
	}
}

// Check is what the gate does with an action of class that concerns person
// ("" if it concerns no one, like publishing). A zero Policy allows everything.
func (p Policy) Check(class Class, person string) Verdict {
	if p.Classes == nil {
		return Auto
	}
	v, ok := p.Classes[class]
	if !ok {
		v = Ask
	}
	if v == Forbid {
		return Forbid
	}
	if person != "" && person == p.Owner {
		return Auto
	}
	return v
}

// ByChat reports whether the owner may change class by telling the agent:
// not money, and not the world outside the chat, which stay in the config.
func ByChat(c Class) bool {
	switch c {
	case ReachOut, Commit, Publish, ReadWeb, Dream:
		return true
	}
	return false
}

// With is p with the given classes changed.
func (p Policy) With(changes map[Class]Verdict) Policy {
	classes := make(map[Class]Verdict, len(p.Classes)+len(changes))
	for c, v := range p.Classes {
		classes[c] = v
	}
	for c, v := range changes {
		classes[c] = v
	}
	p.Classes = classes
	return p
}

// WithChatRules is p with the owner's changes made by chat (class → verdict)
// applied, skipping any that can't be made by chat or aren't valid.
func (p Policy) WithChatRules(rules map[string]string) Policy {
	if p.Classes == nil {
		return p // a zero policy allows everything, and stays that way
	}
	changes := map[Class]Verdict{}
	for c, v := range rules {
		if class, verdict := Class(c), Verdict(v); ByChat(class) && Validate(map[Class]Verdict{class: verdict}) == nil {
			changes[class] = verdict
		}
	}
	return p.With(changes)
}

// Validate checks a policy read from config: known classes and verdicts,
// replies and memory always on, and no asking for what can't wait for an
// answer (reading the web happens in the middle of an activity).
func Validate(classes map[Class]Verdict) error {
	known := Defaults()
	for c, v := range classes {
		if _, ok := known[c]; !ok {
			return fmt.Errorf("autonomy: no action class %q; there are %s", c, names(known))
		}
		switch {
		case v != Auto && v != Ask && v != Forbid:
			return fmt.Errorf("autonomy: %s must be auto, ask or forbid, have %q", c, v)
		case (c == Remember || c == Reply) && v != Auto:
			return fmt.Errorf("autonomy: %s must be auto: it is how the agent talks and remembers", c)
		case c == ReadWeb && v == Ask:
			return fmt.Errorf("autonomy: read_web can be auto or forbid, not ask")
		}
	}
	return nil
}

func names(m map[Class]Verdict) string {
	var out []string
	for c := range m {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
