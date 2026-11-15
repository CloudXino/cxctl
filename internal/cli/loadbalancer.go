package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

const lbRuleHelp = `Forwarding rules are ENTRY_PROTOCOL:ENTRY_PORT:TARGET_PORT or
ENTRY_PROTOCOL:ENTRY_PORT:TARGET_PROTOCOL:TARGET_PORT, e.g. http:80:8080 or tcp:443:tcp:8443
(https passes through as tcp). The health check is PROTOCOL[:PORT[:PATH]], e.g. http:8080:/healthz.`

func (a *app) loadBalancerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "load-balancer",
		Aliases: []string{"load-balancers", "lb"},
		Short:   "Manage load balancers",
	}
	cmd.AddCommand(a.lbListCmd(), a.lbGetCmd(), a.lbCreateCmd(), a.lbUpdateCmd(), a.lbDeleteCmd(),
		a.lbTargetCmd("add-target", "Send traffic to one more server", (*cloudxino.LoadBalancersService).AddTarget),
		a.lbTargetCmd("remove-target", "Stop sending traffic to a server", (*cloudxino.LoadBalancersService).RemoveTarget))
	return cmd
}

func parseForwarding(v string) (cloudxino.ForwardingRule, error) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	bad := func() (cloudxino.ForwardingRule, error) {
		return cloudxino.ForwardingRule{}, usagef("cannot read forwarding rule %q\n\n%s", v, lbRuleHelp)
	}
	var entryProto, targetProto, entryPort, targetPort string
	switch len(parts) {
	case 3:
		entryProto, entryPort, targetPort = parts[0], parts[1], parts[2]
		targetProto = entryProto
	case 4:
		entryProto, entryPort, targetProto, targetPort = parts[0], parts[1], parts[2], parts[3]
	default:
		return bad()
	}
	ep, err1 := strconv.Atoi(entryPort)
	tp, err2 := strconv.Atoi(targetPort)
	if err1 != nil || err2 != nil {
		return bad()
	}
	entryProto, targetProto = strings.ToLower(entryProto), strings.ToLower(targetProto)
	return cloudxino.ForwardingRule{EntryProtocol: &entryProto, EntryPort: ep, TargetProtocol: &targetProto, TargetPort: tp}, nil
}

func parseHealthCheck(v string) (*cloudxino.HealthCheck, error) {
	parts := strings.SplitN(strings.TrimSpace(v), ":", 3)
	proto := strings.ToLower(parts[0])
	if proto != "tcp" && proto != "http" {
		return nil, usagef("health check %q: protocol must be tcp or http\n\n%s", v, lbRuleHelp)
	}
	hc := &cloudxino.HealthCheck{Protocol: &proto}
	if len(parts) > 1 && parts[1] != "" {
		port, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, usagef("health check %q: bad port", v)
		}
		hc.Port = &port
	}
	if len(parts) > 2 && parts[2] != "" {
		hc.Path = &parts[2]
	}
	return hc, nil
}

func (a *app) findLB(ctx context.Context, ref string) (cloudxino.LoadBalancer, error) {
	c, err := a.api()
	if err != nil {
		return cloudxino.LoadBalancer{}, err
	}
	all, err := c.LoadBalancers.List(ctx)
	if err != nil {
		return cloudxino.LoadBalancer{}, err
	}
	return pick("load balancer", ref, all, func(v cloudxino.LoadBalancer) string { return v.Id }, func(v cloudxino.LoadBalancer) string { return s(v.Name) })
}

func formatForwarding(r cloudxino.ForwardingRuleOut) string {
	return fmt.Sprintf("%s:%s -> %s:%s", s(r.EntryProtocol), n(r.EntryPort), s(r.TargetProtocol), n(r.TargetPort))
}

func (a *app) printLB(lb *cloudxino.LoadBalancer) error {
	status := s(lb.Status)
	if d := s(lb.StatusDetail); d != "" {
		status += " (" + d + ")"
	}
	fields := [][2]string{{"ID", lb.Id}, {"Name", s(lb.Name)}, {"Status", status}, {"IP", orDash(s(lb.IpAddress))},
		{"Region", s(lb.Region)}, {"Algorithm", s(lb.Algorithm)}, {"Targets", orDash(join(lb.TargetIds))}}
	if lb.ForwardingRules != nil {
		for i, r := range *lb.ForwardingRules {
			label := ""
			if i == 0 {
				label = "Forwarding"
			}
			fields = append(fields, [2]string{label, formatForwarding(r)})
		}
	}
	if hc := lb.HealthCheck; hc != nil {
		v := s(hc.Protocol) + ":" + orDash(n(hc.Port))
		if p := s(hc.Path); p != "" {
			v += ":" + p
		}
		fields = append(fields, [2]string{"Health check", v})
	}
	return a.printObject(lb.Id, fields, lb)
}

func (a *app) lbListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List load balancers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			list, err := c.LoadBalancers.List(cmd.Context())
			if err != nil {
				return err
			}
			t := table{header: []string{"ID", "NAME", "STATUS", "IP", "REGION", "RULES", "TARGETS"}, data: list}
			for _, lb := range list {
				var rules []string
				if lb.ForwardingRules != nil {
					for _, r := range *lb.ForwardingRules {
						rules = append(rules, formatForwarding(r))
					}
				}
				targets := 0
				if lb.TargetIds != nil {
					targets = len(*lb.TargetIds)
				}
				t.rows = append(t.rows, []string{lb.Id, s(lb.Name), s(lb.Status), orDash(s(lb.IpAddress)), s(lb.Region), strings.Join(rules, " "), strconv.Itoa(targets)})
				t.ids = append(t.ids, lb.Id)
			}
			return a.print(t)
		},
	}
}

func (a *app) lbGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get LOAD_BALANCER",
		Short: "Show a load balancer (by ID or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lb, err := a.findLB(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printLB(&lb)
		},
	}
}

type lbFlags struct {
	name, algorithm, healthCheck string
	rules, targets               []string
	wait                         bool
	timeout                      time.Duration
}

func (f *lbFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "name")
	fl.StringVar(&f.algorithm, "algorithm", "", "round_robin (default) or least_connections")
	fl.StringArrayVar(&f.rules, "rule", nil, "forwarding rule, e.g. http:80:8080; repeatable")
	fl.StringVar(&f.healthCheck, "health-check", "", "PROTOCOL[:PORT[:PATH]], e.g. http:8080:/healthz")
	fl.StringSliceVar(&f.targets, "target", nil, "server (ID or name) to send traffic to; repeatable")
	fl.BoolVar(&f.wait, "wait", false, "wait until the load balancer is active")
	fl.DurationVar(&f.timeout, "timeout", 15*time.Minute, "how long --wait waits")
}

func (f *lbFlags) forwarding() ([]cloudxino.ForwardingRule, error) {
	out := []cloudxino.ForwardingRule{}
	for _, v := range f.rules {
		r, err := parseForwarding(v)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (a *app) lbCreateCmd() *cobra.Command {
	var f lbFlags
	var region, project string
	cmd := &cobra.Command{
		Use:     "create --name NAME --rule RULE...",
		Short:   "Create a load balancer (billed from the team's wallet)",
		Long:    "Create a load balancer: a public IP that spreads traffic over servers.\n\n" + lbRuleHelp,
		Example: "  cxctl lb create --name web --rule http:80:8080 --health-check http:8080:/healthz --target web-1 --target web-2 --wait",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if f.name == "" || len(f.rules) == 0 {
				return usagef("--name and at least one --rule are required")
			}
			ctx := cmd.Context()
			req := cloudxino.CreateLoadBalancerRequest{Name: f.name}
			var err error
			if req.ForwardingRules, err = f.forwarding(); err != nil {
				return err
			}
			setStr(&req.Algorithm, f.algorithm)
			setStr(&req.Region, region)
			if f.healthCheck != "" {
				if req.HealthCheck, err = parseHealthCheck(f.healthCheck); err != nil {
					return err
				}
			}
			if project != "" {
				p, err := a.findProject(ctx, project)
				if err != nil {
					return err
				}
				req.ProjectId = &p.Id
			}
			ids, err := a.serverIDs(ctx, f.targets)
			if err != nil {
				return err
			}
			req.TargetIds = &ids
			c, err := a.api()
			if err != nil {
				return err
			}
			lb, err := c.LoadBalancers.Create(ctx, req)
			if err != nil {
				return err
			}
			if f.wait {
				a.progress("Waiting for %s (%s) to be active...", s(lb.Name), lb.Id)
				if lb, err = c.LoadBalancers.WaitActive(ctx, lb.Id, a.waitOpts(f.timeout)); err != nil {
					return err
				}
			}
			return a.printLB(lb)
		},
	}
	f.add(cmd)
	cmd.Flags().StringVar(&region, "region", "", "region ID (default: the default region)")
	cmd.Flags().StringVar(&project, "project", "", "project ID or name (default: the team's default project)")
	return cmd
}

func (a *app) lbUpdateCmd() *cobra.Command {
	var f lbFlags
	cmd := &cobra.Command{
		Use:   "update LOAD_BALANCER",
		Short: "Change a load balancer: each list given replaces the old one",
		Long:  "Change a load balancer. --rule and --target replace the whole list when given.\n\n" + lbRuleHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			lb, err := a.findLB(ctx, args[0])
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			req := cloudxino.UpdateLoadBalancerRequest{}
			if fl.Changed("name") {
				req.Name = &f.name
			}
			if fl.Changed("algorithm") {
				req.Algorithm = &f.algorithm
			}
			if fl.Changed("rule") {
				rules, err := f.forwarding()
				if err != nil {
					return err
				}
				req.ForwardingRules = &rules
			}
			if fl.Changed("health-check") {
				if req.HealthCheck, err = parseHealthCheck(f.healthCheck); err != nil {
					return err
				}
			}
			if fl.Changed("target") {
				ids, err := a.serverIDs(ctx, f.targets)
				if err != nil {
					return err
				}
				req.TargetIds = &ids
			}
			if req == (cloudxino.UpdateLoadBalancerRequest{}) {
				return usagef("nothing to change")
			}
			c, _ := a.api()
			out, err := c.LoadBalancers.Update(ctx, lb.Id, req)
			if err != nil {
				return err
			}
			if f.wait {
				if out, err = c.LoadBalancers.WaitActive(ctx, lb.Id, a.waitOpts(f.timeout)); err != nil {
					return err
				}
			}
			return a.printLB(out)
		},
	}
	f.add(cmd)
	return cmd
}

func (a *app) lbTargetCmd(name, short string, do func(*cloudxino.LoadBalancersService, context.Context, string, string) (*cloudxino.LoadBalancer, error)) *cobra.Command {
	return &cobra.Command{
		Use:   name + " LOAD_BALANCER SERVER",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			lb, err := a.findLB(ctx, args[0])
			if err != nil {
				return err
			}
			ids, err := a.serverIDs(ctx, args[1:])
			if err != nil {
				return err
			}
			c, _ := a.api()
			out, err := do(c.LoadBalancers, ctx, lb.Id, ids[0])
			if err != nil {
				return err
			}
			return a.printLB(out)
		},
	}
}

func (a *app) lbDeleteCmd() *cobra.Command {
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "delete LOAD_BALANCER",
		Aliases: []string{"rm"},
		Short:   "Delete a load balancer",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			lb, err := a.findLB(ctx, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting load balancer %s", s(lb.Name))); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.LoadBalancers.Delete(ctx, lb.Id); err != nil {
				return err
			}
			if wait {
				if err := c.LoadBalancers.WaitDeleted(ctx, lb.Id, a.waitOpts(timeout)); err != nil {
					return err
				}
			}
			a.done("Deleted load balancer %s (%s).", s(lb.Name), lb.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until it is gone")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "how long --wait waits")
	return cmd
}
