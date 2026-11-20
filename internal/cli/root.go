// Package cli implements the cxctl commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

// defaultWaitInterval is how often --wait polls (tests make it short).
var defaultWaitInterval = 5 * time.Second

// app is what every command shares: flags, streams and the API client.
type app struct {
	version string
	in      io.Reader
	out     io.Writer
	errOut  io.Writer

	// global flags
	token      string
	apiURL     string
	contextArg string
	output     string
	noHeader   bool
	configPath string

	// for tests: how often waiters poll
	waitInterval time.Duration

	client *cloudxino.Client
}

// Main runs cxctl with args and returns the process exit code.
func Main(version string, args []string, in io.Reader, out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &app{version: version, in: in, out: out, errOut: errOut, waitInterval: defaultWaitInterval}
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(errOut, "Error:", describe(err))
		var u usageError
		if errors.As(err, &u) {
			return 2
		}
		return 1
	}
	return 0
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "cxctl",
		Short: "Manage CloudXino servers, firewalls, load balancers, reserved IPs, container hosts, DNS and SSH keys",
		Long: `cxctl manages CloudXino resources from the command line.

Log in once with an API key (Account → API keys in the console):

  cxctl auth login

or set CLOUDXINO_TOKEN. Most commands take -o json for scripts.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.version,
	}
	root.SetVersionTemplate("cxctl {{.Version}}\n")
	f := root.PersistentFlags()
	f.StringVar(&a.token, "token", "", "API key (default: CLOUDXINO_TOKEN, then the current context)")
	f.StringVar(&a.apiURL, "api-url", "", "API address (default: CLOUDXINO_API_URL, then the context, then "+cloudxino.DefaultBaseURL+")")
	f.StringVar(&a.contextArg, "context", "", "saved login to use (default: the current one)")
	f.StringVarP(&a.output, "output", "o", "table", "output format: table, json or id")
	f.BoolVar(&a.noHeader, "no-header", false, "leave out the table header")
	f.StringVar(&a.configPath, "config", "", "config file (default: "+defaultConfigPathHint()+")")

	root.AddCommand(
		a.authCmd(),
		a.accountCmd(),
		a.serverCmd(),
		a.firewallCmd(),
		a.loadBalancerCmd(),
		a.dnsCmd(),
		a.reservedIPCmd(),
		a.containersCmd(),
		a.sshKeyCmd(),
		a.projectCmd(),
	)
	return root
}

// api returns the client, made on first use from flags, environment and config.
func (a *app) api() (*cloudxino.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	token, apiURL, err := a.credentials()
	if err != nil {
		return nil, err
	}
	opts := []cloudxino.Option{cloudxino.WithUserAgent("cxctl/" + a.version)}
	if apiURL != "" {
		opts = append(opts, cloudxino.WithBaseURL(apiURL))
	}
	c, err := cloudxino.NewClient(token, opts...)
	if err != nil {
		return nil, err
	}
	a.client = c
	return c, nil
}

// credentials picks the token and API address: flag, then environment, then the saved context.
func (a *app) credentials() (token, apiURL string, err error) {
	token, apiURL = a.token, a.apiURL
	if token == "" {
		token = os.Getenv("CLOUDXINO_TOKEN")
	}
	if apiURL == "" {
		apiURL = os.Getenv("CLOUDXINO_API_URL")
	}
	if token != "" && a.contextArg == "" {
		return token, apiURL, nil
	}
	cfg, err := loadConfig(a.configFile())
	if err != nil {
		return "", "", err
	}
	name := a.contextArg
	if name == "" {
		name = cfg.Current
	}
	if name == "" {
		return "", "", errors.New("not logged in: run `cxctl auth login` or set CLOUDXINO_TOKEN")
	}
	c, ok := cfg.Contexts[name]
	if !ok {
		return "", "", fmt.Errorf("no saved login named %q (see `cxctl auth list`)", name)
	}
	if a.token == "" {
		token = c.Token
	}
	if apiURL == "" {
		apiURL = c.APIURL
	}
	return token, apiURL, nil
}

func (a *app) waitOpts(timeout time.Duration) *cloudxino.WaitOptions {
	return &cloudxino.WaitOptions{Interval: a.waitInterval, Timeout: timeout}
}

// usageError is a mistake in the command line (exit code 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// describe turns an error into one line for people: API errors without the request plumbing.
func describe(err error) string {
	var apiErr *cloudxino.APIError
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		if msg == "" {
			msg = strings.ToLower(httpStatusText(apiErr.StatusCode))
		}
		if apiErr.Code != "" {
			msg += " (" + apiErr.Code + ")"
		}
		if apiErr.RequestID != "" {
			msg += " [request " + apiErr.RequestID + "]"
		}
		return msg
	}
	var stuck *cloudxino.ErrStuck
	if errors.As(err, &stuck) {
		return strings.TrimPrefix(stuck.Error(), "cloudxino: ")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out waiting; the operation may still finish (check again with `get`)"
	}
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return err.Error()
}
