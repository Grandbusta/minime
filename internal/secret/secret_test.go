package secret

import (
	"bytes"
	"errors"
	"testing"
)

func TestSealedSecretsOpenOnlyWithTheirKeyAndName(t *testing.T) {
	k, err := ParseKey(NewKey())
	if err != nil {
		t.Fatal(err)
	}
	sealed := k.Seal("OPENAI_API_KEY", "sk-live-123")
	if bytes.Contains(sealed, []byte("sk-live")) {
		t.Fatal("the value is readable in what's stored")
	}
	if v, err := k.Open("OPENAI_API_KEY", sealed); err != nil || v != "sk-live-123" {
		t.Fatalf("open = %q, %v", v, err)
	}
	other, _ := ParseKey(NewKey())
	if _, err := other.Open("OPENAI_API_KEY", sealed); !errors.Is(err, ErrWrongKey) {
		t.Errorf("another key opened it: %v", err)
	}
	if _, err := k.Open("TELEGRAM_BOT_TOKEN", sealed); !errors.Is(err, ErrWrongKey) {
		t.Errorf("it opened under another name: %v", err)
	}
	for _, bad := range []string{"", "abc", NewKey()[:62]} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("key %q accepted", bad)
		}
	}
}
