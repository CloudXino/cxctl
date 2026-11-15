package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

const ruleHelp = `Rules are PROTOCOL:PORTS:ADDRESSES, for example
  tcp:22:203.0.113.7/32     SSH from one address
  tcp:80,443                web from anywhere (addresses left out = 0.0.0.0/0)
  udp:8000-8100:10.0.0.0/8  a port range from a network
  icmp:0.0.0.0/0            ping (ICMP has no ports)
PORTS is a list, a range or "all"; ADDRESSES is a comma-separated list of IPv4 addresses or networks.`

func (a *app) firewallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "firewall",
		Aliases: []string{"firewalls", "fw"},
		Short:   "Manage Cloud Firewalls",
	}
	cmd.AddCommand(a.firewallListCmd(), a.firewallGetCmd(), a.firewallCreateCmd(), a.firewallUpdateCmd(), a.firewallDeleteCmd())
	return cmd
}

// parseRule reads one --inbound / --outbound value; addrKey is "sources" or "destinations".
func parseRule(v, addrKey string) (cloudxino.RuleIn, error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	proto := strings.ToLower(parts[0])
	var ports, addrs string
	switch {
	case proto == "icmp" && len(parts) == 2:
		addrs = parts[1]
	case proto == "icmp" && len(parts) == 3 && parts[1] == "":
		addrs = parts[2]
	case proto != "icmp" && (len(parts) == 2 || len(parts) == 3):
		ports = parts[1]
		if len(parts) == 3 {
			addrs = parts[2]
		}
	case len(parts) == 1 && proto != "":
		// "tcp" alone: all ports from anywhere
	default:
		return cloudxino.RuleIn{}, usagef("cannot read rule %q\n\n%s", v, ruleHelp)
	}
	switch proto {
	case "tcp", "udp", "icmp":
	default:
		return cloudxino.RuleIn{}, usagef("rule %q: protocol must be tcp, udp or icmp", v)
	}
	r := cloudxino.RuleIn{Protocol: proto}
	if ports != "" {
		r.Ports = &ports
	}
	list := []string{"0.0.0.0/0"}
	if strings.TrimSpace(addrs) != "" {
		list = nil
		for _, a := range strings.Split(addrs, ",") {
			if a = strings.TrimSpace(a); a != "" {
				list = append(list, a)
			}
		}
	}
	if addrKey == "sources" {
		r.Sources = &list
	} else {
		r.Destinations = &list
	}
	return r, nil
}

func parseRules(vals []string, addrKey string) (*[]cloudxino.RuleIn, error) {
	out := []cloudxino.RuleIn{}
	for _, v := range vals {
		if strings.TrimSpace(v) == "" {
			continue // --inbound= clears the list
		}
		r, err := parseRule(v, addrKey)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return &out, nil
}

func formatRule(r cloudxino.FirewallRule, addrs *[]string) string {
	proto := s(r.Protocol)
	if proto == "icmp" {
		return proto + ":" + join(addrs)
	}
	return proto + ":" + orDash(s(r.Ports)) + ":" + join(addrs)
}

func (a *app) findFirewall(ctx context.Context, ref string) (cloudxino.Firewall, error) {
	c, err := a.api()
	if err != nil {
		return cloudxino.Firewall{}, err
	}
	all, err := c.Firewalls.List(ctx)
	if err != nil {
		return cloudxino.Firewall{}, err
	}
	return pick("firewall", ref, all, func(v cloudxino.Firewall) string { return v.Id }, func(v cloudxino.Firewall) string { return s(v.Name) })
}

func (a *app) printFirewall(fw *cloudxino.Firewall) error {
	fields := [][2]string{{"ID", fw.Id}, {"Name", s(fw.Name)}, {"Status", s(fw.Status)},
		{"Servers", orDash(join(fw.ServerIds))}, {"Tags", orDash(join(fw.Tags))}, {"Applies to", orDash(n(fw.ServerCount)) + " servers"}}
	if fw.InboundRules != nil {
		for i, r := range *fw.InboundRules {
			label := ""
			if i == 0 {
				label = "Inbound"
			}
			fields = append(fields, [2]string{label, formatRule(r, r.Sources)})
		}
	}
	if fw.InboundRules == nil || len(*fw.InboundRules) == 0 {
		fields = append(fields, [2]string{"Inbound", "nothing allowed"})
	}
	if fw.OutboundRules != nil {
		for i, r := range *fw.OutboundRules {
			label := ""
			if i == 0 {
				label = "Outbound"
			}
			fields = append(fields, [2]string{label, formatRule(r, r.Destinations)})
		}
	}
	return a.printObject(fw.Id, fields, fw)
}

func (a *app) firewallListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List Cloud Firewalls",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			list, err := c.Firewalls.List(cmd.Context())
			if err != nil {
				return err
			}
			t := table{header: []string{"ID", "NAME", "STATUS", "INBOUND", "OUTBOUND", "SERVERS", "TAGS"}, data: list}
			for _, fw := range list {
				in, out := 0, 0
				if fw.InboundRules != nil {
					in = len(*fw.InboundRules)
				}
				if fw.OutboundRules != nil {
					out = len(*fw.OutboundRules)
				}
				t.rows = append(t.rows, []string{fw.Id, s(fw.Name), s(fw.Status), fmt.Sprint(in), fmt.Sprint(out), orDash(n(fw.ServerCount)), join(fw.Tags)})
				t.ids = append(t.ids, fw.Id)
			}
			return a.print(t)
		},
	}
}

func (a *app) firewallGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get FIREWALL",
		Short: "Show a firewall and its rules (by ID or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fw, err := a.findFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printFirewall(&fw)
		},
	}
}

type firewallFlags struct {
	name              string
	inbound, outbound []string
	servers, tags     []string
}

func (f *firewallFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "firewall name")
	fl.StringArrayVar(&f.inbound, "inbound", nil, "inbound rule PROTOCOL:PORTS:SOURCES; repeatable")
	fl.StringArrayVar(&f.outbound, "outbound", nil, "outbound rule PROTOCOL:PORTS:DESTINATIONS; repeatable (none given = all outbound allowed)")
	fl.StringSliceVar(&f.servers, "server", nil, "server (ID or name) to apply to; repeatable")
	fl.StringSliceVar(&f.tags, "tag", nil, "apply to every server with this tag; repeatable")
}

func (a *app) serverIDs(ctx context.Context, refs []string) ([]string, error) {
	ids := []string{}
	if len(refs) == 0 {
		return ids, nil
	}
	c, err := a.api()
	if err != nil {
		return nil, err
	}
	all, err := c.Servers.List(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		v, err := pick("server", ref, all, func(v cloudxino.Server) string { return v.Id }, func(v cloudxino.Server) string { return s(v.Name) })
		if err != nil {
			return nil, err
		}
		ids = append(ids, v.Id)
	}
	return ids, nil
}

func (a *app) firewallCreateCmd() *cobra.Command {
	var f firewallFlags
	cmd := &cobra.Command{
		Use:     "create --name NAME [--inbound RULE]...",
		Short:   "Create a Cloud Firewall",
		Long:    "Create a Cloud Firewall. Inbound traffic no rule allows is dropped.\n\n" + ruleHelp,
		Example: `  cxctl firewall create --name web --inbound tcp:80,443 --inbound tcp:22:203.0.113.7/32 --tag web`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.name == "" {
				return usagef("--name is required")
			}
			ctx := cmd.Context()
			req := cloudxino.CreateFirewallRequest{Name: f.name}
			var err error
			if req.InboundRules, err = parseRules(f.inbound, "sources"); err != nil {
				return err
			}
			if len(f.outbound) > 0 {
				if req.OutboundRules, err = parseRules(f.outbound, "destinations"); err != nil {
					return err
				}
			}
			ids, err := a.serverIDs(ctx, f.servers)
			if err != nil {
				return err
			}
			req.ServerIds = &ids
			tags := append([]string{}, f.tags...)
			req.Tags = &tags
			c, err := a.api()
			if err != nil {
				return err
			}
			fw, err := c.Firewalls.Create(ctx, req)
			if err != nil {
				return err
			}
			return a.printFirewall(fw)
		},
	}
	f.add(cmd)
	return cmd
}

func (a *app) firewallUpdateCmd() *cobra.Command {
	var f firewallFlags
	cmd := &cobra.Command{
		Use:   "update FIREWALL",
		Short: "Change a firewall: each list given replaces the old one",
		Long: `Change a firewall's name, rules, servers or tags. A flag that is given replaces that whole list
(--inbound replaces every inbound rule); lists not given stay as they are. Pass --tag= to clear tags.

` + ruleHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			fw, err := a.findFirewall(ctx, args[0])
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			req := cloudxino.UpdateFirewallRequest{}
			if fl.Changed("name") {
				req.Name = &f.name
			}
			if fl.Changed("inbound") {
				if req.InboundRules, err = parseRules(f.inbound, "sources"); err != nil {
					return err
				}
			}
			if fl.Changed("outbound") {
				if req.OutboundRules, err = parseRules(f.outbound, "destinations"); err != nil {
					return err
				}
			}
			if fl.Changed("server") {
				ids, err := a.serverIDs(ctx, f.servers)
				if err != nil {
					return err
				}
				req.ServerIds = &ids
			}
			if fl.Changed("tag") {
				tags := append([]string{}, f.tags...)
				req.Tags = &tags
			}
			if req == (cloudxino.UpdateFirewallRequest{}) {
				return usagef("nothing to change")
			}
			c, _ := a.api()
			out, err := c.Firewalls.Update(ctx, fw.Id, req)
			if err != nil {
				return err
			}
			return a.printFirewall(out)
		},
	}
	f.add(cmd)
	return cmd
}

func (a *app) firewallDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete FIREWALL",
		Aliases: []string{"rm"},
		Short:   "Delete a firewall",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fw, err := a.findFirewall(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting firewall %s", s(fw.Name))); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.Firewalls.Delete(cmd.Context(), fw.Id); err != nil {
				return err
			}
			a.done("Deleted firewall %s (%s).", s(fw.Name), fw.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
