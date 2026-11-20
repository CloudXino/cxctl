package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) containersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "containers",
		Aliases: []string{"container", "mcs"},
		Short:   "Manage container hosts (Managed Container Service) and deploy docker-compose.yml to them",
		Long: `A container host is a CC2 server of the team's own with Docker, Docker Compose, Traefik
(automatic Let's Encrypt certificates) and Portainer. Deploy a docker-compose.yml to it, with
environment variables and domains served over HTTPS. It is billed per hour (the server plus the
management fee) until you delete it.

Hosts are named by ID or by name. Containers are named by container name or compose service.`,
		Example: `  cxctl containers plans
  cxctl containers create --name apps --plan s-2vcpu-4gb --wait
  cxctl containers env set apps DATABASE_URL=postgres://... --no-deploy
  cxctl containers domains add apps app.example.com --service web --port 8080 --no-deploy
  cxctl containers deploy apps -f docker-compose.yml --wait
  cxctl containers logs apps web --lines 100`,
	}
	cmd.AddCommand(
		a.mcsListCmd(), a.mcsGetCmd(), a.mcsCreateCmd(), a.mcsDeleteCmd(), a.mcsRetryCmd(),
		a.mcsCredentialsCmd(), a.mcsStatusCmd(),
		a.mcsDeployCmd(), a.mcsDeploymentsCmd(), a.mcsRedeployCmd(),
		a.mcsEnvCmd(), a.mcsDomainsCmd(),
		a.mcsLogsCmd(),
		a.mcsActionCmd(cloudxino.ContainerRestart, "Restart a container"),
		a.mcsActionCmd(cloudxino.ContainerStop, "Stop a container (not the host's own Traefik or Portainer)"),
		a.mcsActionCmd(cloudxino.ContainerStart, "Start a stopped container"),
		a.mcsPlansCmd(), a.mcsPlanCmd(),
	)
	return cmd
}

// findHost takes an ID or a name.
func (a *app) findHost(ctx context.Context, ref string) (cloudxino.ContainerHost, error) {
	c, err := a.api()
	if err != nil {
		return cloudxino.ContainerHost{}, err
	}
	all, err := c.ContainerHosts.List(ctx, nil)
	if err != nil {
		return cloudxino.ContainerHost{}, err
	}
	return pick("container host", ref, all, func(v cloudxino.ContainerHost) string { return v.Id }, func(v cloudxino.ContainerHost) string { return v.Name })
}

func domainNames(ds []cloudxino.ContainerDomain) string {
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Domain
	}
	return strings.Join(names, ",")
}

func lastDeploy(d *cloudxino.ContainerDeployment) string {
	if d == nil {
		return "-"
	}
	return fmt.Sprintf("#%d %s", d.Number, d.Status)
}

func hostTable(list []cloudxino.ContainerHost) table {
	t := table{header: []string{"ID", "NAME", "STATUS", "PLAN", "REGION", "IP", "DOMAINS", "LAST DEPLOY"}, data: list}
	for _, v := range list {
		t.rows = append(t.rows, []string{v.Id, v.Name, v.Status, v.Plan, v.Server.Region, orDash(v.Server.IpAddress),
			orDash(domainNames(v.Domains)), lastDeploy(v.LastDeployment)})
		t.ids = append(t.ids, v.Id)
	}
	return t
}

func (a *app) printHost(v *cloudxino.ContainerHost) error {
	price := "-"
	if v.Price != nil {
		price = fmt.Sprintf("%s (server %s + fee %s)", money(v.Price.PerMonth), money(v.Price.ServerPerMonth), money(v.Price.FeePerMonth))
	}
	fields := [][2]string{
		{"ID", v.Id}, {"Name", v.Name}, {"Status", v.Status}, {"Plan", v.Plan}, {"Region", v.Server.Region},
		{"IP", orDash(v.Server.IpAddress)}, {"Portainer", v.PortainerUrl}, {"Project", v.ProjectId},
		{"Domains", orDash(domainNames(v.Domains))}, {"Env vars", orDash(strings.Join(v.EnvKeys, ","))},
		{"Last deploy", lastDeploy(v.LastDeployment)}, {"Per month", price}, {"Created", s(v.CreatedAt)},
	}
	if v.Resize != nil && v.Resize.To != "" {
		fields = append(fields, [2]string{"Plan change", v.Resize.To + " " + orDash(s(v.Resize.Error))})
	}
	if e := s(v.Error); e != "" {
		fields = append(fields, [2]string{"Error", e})
	}
	return a.printObject(v.Id, fields, v)
}

// num prints a number without trailing zeros (2, 2.5).
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func (a *app) mcsListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List container hosts",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.api()
			if err != nil {
				return err
			}
			opts := &cloudxino.ListContainerHostsOptions{}
			if project != "" {
				p, err := a.findProject(ctx, project)
				if err != nil {
					return err
				}
				opts.ProjectID = p.Id
			}
			list, err := c.ContainerHosts.List(ctx, opts)
			if err != nil {
				return err
			}
			return a.print(hostTable(list))
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only hosts in this project (ID or name)")
	return cmd
}

func (a *app) mcsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get HOST",
		Short: "Show a container host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			v, err := c.ContainerHosts.Get(cmd.Context(), h.Id)
			if err != nil {
				return err
			}
			return a.printHost(v)
		},
	}
}

func (a *app) mcsCreateCmd() *cobra.Command {
	var (
		req                             cloudxino.CreateContainerHostRequest
		region, project, idempotencyKey string
		wait                            bool
		timeout                         time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a container host (billed from the team's wallet)",
		Long: `Create a container host: a server with Docker, Compose, Traefik and Portainer. Installing takes
about 5 minutes; --wait waits until it is ready. Then deploy a compose file with
cxctl containers deploy.`,
		Example: `  cxctl containers plans     # plans and prices
  cxctl containers create --name apps --plan s-2vcpu-4gb --wait`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.api()
			if err != nil {
				return err
			}
			if req.Name == "" || req.Plan == "" {
				return usagef("--name and --plan are required (see: cxctl containers plans)")
			}
			setStr(&req.Region, region)
			if project != "" {
				p, err := a.findProject(ctx, project)
				if err != nil {
					return err
				}
				req.ProjectId = &p.Id
			}
			v, err := c.ContainerHosts.Create(ctx, req, idempotencyKey)
			if err != nil {
				return err
			}
			if wait {
				a.progress("Waiting for %s (%s) to be ready; installing Docker takes about 5 minutes...", v.Name, v.Id)
				if v, err = c.ContainerHosts.WaitReady(ctx, v.Id, a.waitOpts(timeout)); err != nil {
					return err
				}
			}
			return a.printHost(v)
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Name, "name", "", "host name: letters, digits and hyphens (required)")
	f.StringVar(&req.Plan, "plan", "", "plan slug, e.g. s-2vcpu-4gb (required; see: cxctl containers plans)")
	f.StringVar(&region, "region", "", "region ID (default: the default region)")
	f.StringVar(&project, "project", "", "project ID or name (default: the team's default project)")
	f.StringVar(&idempotencyKey, "idempotency-key", "", "reuse a key to make re-running this command safe: a second run returns the first host")
	f.BoolVar(&wait, "wait", false, "wait until the host is ready")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) mcsDeleteCmd() *cobra.Command {
	var yes, wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:     "delete HOST",
		Aliases: []string{"rm"},
		Short:   "Delete a container host, its server and every container and volume on it",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting container host %s with its server, containers and volumes", h.Name)); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.ContainerHosts.Delete(ctx, h.Id); err != nil {
				return err
			}
			if wait {
				if err := c.ContainerHosts.WaitDeleted(ctx, h.Id, a.waitOpts(timeout)); err != nil {
					return err
				}
				a.done("Deleted container host %s (%s).", h.Name, h.Id)
				return nil
			}
			a.done("Deleting container host %s (%s).", h.Name, h.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the host is gone")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) mcsRetryCmd() *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "retry HOST",
		Short: "Run the installer again on a host whose install failed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			v, err := c.ContainerHosts.Retry(ctx, h.Id)
			if err != nil {
				return err
			}
			if wait {
				if v, err = c.ContainerHosts.WaitReady(ctx, h.Id, a.waitOpts(timeout)); err != nil {
					return err
				}
			}
			return a.printHost(v)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the host is ready")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) mcsCredentialsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "credentials HOST",
		Short: "Show the first Portainer login (no longer valid once changed in Portainer)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			v, err := c.ContainerHosts.Credentials(cmd.Context(), h.Id)
			if err != nil {
				return err
			}
			return a.printObject(h.Id, [][2]string{{"Portainer", v.PortainerUrl}, {"Username", v.Username}, {"Password", v.Password}, {"Note", v.Note}}, v)
		},
	}
}

func (a *app) mcsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status HOST",
		Aliases: []string{"ps"},
		Short:   "List the containers on a host with their CPU and memory, read from the host now",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			st, err := c.ContainerHosts.Status(cmd.Context(), h.Id)
			if err != nil {
				return err
			}
			t := table{header: []string{"NAME", "SERVICE", "IMAGE", "STATE", "STATUS", "CPU", "MEMORY"}, data: st}
			for _, ct := range st.Containers {
				service := ct.Service
				if ct.System {
					service = "(system)"
				}
				cpu, mem := "-", "-"
				if ct.Stats != nil {
					cpu, mem = orDash(ct.Stats.Cpu), orDash(ct.Stats.Memory)
				}
				t.rows = append(t.rows, []string{ct.Name, orDash(service), ct.Image, ct.State, ct.Status, cpu, mem})
				t.ids = append(t.ids, ct.Name)
			}
			return a.print(t)
		},
	}
}

// findContainer resolves a container name or compose service on a host to the container's name.
func (a *app) findContainer(ctx context.Context, hostID, ref string) (string, error) {
	c, _ := a.api()
	st, err := c.ContainerHosts.Status(ctx, hostID)
	if err != nil {
		return "", err
	}
	byService := []string{}
	for _, ct := range st.Containers {
		if ct.Name == ref {
			return ref, nil
		}
		if ct.Service == ref && !ct.System {
			byService = append(byService, ct.Name)
		}
	}
	switch len(byService) {
	case 1:
		return byService[0], nil
	case 0:
		return "", fmt.Errorf("no container or compose service named %q on this host (see: cxctl containers status)", ref)
	}
	return "", fmt.Errorf("service %q runs %d containers; name one: %s", ref, len(byService), strings.Join(byService, ", "))
}

func (a *app) mcsLogsCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "logs HOST CONTAINER",
		Short: "Show the last lines of a container's log (CONTAINER: container name or compose service)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			name, err := a.findContainer(ctx, h.Id, args[1])
			if err != nil {
				return err
			}
			c, _ := a.api()
			logs, err := c.ContainerHosts.Logs(ctx, h.Id, name, lines)
			if err != nil {
				return err
			}
			if a.output == "json" {
				return a.printJSON(map[string]string{"container": name, "logs": logs})
			}
			_, err = io.WriteString(a.out, logs)
			return err
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 200, "how many lines (10-2000)")
	return cmd
}

func (a *app) mcsActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " HOST CONTAINER",
		Short: short + " (CONTAINER: container name or compose service)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			name, err := a.findContainer(ctx, h.Id, args[1])
			if err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.ContainerHosts.ContainerAction(ctx, h.Id, name, action); err != nil {
				return err
			}
			a.done("%s %s: done.", action, name)
			return nil
		},
	}
}

func (a *app) mcsPlansCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "plans",
		Short: "List container host plans and monthly prices (BDT)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			o, err := c.ContainerHosts.Options(cmd.Context())
			if err != nil {
				return err
			}
			if !o.Enabled && a.output == "table" {
				a.progress("Ordering container hosts is switched off right now.")
			}
			t := table{header: []string{"PLAN", "CPU", "MEMORY", "DISK", "SERVER/MO", "FEE/MO", "TOTAL/MO"}, data: o}
			for _, p := range o.Plans {
				t.rows = append(t.rows, []string{p.Slug, strconv.Itoa(p.Cpu), num(p.MemoryGb) + " GB", strconv.Itoa(p.DiskGb) + " GB",
					money(p.ServerPerMonth), money(p.FeePerMonth), money(p.PerMonth)})
				t.ids = append(t.ids, p.Slug)
			}
			return a.print(t)
		},
	}
}

func (a *app) mcsPlanCmd() *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "plan HOST PLAN",
		Short: "Move a host to another plan (resizes its server: about a minute of downtime)",
		Long: `Move a host to another plan. Its server is resized and restarted (about a minute of
downtime). Disks only grow, so a plan with a smaller disk is refused.`,
		Example: `  cxctl containers plan apps s-4vcpu-8gb --wait`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			v, err := c.ContainerHosts.ChangePlan(ctx, h.Id, args[1])
			if err != nil {
				return err
			}
			if wait {
				a.progress("Waiting for %s to move to %s...", h.Name, args[1])
				if v, err = c.ContainerHosts.WaitPlan(ctx, h.Id, args[1], a.waitOpts(timeout)); err != nil {
					return err
				}
			}
			return a.printHost(v)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until the host is on the new plan")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

// --- deployments ---------------------------------------------------------------------------------

func deploymentTable(list []cloudxino.ContainerDeployment) table {
	t := table{header: []string{"#", "ID", "STATUS", "NOTE", "BY", "STARTED", "FINISHED"}, data: list}
	for _, d := range list {
		t.rows = append(t.rows, []string{strconv.Itoa(d.Number), d.Id, d.Status, orDash(s(d.Note)), orDash(s(d.CreatedBy)),
			orDash(s(d.StartedAt)), orDash(s(d.FinishedAt))})
		t.ids = append(t.ids, d.Id)
	}
	return t
}

func (a *app) printDeployment(d *cloudxino.ContainerDeployment) error {
	return a.printObject(d.Id, [][2]string{
		{"Deployment", "#" + strconv.Itoa(d.Number)}, {"ID", d.Id}, {"Status", d.Status}, {"Note", orDash(s(d.Note))},
		{"Env vars", orDash(strings.Join(d.EnvKeys, ","))}, {"Domains", orDash(domainNames(d.Domains))},
		{"By", orDash(s(d.CreatedBy))}, {"Started", orDash(s(d.StartedAt))}, {"Finished", orDash(s(d.FinishedAt))},
	}, d)
}

// errDeploymentFailed is returned when a followed deployment fails (its output was already shown).
var errDeploymentFailed = errors.New("deployment failed")

// follow polls a running deployment, writes its new output to stderr as it comes, and returns it
// once it has finished. A failed deployment returns an error after its output.
func (a *app) follow(ctx context.Context, hostID string, dep *cloudxino.ContainerDeployment, timeout time.Duration) (*cloudxino.ContainerDeployment, error) {
	c, _ := a.api()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	a.progress("Deployment #%d started; following its output...", dep.Number)
	shown := ""
	for {
		if out := s(dep.Output); out != "" {
			fmt.Fprint(a.errOut, newOutput(shown, out))
			shown = out
		}
		switch dep.Status {
		case cloudxino.DeploymentSucceeded:
			a.progress("Deployment #%d succeeded.", dep.Number)
			return dep, nil
		case cloudxino.DeploymentFailed:
			return dep, fmt.Errorf("%w: #%d (cxctl containers redeploy rolls back to an earlier one)", errDeploymentFailed, dep.Number)
		}
		t := time.NewTimer(a.waitInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return dep, ctx.Err()
		case <-t.C:
		}
		next, err := c.ContainerHosts.GetDeployment(ctx, hostID, dep.Id)
		if err != nil {
			return dep, err
		}
		dep = next
	}
}

// newOutput is the part of out not shown yet. The API keeps only the end of a long output, so
// when shown is no longer a prefix, the part after its last line is looked for.
func newOutput(shown, out string) string {
	if strings.HasPrefix(out, shown) {
		return ensureNewline(out[len(shown):])
	}
	lines := strings.Split(strings.TrimRight(shown, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "" {
		if i := strings.LastIndex(out, last+"\n"); i >= 0 {
			return ensureNewline(out[i+len(last)+1:])
		}
	}
	return ensureNewline(out)
}

func ensureNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

// finishDeploy prints a deployment that was started, following it with --wait.
func (a *app) finishDeploy(ctx context.Context, hostID string, dep *cloudxino.ContainerDeployment, wait bool, timeout time.Duration) error {
	if dep == nil {
		a.done("Saved; no compose file is deployed yet, so nothing was deployed.")
		return nil
	}
	if wait {
		var err error
		if dep, err = a.follow(ctx, hostID, dep, timeout); err != nil {
			if errors.Is(err, errDeploymentFailed) && a.output != "table" {
				_ = a.printDeployment(dep)
			}
			return err
		}
	}
	return a.printDeployment(dep)
}

func readInput(in io.Reader, file string) (string, error) {
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(in)
	} else {
		data, err = os.ReadFile(file)
	}
	return string(data), err
}

func (a *app) mcsDeployCmd() *cobra.Command {
	var file, envFile, note string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "deploy HOST [-f docker-compose.yml]",
		Short: "Deploy a compose file to a host (without -f: deploy the saved one again)",
		Long: `Deploy a docker-compose.yml: it is saved on the host and started with docker compose up, with
the host's environment variables and domains. Only one deployment runs at a time.

--env-file replaces all of the host's environment variables with the NAME=value lines of a .env
file first. --wait follows the deployment's output and fails when the deployment fails, which suits
CI:

  - run: cxctl containers deploy apps -f docker-compose.yml --note "$GITHUB_SHA" --wait
    env:
      CLOUDXINO_TOKEN: ${{ secrets.CLOUDXINO_TOKEN }}

Ports 80 and 443 belong to Traefik: publish services with "cxctl containers domains add" instead.`,
		Example: `  cxctl containers deploy apps -f docker-compose.yml --wait
  cat docker-compose.yml | cxctl containers deploy apps -f - --env-file .env.production
  cxctl containers deploy apps          # the saved compose file again`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			req := cloudxino.DeployRequest{}
			if file != "" {
				compose, err := readInput(a.in, file)
				if err != nil {
					return err
				}
				if strings.TrimSpace(compose) == "" {
					return usagef("%s is empty", file)
				}
				req.Compose = &compose
			}
			setStr(&req.Note, note)
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			if envFile != "" {
				text, err := readInput(a.in, envFile)
				if err != nil {
					return err
				}
				if _, err := c.ContainerHosts.SetEnvText(ctx, h.Id, text, false); err != nil {
					return err
				}
			}
			dep, err := c.ContainerHosts.Deploy(ctx, h.Id, req)
			if err != nil {
				return err
			}
			return a.finishDeploy(ctx, h.Id, dep, wait, timeout)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&file, "file", "f", "", "compose file to deploy (- reads standard input)")
	f.StringVar(&envFile, "env-file", "", "replace the host's environment variables with this .env file first")
	f.StringVar(&note, "note", "", "a note for the deployment list, e.g. the commit (up to 200 characters)")
	f.BoolVar(&wait, "wait", false, "follow the output and wait until the deployment has finished")
	f.DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

func (a *app) mcsDeploymentsCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "deployments HOST",
		Short: "List a host's deployments, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			list, err := c.ContainerHosts.ListDeployments(cmd.Context(), h.Id, &cloudxino.ListDeploymentsOptions{Limit: limit})
			if err != nil {
				return err
			}
			return a.print(deploymentTable(list))
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "how many (at most 200)")
	return cmd
}

// findDeployment takes a deployment ID or number ("3" or "#3").
func (a *app) findDeployment(ctx context.Context, hostID, ref string) (cloudxino.ContainerDeployment, error) {
	c, _ := a.api()
	list, err := c.ContainerHosts.ListDeployments(ctx, hostID, &cloudxino.ListDeploymentsOptions{Limit: 200})
	if err != nil {
		return cloudxino.ContainerDeployment{}, err
	}
	return pick("deployment", strings.TrimPrefix(ref, "#"), list,
		func(d cloudxino.ContainerDeployment) string { return d.Id },
		func(d cloudxino.ContainerDeployment) string { return strconv.Itoa(d.Number) })
}

func (a *app) mcsRedeployCmd() *cobra.Command {
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "redeploy HOST DEPLOYMENT",
		Short: "Deploy an earlier deployment's compose file and domains again (a rollback; env vars stay as they are)",
		Example: `  cxctl containers deployments apps
  cxctl containers redeploy apps 3 --wait`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			old, err := a.findDeployment(ctx, h.Id, args[1])
			if err != nil {
				return err
			}
			c, _ := a.api()
			dep, err := c.ContainerHosts.Redeploy(ctx, h.Id, old.Id)
			if err != nil {
				return err
			}
			return a.finishDeploy(ctx, h.Id, dep, wait, timeout)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "follow the output and wait until the deployment has finished")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "how long --wait waits")
	return cmd
}

// --- env vars ------------------------------------------------------------------------------------

func (a *app) mcsEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Show and change a host's environment variables",
		Long: `Environment variables reach every service of the compose file, and can be used as ${NAME} in it.
They are kept encrypted. Changing them deploys the saved compose file again, unless you pass
--no-deploy (then they take effect with the next deployment).`,
	}
	cmd.AddCommand(a.mcsEnvGetCmd(), a.mcsEnvSetCmd(), a.mcsEnvUnsetCmd())
	return cmd
}

func (a *app) mcsEnvGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get HOST [NAME]",
		Aliases: []string{"list", "ls"},
		Short:   "Show the environment variables with their values (NAME=value lines), or one value",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			env, err := c.ContainerHosts.GetEnv(cmd.Context(), h.Id)
			if err != nil {
				return err
			}
			if len(args) == 2 {
				v, ok := env[args[1]]
				if !ok {
					return fmt.Errorf("%s has no environment variable %s", h.Name, args[1])
				}
				if a.output == "json" {
					return a.printJSON(map[string]string{args[1]: v})
				}
				fmt.Fprintln(a.out, v)
				return nil
			}
			if err := a.checkOutput(); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printJSON(env)
			}
			keys := make([]string, 0, len(env))
			for k := range env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if a.output == "id" {
					fmt.Fprintln(a.out, k)
				} else {
					fmt.Fprintf(a.out, "%s=%s\n", k, env[k])
				}
			}
			return nil
		},
	}
}

// deployFlags are the flags of every command that may deploy.
type deployFlags struct {
	noDeploy bool
	wait     bool
	timeout  time.Duration
}

func (d *deployFlags) add(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&d.noDeploy, "no-deploy", false, "only save; the change takes effect with the next deployment")
	f.BoolVar(&d.wait, "wait", false, "follow the deployment's output and wait until it has finished")
	f.DurationVar(&d.timeout, "timeout", 30*time.Minute, "how long --wait waits")
}

func (a *app) saveEnv(cmd *cobra.Command, host string, d deployFlags, change func(env map[string]string) error) error {
	ctx := cmd.Context()
	h, err := a.findHost(ctx, host)
	if err != nil {
		return err
	}
	c, _ := a.api()
	env, err := c.ContainerHosts.GetEnv(ctx, h.Id)
	if err != nil {
		return err
	}
	if err := change(env); err != nil {
		return err
	}
	res, err := c.ContainerHosts.SetEnv(ctx, h.Id, env, !d.noDeploy)
	if err != nil {
		return err
	}
	if d.noDeploy {
		a.done("Saved %d environment variables on %s; they take effect with the next deployment.", len(res.EnvKeys), h.Name)
		return nil
	}
	return a.finishDeploy(ctx, h.Id, res.Deployment, d.wait, d.timeout)
}

func (a *app) mcsEnvSetCmd() *cobra.Command {
	var d deployFlags
	var file string
	cmd := &cobra.Command{
		Use:   "set HOST NAME=value... [--from-file .env]",
		Short: "Add or change environment variables (the others stay)",
		Example: `  cxctl containers env set apps LOG_LEVEL=debug
  cxctl containers env set apps --from-file .env.production --no-deploy`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			set := map[string]string{}
			if file != "" {
				text, err := readInput(a.in, file)
				if err != nil {
					return err
				}
				for n, line := range strings.Split(text, "\n") {
					line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					k, v, ok := strings.Cut(line, "=")
					if !ok {
						return usagef("%s line %d: write NAME=value", file, n+1)
					}
					v = strings.TrimSpace(v)
					if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
						v = v[1 : len(v)-1]
					}
					set[strings.TrimSpace(k)] = v
				}
			}
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usagef("write each variable as NAME=value, not %q", kv)
				}
				set[k] = v
			}
			if len(set) == 0 {
				return usagef("give NAME=value or --from-file")
			}
			return a.saveEnv(cmd, args[0], d, func(env map[string]string) error {
				for k, v := range set {
					env[k] = v
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&file, "from-file", "", "add the NAME=value lines of a .env file (- reads standard input)")
	d.add(cmd)
	return cmd
}

func (a *app) mcsEnvUnsetCmd() *cobra.Command {
	var d deployFlags
	cmd := &cobra.Command{
		Use:     "unset HOST NAME...",
		Aliases: []string{"rm"},
		Short:   "Remove environment variables",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.saveEnv(cmd, args[0], d, func(env map[string]string) error {
				for _, k := range args[1:] {
					if _, ok := env[k]; !ok {
						return fmt.Errorf("%s has no environment variable %s", args[0], k)
					}
					delete(env, k)
				}
				return nil
			})
		},
	}
	d.add(cmd)
	return cmd
}

// --- domains -------------------------------------------------------------------------------------

func (a *app) mcsDomainsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "domains",
		Aliases: []string{"domain"},
		Short:   "Serve compose services on domains over HTTPS",
		Long: `Each domain is routed by Traefik to a port of a compose service, with a Let's Encrypt
certificate. Point the domain's A record at the host's IP address (cxctl containers get) first.
Changing domains deploys the saved compose file again, unless you pass --no-deploy.`,
	}
	cmd.AddCommand(a.mcsDomainsListCmd(), a.mcsDomainsAddCmd(), a.mcsDomainsRemoveCmd())
	return cmd
}

func (a *app) mcsDomainsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list HOST",
		Aliases: []string{"ls"},
		Short:   "List a host's domains with their DNS and certificate state",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			h, err := a.findHost(ctx, args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			t := table{header: []string{"DOMAIN", "SERVICE", "PORT", "DNS", "CERTIFICATE"}}
			st, err := c.ContainerHosts.Status(ctx, h.Id)
			if cloudxino.IsConflict(err) { // not ready: no live state, just what is saved
				t.data = h.Domains
				for _, d := range h.Domains {
					t.rows = append(t.rows, []string{d.Domain, d.Service, strconv.Itoa(d.Port), "-", "-"})
					t.ids = append(t.ids, d.Domain)
				}
				return a.print(t)
			}
			if err != nil {
				return err
			}
			t.data = st.Domains
			for _, d := range st.Domains {
				dns := orDash(strings.Join(d.DNS, ","))
				if !d.PointsHere {
					dns += " (not this host)"
				}
				t.rows = append(t.rows, []string{d.Domain, d.Service, strconv.Itoa(d.Port), dns, strings.ReplaceAll(d.SSL, "_", " ")})
				t.ids = append(t.ids, d.Domain)
			}
			return a.print(t)
		},
	}
}

func (a *app) saveDomains(cmd *cobra.Command, h cloudxino.ContainerHost, domains []cloudxino.ContainerDomain, d deployFlags) error {
	ctx := cmd.Context()
	c, _ := a.api()
	res, err := c.ContainerHosts.SetDomains(ctx, h.Id, domains, !d.noDeploy)
	if err != nil {
		return err
	}
	if d.noDeploy {
		a.done("Saved %d domains on %s; they take effect with the next deployment.", len(res.Domains), h.Name)
		return nil
	}
	return a.finishDeploy(ctx, h.Id, res.Deployment, d.wait, d.timeout)
}

func (a *app) mcsDomainsAddCmd() *cobra.Command {
	var d deployFlags
	var service string
	var port int
	cmd := &cobra.Command{
		Use:     "add HOST DOMAIN --service SERVICE --port PORT",
		Short:   "Serve a compose service's port on a domain (or change where a domain goes)",
		Example: `  cxctl containers domains add apps app.example.com --service web --port 8080 --wait`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if service == "" || port == 0 {
				return usagef("--service and --port are required")
			}
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			name := strings.TrimRight(strings.ToLower(strings.TrimSpace(args[1])), ".")
			domains := []cloudxino.ContainerDomain{}
			for _, dm := range h.Domains {
				if dm.Domain != name {
					domains = append(domains, dm)
				}
			}
			domains = append(domains, cloudxino.ContainerDomain{Domain: name, Service: service, Port: port})
			return a.saveDomains(cmd, h, domains, d)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "compose service to route to (required)")
	cmd.Flags().IntVar(&port, "port", 0, "port the service listens on inside its container (required)")
	d.add(cmd)
	return cmd
}

func (a *app) mcsDomainsRemoveCmd() *cobra.Command {
	var d deployFlags
	cmd := &cobra.Command{
		Use:     "remove HOST DOMAIN...",
		Aliases: []string{"rm"},
		Short:   "Stop serving domains",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.findHost(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			drop := map[string]bool{}
			for _, name := range args[1:] {
				drop[strings.TrimRight(strings.ToLower(strings.TrimSpace(name)), ".")] = true
			}
			domains := []cloudxino.ContainerDomain{}
			for _, dm := range h.Domains {
				if drop[dm.Domain] {
					delete(drop, dm.Domain)
					continue
				}
				domains = append(domains, dm)
			}
			for name := range drop {
				return fmt.Errorf("%s does not serve %s", h.Name, name)
			}
			return a.saveDomains(cmd, h, domains, d)
		},
	}
	d.add(cmd)
	return cmd
}
