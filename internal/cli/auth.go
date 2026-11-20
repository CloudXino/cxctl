package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Save, switch and remove logins (API keys)",
	}
	cmd.AddCommand(a.authLoginCmd(), a.authListCmd(), a.authUseCmd(), a.authLogoutCmd())
	return cmd
}

func (a *app) authLoginCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save an API key as a named login and make it current",
		Long: `Save an API key (Account → API keys in the console) and make it the current login.

The key is read from standard input, so it does not end up in your shell history, and it is
not shown as you type or paste it:

  cxctl auth login --name work
  echo "$KEY" | cxctl auth login --name ci

The key is checked against the API before it is saved.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := a.token
			if token == "" {
				var err error
				if token, err = readSecret(a.in, a.errOut, "API key (not shown as you type): "); err != nil {
					return err
				}
			}
			if token == "" {
				return usagef("no API key given")
			}
			apiURL := a.apiURL
			if apiURL == "" {
				apiURL = os.Getenv("CLOUDXINO_API_URL")
			}
			opts := []cloudxino.Option{cloudxino.WithUserAgent("cxctl/" + a.version)}
			if apiURL != "" {
				opts = append(opts, cloudxino.WithBaseURL(apiURL))
			}
			c, err := cloudxino.NewClient(token, opts...)
			if err != nil {
				return err
			}
			me, err := c.Account.Me(cmd.Context())
			if err != nil {
				return fmt.Errorf("checking the key: %s", describe(err))
			}
			cfg, err := loadConfig(a.configFile())
			if err != nil {
				return err
			}
			ctx := configContext{Token: token, APIURL: apiURL}
			if me.Organization != nil {
				ctx.Team = me.Organization.Name
			}
			if me.APIKey != nil {
				ctx.Role = me.APIKey.Role
				ctx.KeyPrefix = me.APIKey.KeyPrefix
			}
			if name == "" {
				name = "default"
			}
			cfg.Contexts[name] = ctx
			cfg.Current = name
			if err := cfg.save(a.configFile()); err != nil {
				return err
			}
			a.done("Logged in to %s as %q (role %s); saved as %q in %s.", orDash(ctx.Team), me.Email, orDash(ctx.Role), name, a.configFile())
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", `name for this login (default "default")`)
	return cmd
}

func (a *app) authListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List saved logins",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(a.configFile())
			if err != nil {
				return err
			}
			type row struct {
				Name    string `json:"name"`
				Current bool   `json:"current"`
				Team    string `json:"team"`
				Role    string `json:"role"`
				APIURL  string `json:"api_url"`
				Key     string `json:"key"`
			}
			t := table{header: []string{"CURRENT", "NAME", "TEAM", "ROLE", "KEY", "API"}}
			var data []row
			for _, n := range cfg.names() {
				c := cfg.Contexts[n]
				r := row{Name: n, Current: n == cfg.Current, Team: c.Team, Role: c.Role, APIURL: c.APIURL, Key: c.KeyPrefix}
				data = append(data, r)
				mark := ""
				if r.Current {
					mark = "*"
				}
				t.rows = append(t.rows, []string{mark, n, orDash(c.Team), orDash(c.Role), orDash(r.Key), orDash(c.APIURL)})
				t.ids = append(t.ids, n)
			}
			t.data = data
			if data == nil {
				t.data = []row{}
			}
			return a.print(t)
		},
	}
}

func (a *app) authUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Make a saved login current",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(a.configFile())
			if err != nil {
				return err
			}
			if _, ok := cfg.Contexts[args[0]]; !ok {
				return fmt.Errorf("no saved login named %q (have: %s)", args[0], strings.Join(cfg.names(), ", "))
			}
			cfg.Current = args[0]
			if err := cfg.save(a.configFile()); err != nil {
				return err
			}
			a.done("Now using %q.", args[0])
			return nil
		},
	}
}

func (a *app) authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout [NAME]",
		Short: "Forget a saved login (default: the current one)",
		Long:  "Forget a saved login. This only removes the key from this computer; revoke it in the console to stop it working.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(a.configFile())
			if err != nil {
				return err
			}
			name := cfg.Current
			if len(args) == 1 {
				name = args[0]
			}
			if _, ok := cfg.Contexts[name]; !ok || name == "" {
				return errors.New("no such saved login")
			}
			delete(cfg.Contexts, name)
			if cfg.Current == name {
				cfg.Current = ""
			}
			if err := cfg.save(a.configFile()); err != nil {
				return err
			}
			a.done("Forgot %q. Revoke the key in the console if it should stop working.", name)
			return nil
		},
	}
}
