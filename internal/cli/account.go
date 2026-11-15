package cli

import (
	"context"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) accountCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "account",
		Aliases: []string{"whoami"},
		Short:   "Show the team and role the API key acts for",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			me, err := c.Account.Me(cmd.Context())
			if err != nil {
				return err
			}
			fields := [][2]string{{"Email", me.Email}, {"Name", orDash(me.Name)}}
			id := me.ID
			if me.Organization != nil {
				fields = append(fields, [2]string{"Team", me.Organization.Name}, [2]string{"Team role", me.Organization.Role})
				id = me.Organization.ID
			}
			if me.APIKey != nil {
				fields = append(fields, [2]string{"API key", me.APIKey.Name + " (" + me.APIKey.KeyPrefix + "…)"},
					[2]string{"Key role", orDash(me.APIKey.RoleLabel)})
				if me.APIKey.ExpiresAt != nil {
					fields = append(fields, [2]string{"Key expires", *me.APIKey.ExpiresAt})
				}
			}
			return a.printObject(id, fields, me)
		},
	}
}

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Aliases: []string{"projects"}, Short: "List the team's projects"}
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List projects",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.api()
			if err != nil {
				return err
			}
			ps, err := c.Projects.List(cmd.Context())
			if err != nil {
				return err
			}
			t := table{header: []string{"ID", "NAME", "DEFAULT", "DESCRIPTION"}, data: ps}
			for _, p := range ps {
				def := ""
				if p.IsDefault != nil && *p.IsDefault {
					def = "yes"
				}
				t.rows = append(t.rows, []string{p.Id, s(p.Name), def, s(p.Description)})
				t.ids = append(t.ids, p.Id)
			}
			return a.print(t)
		},
	}, &cobra.Command{
		Use:   "get PROJECT",
		Short: "Show a project (by ID or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.findProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			def := "no"
			if p.IsDefault != nil && *p.IsDefault {
				def = "yes"
			}
			return a.printObject(p.Id, [][2]string{
				{"ID", p.Id}, {"Name", s(p.Name)}, {"Default", def}, {"Description", orDash(s(p.Description))}, {"Created", s(p.CreatedAt)},
			}, p)
		},
	})
	return cmd
}

func (a *app) findProject(ctx context.Context, ref string) (cloudxino.Project, error) {
	c, err := a.api()
	if err != nil {
		return cloudxino.Project{}, err
	}
	ps, err := c.Projects.List(ctx)
	if err != nil {
		return cloudxino.Project{}, err
	}
	return pick("project", ref, ps, func(p cloudxino.Project) string { return p.Id }, func(p cloudxino.Project) string { return s(p.Name) })
}
