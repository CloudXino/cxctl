package cli

import (
	"context"
	"fmt"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) reservedIPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "reserved-ip",
		Aliases: []string{"reserved-ips", "rip"},
		Short:   "Manage reserved IPs (public IPv4 addresses the team keeps)",
		Long: `A reserved IP is a public IPv4 address the team keeps until it deletes it. Point it at a
CC2 server, a CC2 load balancer or a Kubernetes (CKS) LoadBalancer Service in the same region,
and move it without changing the address (nothing changes inside the server or load balancer). It is billed per hour while the team has it, assigned
or not.

Reserved IPs are named by ID or by address.`,
	}
	cmd.AddCommand(a.ripListCmd(), a.ripGetCmd(), a.ripCreateCmd(), a.ripAssignCmd(), a.ripUnassignCmd(), a.ripDeleteCmd())
	return cmd
}

// findReservedIP takes an ID or an address.
func (a *app) findReservedIP(ctx context.Context, ref string) (cloudxino.ReservedIP, error) {
	c, err := a.api()
	if err != nil {
		return cloudxino.ReservedIP{}, err
	}
	all, err := c.ReservedIPs.List(ctx, nil)
	if err != nil {
		return cloudxino.ReservedIP{}, err
	}
	return pick("reserved IP", ref, all, func(v cloudxino.ReservedIP) string { return v.Id }, func(v cloudxino.ReservedIP) string { return s(v.IpAddress) })
}

// ripTarget is what the address points at: the target's name, else its ID, else "-".
func ripTarget(v cloudxino.ReservedIP) string {
	if t := s(v.AssignedTo); t != "" {
		return t
	}
	return orDash(s(v.TargetId))
}

// ripTargetKind names the kind of target for people.
func ripTargetKind(v cloudxino.ReservedIP) string {
	switch s(v.TargetType) {
	case cloudxino.ReservedIPTargetServer:
		return "server"
	case cloudxino.ReservedIPTargetLoadBalancer:
		return "load balancer"
	case cloudxino.ReservedIPTargetCKSLoadBalancer:
		return "Kubernetes LoadBalancer Service"
	}
	return orDash(s(v.TargetType))
}

func (a *app) printReservedIP(v *cloudxino.ReservedIP) error {
	return a.printObject(v.Id, [][2]string{
		{"ID", v.Id}, {"Address", s(v.IpAddress)}, {"Status", s(v.Status)}, {"Region", s(v.Region)},
		{"Project", s(v.ProjectId)}, {"Target", ripTarget(*v)}, {"Target type", ripTargetKind(*v)},
		{"Target ID", orDash(s(v.TargetId))},
		{"Last error", orDash(s(v.Error))}, {"Created", s(v.CreatedAt)},
	}, v)
}

func (a *app) ripListCmd() *cobra.Command {
	var address string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List reserved IPs",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			list, err := c.ReservedIPs.List(cmd.Context(), &cloudxino.ListReservedIPsOptions{IPAddress: address})
			if err != nil {
				return err
			}
			t := table{header: []string{"ID", "ADDRESS", "STATUS", "REGION", "TARGET"}, data: list}
			for _, v := range list {
				t.rows = append(t.rows, []string{v.Id, s(v.IpAddress), s(v.Status), s(v.Region), ripTarget(v)})
				t.ids = append(t.ids, v.Id)
			}
			return a.print(t)
		},
	}
	cmd.Flags().StringVar(&address, "ip", "", "only this address")
	return cmd
}

func (a *app) ripGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get RESERVED-IP",
		Short: "Show a reserved IP (by ID or address)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.findReservedIP(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printReservedIP(&v)
		},
	}
}

func (a *app) ripCreateCmd() *cobra.Command {
	var region, project, server, idempotencyKey string
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"reserve"},
		Short:   "Reserve a public IPv4 address (billed per hour until deleted)",
		Long: `Reserve a public IPv4 address in a region, optionally pointing it at a server right away.
It is billed per hour until you delete it, assigned or not.

When no address is free, the API answers "Reserved IPs are not available right now"
(unavailable) and nothing is reserved.`,
		Example: `  cxctl reserved-ip create --region dhaka-1
  cxctl reserved-ip create --server web-1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.api()
			if err != nil {
				return err
			}
			var req cloudxino.CreateReservedIPRequest
			setStr(&req.Region, region)
			if project != "" {
				p, err := a.findProject(ctx, project)
				if err != nil {
					return err
				}
				req.ProjectId = &p.Id
			}
			if server != "" {
				v, err := a.findServer(ctx, server)
				if err != nil {
					return err
				}
				req.ServerId = &v.Id
				if req.Region == nil && v.Region != nil {
					req.Region = v.Region // the address must be in the server's region
				}
			}
			v, err := c.ReservedIPs.Create(ctx, req, idempotencyKey)
			if err != nil {
				return err
			}
			return a.printReservedIP(v)
		},
	}
	f := cmd.Flags()
	f.StringVar(&region, "region", "", "region ID (default: the server's region with --server, else the default region)")
	f.StringVar(&project, "project", "", "project ID or name (default: the team's default project)")
	f.StringVar(&server, "server", "", "server (ID or name) to point the address at right away")
	f.StringVar(&idempotencyKey, "idempotency-key", "", "reuse a key to make re-running this command safe: a second run returns the first reserved IP")
	return cmd
}

func (a *app) ripAssignCmd() *cobra.Command {
	var lb, cksLB string
	cmd := &cobra.Command{
		Use:     "assign RESERVED-IP [SERVER] [--load-balancer LB | --cks-load-balancer ID]",
		Aliases: []string{"move"},
		Short:   "Point a reserved IP at a server, load balancer or CKS Service (moves it when it points elsewhere)",
		Long: `Point a reserved IP at exactly one target in the same region:

  SERVER                    a CC2 server (ID or name)
  --load-balancer LB        a CC2 load balancer (ID or name): its forwarding rules answer on the
                            reserved address too
  --cks-load-balancer ID    a Kubernetes LoadBalancer Service (its ID from the cluster's
                            load-balancers list in the console or the API)

Servers and load balancers: when it already points elsewhere it moves without a gap, and it is
done when the command returns. A Kubernetes Service switches to the address within a minute and
gives its own IP back. A Service can also ask for a reserved IP itself with the annotation
cloudxino.com/reserved-ip: <address>.`,
		Example: `  cxctl reserved-ip assign 203.0.113.10 web-2
  cxctl reserved-ip assign 203.0.113.10 --load-balancer edge
  cxctl reserved-ip assign 203.0.113.10 --cks-load-balancer 6f1c...`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			given := 0
			for _, set := range []bool{len(args) == 2, lb != "", cksLB != ""} {
				if set {
					given++
				}
			}
			if given != 1 {
				return fmt.Errorf("give exactly one target: SERVER, --load-balancer or --cks-load-balancer")
			}
			rip, err := a.findReservedIP(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			var v *cloudxino.ReservedIP
			switch {
			case lb != "":
				l, err := a.findLB(ctx, lb)
				if err != nil {
					return err
				}
				v, err = c.ReservedIPs.AssignLoadBalancer(ctx, rip.Id, l.Id)
				if err != nil {
					return err
				}
			case cksLB != "":
				if v, err = c.ReservedIPs.AssignCKSLoadBalancer(ctx, rip.Id, cksLB); err != nil {
					return err
				}
			default:
				srv, err := a.findServer(ctx, args[1])
				if err != nil {
					return err
				}
				if v, err = c.ReservedIPs.Assign(ctx, rip.Id, srv.Id); err != nil {
					return err
				}
			}
			return a.printReservedIP(v)
		},
	}
	cmd.Flags().StringVar(&lb, "load-balancer", "", "CC2 load balancer (ID or name) to point the address at")
	cmd.Flags().StringVar(&cksLB, "cks-load-balancer", "", "ID of a Kubernetes LoadBalancer Service to point the address at")
	return cmd
}

func (a *app) ripUnassignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unassign RESERVED-IP",
		Short: "Stop sending a reserved IP to its target (the team keeps the address)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rip, err := a.findReservedIP(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			v, err := c.ReservedIPs.Unassign(cmd.Context(), rip.Id)
			if err != nil {
				return err
			}
			return a.printReservedIP(v)
		},
	}
}

func (a *app) ripDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete RESERVED-IP",
		Aliases: []string{"rm", "release"},
		Short:   "Give a reserved IP back (unassigned first; billing stops)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rip, err := a.findReservedIP(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			what := fmt.Sprintf("Giving back reserved IP %s", s(rip.IpAddress))
			if rip.TargetId != nil {
				what += fmt.Sprintf(" (it points at %s)", ripTarget(rip))
			}
			if err := a.confirm(yes, what); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.ReservedIPs.Delete(cmd.Context(), rip.Id); err != nil {
				return err
			}
			a.done("Deleted reserved IP %s (%s).", s(rip.IpAddress), rip.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
