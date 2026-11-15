package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) serverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "server",
		Aliases: []string{"servers", "cc2"},
		Short:   "Manage CC2 servers",
	}
	cmd.AddCommand(
		a.serverListCmd(), a.serverGetCmd(), a.serverCreateCmd(), a.serverUpdateCmd(), a.serverDeleteCmd(),
		a.serverActionCmd("start", "Power a server on", (*cloudxino.ServersService).PowerOn),
		a.serverActionCmd("stop", "Power a server off", (*cloudxino.ServersService).PowerOff),
		a.serverActionCmd("reboot", "Reboot a server", (*cloudxino.ServersService).Reboot),
		a.serverResizeCmd(), a.serverPrivateKeyCmd(),
		a.serverSizesCmd(), a.serverRegionsCmd(), a.serverImagesCmd(),
	)
	return cmd
}

func (a *app) findServer(ctx context.Context, ref string) (cloudxino.Server, error) {
	c, err := a.api()
	if err != nil {
		return cloudxino.Server{}, err
	}
	all, err := c.Servers.List(ctx, nil)
	if err != nil {
		return cloudxino.Server{}, err
	}
	return pick("server", ref, all, func(v cloudxino.Server) string { return v.Id }, func(v cloudxino.Server) string { return s(v.Name) })
}

func serverStatus(v cloudxino.Server) string {
	st := s(v.Status)
	if v.Progress != nil && v.Progress.Percent != nil && st == cloudxino.ServerCreating {
		st += " " + strconv.Itoa(*v.Progress.Percent) + "%"
	}
	return st
}

func serverTable(list []cloudxino.Server) table {
	t := table{header: []string{"ID", "NAME", "STATUS", "SIZE", "REGION", "IP", "PRIVATE IP", "TAGS"}, data: list}
	for _, v := range list {
		t.rows = append(t.rows, []string{v.Id, s(v.Name), serverStatus(v), s(v.Size), s(v.Region), orDash(s(v.IpAddress)), orDash(s(v.PrivateIp)), join(v.Tags)})
		t.ids = append(t.ids, v.Id)
	}
	return t
}

func (a *app) printServer(v *cloudxino.Server) error {
	return a.printObject(v.Id, [][2]string{
		{"ID", v.Id}, {"Name", s(v.Name)}, {"Status", serverStatus(*v)},
		{"Image", s(v.Image)}, {"Size", s(v.Size)},
		{"CPU / memory / disk", fmt.Sprintf("%s vCPU / %s MB / %s GB", orDash(n(v.Cpu)), orDash(n(v.Memory)), orDash(n(v.Disk)))},
		{"Region", s(v.Region)}, {"Project", s(v.ProjectId)},
		{"IP", orDash(s(v.IpAddress)) + " (" + orDash(s(v.IpType)) + ")"}, {"Private IP", orDash(s(v.PrivateIp))},
		{"Login", s(v.LoginMethod)}, {"SSH keys", orDash(join(v.SshKeyIds))}, {"Tags", orDash(join(v.Tags))},
		{"Created", s(v.CreatedAt)},
	}, v)
}

func (a *app) serverListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List servers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			opts := &cloudxino.ListServersOptions{}
			if project != "" {
				p, err := a.findProject(cmd.Context(), project)
				if err != nil {
					return err
				}
				opts.ProjectID = p.Id
			}
			list, err := c.Servers.List(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return a.print(serverTable(list))
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only this project (ID or name)")
	return cmd
}

func (a *app) serverGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get SERVER",
		Short: "Show a server (by ID or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.findServer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printServer(&v)
		},
	}
}

func (a *app) serverCreateCmd() *cobra.Command {
	var (
		req                         cloudxino.CreateMC2Request
		region, project, ipType     string
		loginMethod, idempotencyKey string
		sshKeys, tags               []string
		passwordStdin, wait         bool
		timeout                     time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a server (billed from the team's wallet)",
		Example: `  cxctl server create --name web-01 --image debian-13 --size s-1vcpu-1gb --ssh-key laptop --wait
  cxctl server sizes     # sizes and prices
  cxctl server images    # images`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.api()
			if err != nil {
				return err
			}
			if req.Name == "" || req.Image == "" || req.Size == "" {
				return usagef("--name, --image and --size are required")
			}
			setStr(&req.Region, region)
			if ipType != "" {
				v := cloudxino.CreateMC2RequestIpType(ipType)
				req.IpType = &v
			}
			if loginMethod != "" {
				v := cloudxino.CreateMC2RequestLoginMethod(loginMethod)
				req.LoginMethod = &v
			}
			if passwordStdin {
				pw, err := readLine(a.in)
				if err != nil {
					return err
				}
				req.Password = &pw
			}
			if project != "" {
				p, err := a.findProject(ctx, project)
				if err != nil {
					return err
				}
				req.ProjectId = &p.Id
			}
			if len(sshKeys) > 0 {
				ids, err := a.sshKeyIDs(ctx, sshKeys)
				if err != nil {
					return err
				}
				req.SshKeyIds = &ids
			}
			if len(tags) > 0 {
				req.Tags = &tags
			}
			v, err := c.Servers.Create(ctx, req, idempotencyKey)
			if err != nil {
				return err
			}
			if wait {
				a.progress("Waiting for %s (%s) to be ready...", s(v.Name), v.Id)
				if v, err = c.Servers.WaitReady(ctx, v.Id, a.waitOpts(timeout)); err != nil {
					return err
				}
			}
			return a.printServer(v)
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Name, "name", "", "hostname: letters, digits and hyphens (required)")
	f.StringVar(&req.Image, "image", "", "OS image, e.g. debian-13 (required; see: cxctl server images)")
	f.StringVar(&req.Size, "size", "", "size slug, e.g. s-1vcpu-1gb (required; see: cxctl server sizes)")
	f.StringVar(&region, "region", "", "region ID (default: the default region; see: cxctl server regions)")
	f.StringVar(&project, "project", "", "project ID or name (default: the team's default project)")
	f.StringSliceVar(&sshKeys, "ssh-key", nil, "saved SSH key (ID, name or fingerprint) to put on the server; repeatable")
	f.StringSliceVar(&tags, "tag", nil, "tag; repeatable (Cloud Firewalls can apply by tag)")
	f.StringVar(&ipType, "ip-type", "", "public (own IPv4) or nat (shared IP with a port range)")
	f.StringVar(&loginMethod, "login-method", "", "key or password")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the root password from standard input (with --login-method password)")
	f.StringVar(&idempotencyKey, "idempotency-key", "", "reuse a key to make re-running this command safe: a second run returns the first server")
	f.BoolVar(&wait, "wait", false, "wait until the server is running")
	f.DurationVar(&timeout, "timeout", 20*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) serverUpdateCmd() *cobra.Command {
	var name string
	var tags []string
	cmd := &cobra.Command{
		Use:   "update SERVER",
		Short: "Rename a server or replace its tags",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := cloudxino.UpdateMC2Request{}
			if cmd.Flags().Changed("name") {
				req.Name = &name
			}
			if cmd.Flags().Changed("tag") {
				req.Tags = &tags
			}
			if req.Name == nil && req.Tags == nil {
				return usagef("nothing to change: pass --name and/or --tag")
			}
			v, err := a.findServer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			out, err := c.Servers.Update(cmd.Context(), v.Id, req)
			if err != nil {
				return err
			}
			return a.printServer(out)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "the new tags (replaces all); repeatable, --tag= for none")
	return cmd
}

func (a *app) serverDeleteCmd() *cobra.Command {
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "delete SERVER...",
		Aliases: []string{"rm"},
		Short:   "Delete servers and their disks",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.api()
			if err != nil {
				return err
			}
			var targets []cloudxino.Server
			for _, ref := range args {
				v, err := a.findServer(ctx, ref)
				if err != nil {
					return err
				}
				targets = append(targets, v)
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting %s (with its disk)", describeTargets(targets, func(v cloudxino.Server) string { return s(v.Name) }))); err != nil {
				return err
			}
			for _, v := range targets {
				if err := c.Servers.Delete(ctx, v.Id); err != nil {
					return fmt.Errorf("deleting %s: %w", v.Id, err)
				}
				if wait {
					if err := c.Servers.WaitDeleted(ctx, v.Id, a.waitOpts(timeout)); err != nil {
						return err
					}
					a.done("Deleted %s (%s).", s(v.Name), v.Id)
				} else {
					a.done("Deleting %s (%s).", s(v.Name), v.Id)
				}
				if a.output == "id" {
					fmt.Fprintln(a.out, v.Id)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the servers are gone")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "how long --wait waits for each server")
	return cmd
}

func (a *app) serverActionCmd(name, short string, do func(*cloudxino.ServersService, context.Context, string) (*cloudxino.ActionResult, error)) *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   name + " SERVER",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			v, err := a.findServer(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			res, err := do(c.Servers, ctx, v.Id)
			if err != nil {
				return err
			}
			if !wait {
				a.done("%s: %s", s(v.Name), orDash(res.Message))
				if a.output != "table" {
					return a.printObject(v.Id, nil, res)
				}
				return nil
			}
			out, err := c.Servers.WaitReady(ctx, v.Id, a.waitOpts(timeout))
			if err != nil {
				return err
			}
			return a.printServer(out)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the server is running or stopped")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) serverResizeCmd() *cobra.Command {
	var size string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "resize SERVER --size SIZE",
		Short: "Move a server to another size (disks can only grow)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if size == "" {
				return usagef("--size is required")
			}
			ctx := cmd.Context()
			v, err := a.findServer(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			res, err := c.Servers.Resize(ctx, v.Id, size)
			if err != nil {
				return err
			}
			if !wait {
				a.done("%s: %s", s(v.Name), orDash(res.Message))
				if a.output != "table" {
					return a.printObject(v.Id, nil, res)
				}
				return nil
			}
			out, err := c.Servers.WaitResized(ctx, v.Id, size, a.waitOpts(timeout))
			if err != nil {
				return err
			}
			return a.printServer(out)
		},
	}
	cmd.Flags().StringVar(&size, "size", "", "new size slug (required)")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the resize has finished")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) serverPrivateKeyCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "private-key SERVER",
		Short: "Download the SSH private key the platform made for a server",
		Long: `Download the SSH private key the platform made for a server created without --ssh-key.
Servers created with your own saved keys (--ssh-key) need no download.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, err := a.findServer(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			key, err := c.Servers.PrivateKey(cmd.Context(), v.Id)
			if err != nil {
				return err
			}
			if file == "" {
				_, err = a.out.Write(key)
				return err
			}
			if err := os.WriteFile(file, key, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(a.errOut, "Saved to %s (mode 0600).\n", file)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "write the key to this file (mode 0600) instead of standard output")
	return cmd
}

// The options endpoints answer the console's own shapes; these are the fields cxctl shows.
type sizeOption struct {
	Slug          string  `json:"slug"`
	Name          string  `json:"name"`
	CPU           int     `json:"cpu"`
	Memory        int     `json:"memory"`
	Disk          int     `json:"disk"`
	PricePerMonth float64 `json:"price_per_month"`
	PublicMonth   float64 `json:"public_price_per_month"`
}

type regionOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Location    string `json:"location"`
}

type imageOption struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	LoginUser   string `json:"login_user"`
}

func (a *app) serverSizesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sizes",
		Short: "List server sizes and monthly prices",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			raw, err := c.Servers.Sizes(cmd.Context())
			if err != nil {
				return err
			}
			var list []sizeOption
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("reading sizes: %w", err)
			}
			t := table{header: []string{"SLUG", "VCPU", "MEMORY", "DISK", "MONTHLY (NAT)", "MONTHLY (PUBLIC IP)"}, data: json.RawMessage(raw)}
			for _, z := range list {
				t.rows = append(t.rows, []string{z.Slug, strconv.Itoa(z.CPU), strconv.Itoa(z.Memory) + " GB", strconv.Itoa(z.Disk) + " GB",
					money(z.PricePerMonth), money(z.PublicMonth)})
				t.ids = append(t.ids, z.Slug)
			}
			return a.print(t)
		},
	}
}

func (a *app) serverRegionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "regions",
		Short: "List regions open for new servers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			raw, err := c.Servers.Regions(cmd.Context())
			if err != nil {
				return err
			}
			var list []regionOption
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("reading regions: %w", err)
			}
			t := table{header: []string{"ID", "NAME", "LOCATION"}, data: json.RawMessage(raw)}
			for _, r := range list {
				name := r.DisplayName
				if name == "" {
					name = r.Name
				}
				t.rows = append(t.rows, []string{r.ID, name, orDash(r.Location)})
				t.ids = append(t.ids, r.ID)
			}
			return a.print(t)
		},
	}
}

func (a *app) serverImagesCmd() *cobra.Command {
	var region string
	cmd := &cobra.Command{
		Use:   "images",
		Short: "List OS images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			raw, err := c.Servers.Images(cmd.Context(), region)
			if err != nil {
				return err
			}
			var list []imageOption
			if err := json.Unmarshal(raw, &list); err != nil {
				return fmt.Errorf("reading images: %w", err)
			}
			t := table{header: []string{"ID", "NAME", "LOGIN USER"}, data: json.RawMessage(raw)}
			for _, im := range list {
				t.rows = append(t.rows, []string{im.ID, im.Name, orDash(im.LoginUser)})
				t.ids = append(t.ids, im.ID)
			}
			return a.print(t)
		},
	}
	cmd.Flags().StringVar(&region, "region", "", "region ID")
	return cmd
}

func money(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) + " BDT" }

func setStr(dst **string, v string) {
	if v != "" {
		*dst = &v
	}
}

// progress tells a person what a long wait is for; it goes to stderr so output stays parseable.
func (a *app) progress(format string, args ...any) {
	fmt.Fprintf(a.errOut, format+"\n", args...)
}

func describeTargets[T any](items []T, name func(T) string) string {
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = name(it)
	}
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%d resources (%s)", len(names), strings.Join(names, ", "))
}
