package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
)

// table is a list for the table, json and id outputs.
type table struct {
	header []string
	rows   [][]string
	ids    []string
	// data is what -o json prints
	data any
}

func (a *app) checkOutput() error {
	switch a.output {
	case "table", "json", "id":
		return nil
	}
	return usagef("unknown output %q: use table, json or id", a.output)
}

func (a *app) print(t table) error {
	if err := a.checkOutput(); err != nil {
		return err
	}
	switch a.output {
	case "json":
		return a.printJSON(t.data)
	case "id":
		for _, id := range t.ids {
			fmt.Fprintln(a.out, id)
		}
		return nil
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 3, ' ', 0)
	if !a.noHeader {
		fmt.Fprintln(w, strings.Join(t.header, "\t"))
	}
	for _, r := range t.rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return w.Flush()
}

// printObject shows one resource: as key/value lines, as JSON, or just its ID.
func (a *app) printObject(id string, fields [][2]string, data any) error {
	if err := a.checkOutput(); err != nil {
		return err
	}
	switch a.output {
	case "json":
		return a.printJSON(data)
	case "id":
		fmt.Fprintln(a.out, id)
		return nil
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	for _, f := range fields {
		fmt.Fprintf(w, "%s:\t%s\n", f[0], f[1])
	}
	return w.Flush()
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// done prints a short confirmation, except in json / id output where it would break parsing.
func (a *app) done(format string, args ...any) {
	if a.output == "table" {
		fmt.Fprintf(a.out, format+"\n", args...)
	}
}

func s(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func n(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func join(p *[]string) string {
	if p == nil {
		return ""
	}
	return strings.Join(*p, ",")
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}
