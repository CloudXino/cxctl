package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudxino/cloudxino-go"
	"github.com/spf13/cobra"
)

func (a *app) sshKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ssh-key",
		Aliases: []string{"ssh-keys"},
		Short:   "Manage the team's saved SSH public keys",
	}
	cmd.AddCommand(a.sshKeyListCmd(), a.sshKeyGetCmd(), a.sshKeyAddCmd(), a.sshKeyRenameCmd(), a.sshKeyDeleteCmd())
	return cmd
}

func (a *app) sshKeys(ctx context.Context) ([]cloudxino.SSHKeyOut, error) {
	c, err := a.api()
	if err != nil {
		return nil, err
	}
	return c.SSHKeys.List(ctx, nil)
}

// findSSHKey takes an ID, a name or a fingerprint.
func (a *app) findSSHKey(ctx context.Context, ref string) (cloudxino.SSHKeyOut, error) {
	keys, err := a.sshKeys(ctx)
	if err != nil {
		return cloudxino.SSHKeyOut{}, err
	}
	return findKeyIn(keys, ref)
}

func findKeyIn(keys []cloudxino.SSHKeyOut, ref string) (cloudxino.SSHKeyOut, error) {
	for _, k := range keys {
		if k.Fingerprint == ref {
			return k, nil
		}
	}
	return pick("SSH key", ref, keys, func(k cloudxino.SSHKeyOut) string { return k.Id }, func(k cloudxino.SSHKeyOut) string { return k.Name })
}

func (a *app) sshKeyIDs(ctx context.Context, refs []string) ([]string, error) {
	keys, err := a.sshKeys(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		k, err := findKeyIn(keys, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, k.Id)
	}
	return ids, nil
}

func (a *app) printSSHKey(k *cloudxino.SSHKeyOut) error {
	created := ""
	if k.CreatedAt != nil {
		created = k.CreatedAt.Format("2006-01-02 15:04:05Z07:00")
	}
	return a.printObject(k.Id, [][2]string{
		{"ID", k.Id}, {"Name", k.Name}, {"Fingerprint", k.Fingerprint}, {"Created", created}, {"Public key", k.PublicKey},
	}, k)
}

func (a *app) sshKeyListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List saved SSH keys",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			keys, err := a.sshKeys(cmd.Context())
			if err != nil {
				return err
			}
			t := table{header: []string{"ID", "NAME", "FINGERPRINT"}, data: keys}
			for _, k := range keys {
				t.rows = append(t.rows, []string{k.Id, k.Name, k.Fingerprint})
				t.ids = append(t.ids, k.Id)
			}
			return a.print(t)
		},
	}
}

func (a *app) sshKeyGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY",
		Short: "Show a saved SSH key (by ID, name or fingerprint)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := a.findSSHKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printSSHKey(&k)
		},
	}
}

func (a *app) sshKeyAddCmd() *cobra.Command {
	var name, file string
	cmd := &cobra.Command{
		Use:     "add --name NAME --public-key-file FILE",
		Aliases: []string{"create", "import"},
		Short:   "Save a public key (never the private key)",
		Example: "  cxctl ssh-key add --name laptop --public-key-file ~/.ssh/id_ed25519.pub",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || file == "" {
				return usagef("--name and --public-key-file are required")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			c, err := a.api()
			if err != nil {
				return err
			}
			k, err := c.SSHKeys.Create(cmd.Context(), name, string(data))
			if err != nil {
				return err
			}
			return a.printSSHKey(k)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name for the key (required)")
	cmd.Flags().StringVar(&file, "public-key-file", "", "the .pub file (required)")
	return cmd
}

func (a *app) sshKeyRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename KEY NEW-NAME",
		Short: "Rename a saved SSH key",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := a.findSSHKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			c, _ := a.api()
			out, err := c.SSHKeys.Rename(cmd.Context(), k.Id, args[1])
			if err != nil {
				return err
			}
			return a.printSSHKey(out)
		},
	}
}

func (a *app) sshKeyDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete KEY",
		Aliases: []string{"rm"},
		Short:   "Delete a saved SSH key (servers that have it keep it)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := a.findSSHKey(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.confirm(yes, fmt.Sprintf("Deleting SSH key %s", k.Name)); err != nil {
				return err
			}
			c, _ := a.api()
			if err := c.SSHKeys.Delete(cmd.Context(), k.Id); err != nil {
				return err
			}
			a.done("Deleted SSH key %s (%s).", k.Name, k.Id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
