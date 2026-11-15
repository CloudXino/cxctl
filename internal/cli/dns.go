package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) dnsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dns", Short: "Manage hosted DNS zones and records"}
	zone := &cobra.Command{Use: "zone", Aliases: []string{"zones"}, Short: "Manage DNS zones"}
	zone.AddCommand(a.zoneListCmd(), a.zoneGetCmd(), a.zoneCreateCmd(), a.zoneDeleteCmd(), a.zoneExportCmd(), a.zoneImportCmd())
	record := &cobra.Command{Use: "record", Aliases: []string{"records"}, Short: "Manage records in a DNS zone"}
	record.AddCommand(a.recordListCmd(), a.recordCreateCmd(), a.recordUpdateCmd(), a.recordDeleteCmd())
	cmd.AddCommand(zone, record)
	return cmd
}

// findZone returns the zone with its records.
func (a *app) findZone(ctx context.Context, ref string) (*cloudxino.DnsZone, error) {
	c, err := a.api()
	if err != nil {
		return nil, err
	}
	all, err := c.DNS.ListZones(ctx)
	if err != nil {
		return nil, err
	}
	z, err := pick("DNS zone", ref, all, func(v cloudxino.DnsZone) string { return v.Id }, func(v cloudxino.DnsZone) string { return s(v.Name) })
	if err != nil {
		return nil, err
	}
	return c.DNS.GetZone(ctx, z.Id)
}

func zoneRecords(z *cloudxino.DnsZone) []cloudxino.DnsRecord {
	if z.Records == nil {
		return []cloudxino.DnsRecord{}
	}
	return *z.Records
}

func (a *app) findRecord(ctx context.Context, zoneRef, ref string) (*cloudxino.DnsZone, cloudxino.DnsRecord, error) {
	z, err := a.findZone(ctx, zoneRef)
	if err != nil {
		return nil, cloudxino.DnsRecord{}, err
	}
	r, err := pick("record", ref, zoneRecords(z), func(v cloudxino.DnsRecord) string { return v.Id }, func(v cloudxino.DnsRecord) string { return s(v.Name) })
	return z, r, err
}

func recordTable(list []cloudxino.DnsRecord) table {
	t := table{header: []string{"ID", "NAME", "TYPE", "CONTENT", "TTL", "PRIORITY"}, data: list}
	for _, r := range list {
		t.rows = append(t.rows, []string{r.Id, s(r.Name), s(r.Type), s(r.Content), n(r.Ttl), orDash(n(r.Priority))})
		t.ids = append(t.ids, r.Id)
	}
	return t
}

func (a *app) printRecord(r *cloudxino.DnsRecord) error {
	return a.printObject(r.Id, [][2]string{
		{"ID", r.Id}, {"Name", s(r.Name)}, {"FQDN", s(r.Fqdn)}, {"Type", s(r.Type)}, {"Content", s(r.Content)},
		{"TTL", n(r.Ttl)}, {"Priority", orDash(n(r.Priority))},
	}, r)
}

func (a *app) printZone(z *cloudxino.DnsZone) error {
	return a.printObject(z.Id, [][2]string{
		{"ID", z.Id}, {"Name", s(z.Name)}, {"Status", s(z.Status)},
		{"Nameservers", join(z.Nameservers)}, {"Nameservers seen", orDash(join(z.NameserversSeen))},
		{"Records", orDash(n(z.RecordCount))}, {"Created", s(z.CreatedAt)},
	}, z)
}

func (a *app) zoneListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List DNS zones",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			list, err := c.DNS.ListZones(cmd.Context())
			if err != nil {
				return err
			}
			t := table{header: []string{"ID", "NAME", "STATUS", "RECORDS", "NAMESERVERS"}, data: list}
			for _, z := range list {
				t.rows = append(t.rows, []string{z.Id, s(z.Name), s(z.Status), orDash(n(z.RecordCount)), join(z.Nameservers)})
				t.ids = append(t.ids, z.Id)
			}
			return a.print(t)
		},
	}
}

func (a *app) zoneGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get ZONE",
		Short: "Show a zone (by ID or domain)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.findZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printZone(z)
		},
	}
}

func (a *app) zoneCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create DOMAIN",
		Short: "Host a domain on CloudXino's nameservers",
		Long:  "Host a domain. It stays pending until you set the nameservers shown here at the domain's registrar.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			z, err := c.DNS.CreateZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.printZone(z); err != nil {
				return err
			}
			if a.output == "table" {
				fmt.Fprintf(a.errOut, "\nSet these nameservers at the registrar of %s: %s\n", s(z.Name), join(z.Nameservers))
			}
			return nil
		},
	}
}

func (a *app) zoneDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete ZONE",
		Aliases: []string{"rm"},
		Short:   "Stop hosting a zone and delete all its records",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.findZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting zone %s and its %d records", s(z.Name), len(zoneRecords(z)))); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.DNS.DeleteZone(cmd.Context(), z.Id); err != nil {
				return err
			}
			a.done("Deleted zone %s (%s).", s(z.Name), z.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (a *app) zoneExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export ZONE",
		Short: "Print the zone as a BIND zone file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.findZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			text, err := c.DNS.Export(cmd.Context(), z.Id)
			if err != nil {
				return err
			}
			_, err = io.WriteString(a.out, text)
			return err
		},
	}
}

func (a *app) zoneImportCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "import ZONE --file FILE",
		Short: "Add the records of a BIND zone file (- for standard input)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" {
				return usagef("--file is required (- reads standard input)")
			}
			var data []byte
			var err error
			if file == "-" {
				data, err = io.ReadAll(a.in)
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return err
			}
			z, err := a.findZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.DNS.Import(cmd.Context(), z.Id, string(data)); err != nil {
				return err
			}
			a.done("Imported records into %s.", s(z.Name))
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "zone file (- for standard input)")
	return cmd
}

func (a *app) recordListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list ZONE",
		Aliases: []string{"ls"},
		Short:   "List a zone's records",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, err := a.findZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.print(recordTable(zoneRecords(z)))
		},
	}
}

type recordFlags struct {
	name, typ, content string
	ttl, priority      int
}

func (f *recordFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", `"@" for the zone itself, or a name relative to it (www)`)
	fl.StringVar(&f.typ, "type", "", "A, AAAA, CNAME, MX, TXT, NS, SRV, CAA, ...")
	fl.StringVar(&f.content, "content", "", "the value: an address, a host name, text, ...")
	fl.IntVar(&f.ttl, "ttl", 0, "time to live in seconds (default 3600)")
	fl.IntVar(&f.priority, "priority", 0, "MX and SRV priority")
}

func (a *app) recordCreateCmd() *cobra.Command {
	var f recordFlags
	cmd := &cobra.Command{
		Use:     "create ZONE --type TYPE --content VALUE [--name NAME]",
		Short:   "Add a record",
		Example: "  cxctl dns record create example.com --name www --type A --content 203.0.113.10 --ttl 300",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.typ == "" || f.content == "" {
				return usagef("--type and --content are required")
			}
			z, err := a.findZone(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			in := cloudxino.RecordIn{Type: f.typ, Content: f.content}
			name := f.name
			if name == "" {
				name = "@"
			}
			in.Name = &name
			fl := cmd.Flags()
			if fl.Changed("ttl") {
				in.Ttl = &f.ttl
			}
			if fl.Changed("priority") {
				in.Priority = &f.priority
			}
			c, _ := a.api()
			r, err := c.DNS.CreateRecord(cmd.Context(), z.Id, in)
			if err != nil {
				return err
			}
			return a.printRecord(r)
		},
	}
	f.add(cmd)
	return cmd
}

func (a *app) recordUpdateCmd() *cobra.Command {
	var f recordFlags
	cmd := &cobra.Command{
		Use:   "update ZONE RECORD",
		Short: "Change a record (RECORD is its ID, or its name when that is unique)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, r, err := a.findRecord(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			// the API replaces the record, so unchanged fields are sent as they are
			in := cloudxino.RecordIn{Type: s(r.Type), Content: s(r.Content), Name: r.Name, Ttl: r.Ttl, Priority: r.Priority}
			if fl.Changed("name") {
				in.Name = &f.name
			}
			if fl.Changed("type") {
				in.Type = f.typ
			}
			if fl.Changed("content") {
				in.Content = f.content
			}
			if fl.Changed("ttl") {
				in.Ttl = &f.ttl
			}
			if fl.Changed("priority") {
				in.Priority = &f.priority
			}
			c, _ := a.api()
			out, err := c.DNS.UpdateRecord(cmd.Context(), z.Id, r.Id, in)
			if err != nil {
				return err
			}
			return a.printRecord(out)
		},
	}
	f.add(cmd)
	return cmd
}

func (a *app) recordDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete ZONE RECORD",
		Aliases: []string{"rm"},
		Short:   "Delete a record (RECORD is its ID, or its name when that is unique)",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			z, r, err := a.findRecord(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting %s record %s (%s)", s(r.Type), orDash(s(r.Fqdn)), s(r.Content))); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.DNS.DeleteRecord(cmd.Context(), z.Id, r.Id); err != nil {
				return err
			}
			a.done("Deleted record %s.", r.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
