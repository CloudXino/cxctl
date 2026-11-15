package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// pick finds the one item whose ID or name is ref. Every command that takes a resource accepts
// either, so people can type names and scripts can pass IDs.
func pick[T any](kind, ref string, items []T, id func(T) string, name func(T) string) (T, error) {
	var zero T
	for _, it := range items {
		if id(it) == ref {
			return it, nil
		}
	}
	var found []T
	for _, it := range items {
		if name(it) == ref {
			found = append(found, it)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return zero, fmt.Errorf("no %s with ID or name %q", kind, ref)
	}
	ids := make([]string, len(found))
	for i, it := range found {
		ids[i] = id(it)
	}
	return zero, fmt.Errorf("%d %ss are named %q; use an ID: %s", len(found), kind, ref, strings.Join(ids, ", "))
}

// confirm asks before something that cannot be undone. --yes skips it; without a terminal to ask
// on, the command stops instead of guessing.
func (a *app) confirm(yes bool, what string) error {
	if yes {
		return nil
	}
	if f, ok := a.in.(*os.File); ok {
		if st, err := f.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			return usagef("%s cannot be undone; pass --yes to confirm", what)
		}
	}
	fmt.Fprintf(a.errOut, "%s cannot be undone. Type yes to continue: ", what)
	line, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil && err != io.EOF {
		return err
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return fmt.Errorf("cancelled")
	}
	return nil
}

// readLine reads one line (an API key, a password) from in, for piping secrets instead of
// putting them on the command line.
func readLine(in io.Reader) (string, error) {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
