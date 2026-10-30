package setup

import (
	"errors"

	"github.com/charmbracelet/huh"
)

// Terminal asks in the terminal, with huh: menus you move through with the
// arrow keys, and secrets masked as you type.
type Terminal struct{}

func (Terminal) Input(title, current string, secret bool) (string, error) {
	value := ""
	in := huh.NewInput().Title(title).Value(&value)
	switch {
	case current != "" && secret:
		in.Placeholder("keep " + mask(current) + ", or paste a new one")
	case current != "":
		in.Placeholder(current + " (press Enter to keep)")
	}
	if secret {
		in.EchoMode(huh.EchoModePassword)
	}
	in.Validate(func(v string) error {
		if v == "" && current == "" {
			return errors.New("this one is needed")
		}
		return nil
	})
	if err := run(in); err != nil {
		return "", err
	}
	if value == "" {
		return current, nil
	}
	return value, nil
}

func (Terminal) Select(title string, options []string) (int, error) {
	n := 0
	opts := make([]huh.Option[int], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, i)
	}
	if err := run(huh.NewSelect[int]().Title(title).Options(opts...).Value(&n)); err != nil {
		return 0, err
	}
	return n, nil
}

func (Terminal) Confirm(title string, def bool) (bool, error) {
	ok := def
	if err := run(huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&ok)); err != nil {
		return false, err
	}
	return ok, nil
}

// run asks one question; Ctrl+C stops setup.
func run(f huh.Field) error {
	err := huh.NewForm(huh.NewGroup(f)).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return errStop
	}
	return err
}

// mask shows only the ends of a secret.
func mask(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
