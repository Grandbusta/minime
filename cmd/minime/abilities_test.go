package main

import (
	"strings"
	"testing"
)

func TestNewAbilities(t *testing.T) {
	set, err := newAbilities([]string{"read_article", "read_book", "read_article"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.Names(), ","); got != "journal,read_article,read_book" {
		t.Errorf("abilities = %s", got)
	}
	if set, err := newAbilities(nil, nil); err != nil || strings.Join(set.Names(), ",") != "journal" {
		t.Errorf("none listed: %v, %v", set.Names(), err)
	}
	if _, err := newAbilities([]string{"web_search"}, nil); err == nil || !strings.Contains(err.Error(), "read_article, read_book") {
		t.Errorf("unknown ability: %v", err)
	}
}
