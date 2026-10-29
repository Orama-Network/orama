package memberscmd

import (
	"strings"

	"github.com/Orama-Network/orama/core/cmd/orama/internal"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/spf13/cobra"
)

// Cmd is the root command for namespace membership.
var Cmd = &cobra.Command{
	Use:   "members",
	Short: "Manage who may work in a namespace",
	Long: `List, add and remove the wallets that hold a grant in a namespace, and
transfer the namespace itself.

A namespace has exactly one owner. Everybody else holds a role:

  admin      the control plane — deployments, functions, secrets, members, keys,
             raw database; everything the owner may do except transfer, back up
             and restore the namespace or export its whole database
  developer  deployments, functions, databases, secrets, sending push, and the
             data plane; not members, keys or the namespace's settings
  runtime    the data plane — invoke, storage, push registration, webrtc, proxy,
             pubsub, cache; not sending push notifications
  reader     a member with no grant at all

Who may run these: list, add and remove need the owner or an admin, signed in
with 'orama auth login --namespace <ns>', or an admin-scope key in ORAMA_TOKEN.
Transfer needs the owner's own wallet session. Any other member is refused, so
'orama auth whoami' is how a member sees its own role.

Ownership is not a role you can hand out: use 'orama members transfer'.`,
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List who holds a grant in this namespace",
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.MembersList(ns)
	},
}

var addCmd = &cobra.Command{
	Use:   "add <wallet>",
	Short: "Give a wallet a role in this namespace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		role, _ := cmd.Flags().GetString("role")
		resource, _ := cmd.Flags().GetString("resource")
		name, _ := cmd.Flags().GetString("name")
		hours, _ := cmd.Flags().GetInt("expires-in-hours")
		return cli.MembersAdd(ns, args[0], role, resource, name, hours)
	},
}

var removeCmd = &cobra.Command{
	Use:     "remove <wallet>",
	Aliases: []string{"rm"},
	Short:   "Take a wallet's grant away",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.MembersRemove(ns, args[0])
	},
}

var transferCmd = &cobra.Command{
	Use:   "transfer <wallet>",
	Short: "Hand this namespace to another wallet",
	Long: `Make another wallet the owner of this namespace.

Only the current owner may do this, signed in with its own wallet (no key holds
the owner's role), and it is one step rather than a removal and a grant, so
there is no moment where the namespace has no owner.
You keep an admin grant, so handing a project over does not lock you out of it;
the new owner removes it with 'orama members remove' if they want you gone.
Keys minted before the transfer keep working until they expire or are revoked.
The new owner signs in with 'orama auth login --namespace <ns>', and
'orama namespace list' then shows the namespace as theirs.
<wallet> must be a wallet address (0x and 40 hex digits, or a Solana public key);
the gateway refuses anything else rather than hand the namespace to nobody.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		force, _ := cmd.Flags().GetBool("force")
		return cli.MembersTransfer(ns, args[0], force)
	},
}

func init() {
	// The role list comes from the gateway rather than a hand-written string,
	// so the help cannot advertise a role the validator refuses.
	addCmd.Flags().String("role", "",
		"Role to grant ("+strings.Join(grantableRoles(), ", ")+")")
	addCmd.Flags().String("resource", "",
		"Narrow the role to a resource, e.g. storage:avatars/* (applied in the "+
			strings.Join(auth.EnforcedSelectorDomains(), ", ")+" domains; any other is refused)")
	addCmd.Flags().String("name", "", "Human label for this member")
	addCmd.Flags().Int("expires-in-hours", 0, "Expire the grant after this many hours (default: never)")

	for _, c := range []*cobra.Command{listCmd, addCmd, removeCmd, transferCmd} {
		c.Flags().String("namespace", "", "Namespace name")
	}
	transferCmd.Flags().Bool("force", false, "Skip confirmation prompt")

	Cmd.AddCommand(listCmd)
	Cmd.AddCommand(addCmd)
	Cmd.AddCommand(removeCmd)
	Cmd.AddCommand(transferCmd)
}

// grantableRoles is every role except owner, which is transferred rather than
// granted.
func grantableRoles() []string {
	out := make([]string, 0, 3)
	for _, role := range auth.AllRoles() {
		if role != string(auth.RoleOwner) {
			out = append(out, role)
		}
	}
	return out
}
