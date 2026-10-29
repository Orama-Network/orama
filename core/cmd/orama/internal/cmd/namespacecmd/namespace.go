package namespacecmd

import (
	"strings"

	cli "github.com/Orama-Network/orama/core/cmd/orama/internal"
	"github.com/Orama-Network/orama/core/cmd/orama/internal/printer"
	"github.com/Orama-Network/orama/core/pkg/gateway/auth"
	"github.com/spf13/cobra"
)

// Cmd is the root command for namespace management.
var Cmd = &cobra.Command{
	Use:     "namespace",
	Aliases: []string{"ns"},
	Short:   "Manage namespaces",
	Long:    `List, delete, and repair namespaces on the Orama network.`,
}

var deleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete the current namespace and all its resources",
	RunE: func(cmd *cobra.Command, args []string) error {
		forceFlag, _ := cmd.Flags().GetBool("force")
		return cli.NamespaceDelete(forceFlag)
	},
}

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List namespaces owned by the current wallet",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cli.NamespaceList(printer.For(cmd))
	},
}

var repairCmd = &cobra.Command{
	Use:   "repair <namespace>",
	Short: "Repair an under-provisioned namespace cluster",
	Long: "Repair an under-provisioned namespace cluster. Run it on a node. " +
		"It talks to that node's gateway on the node's WireGuard address; " +
		"localhost is where public traffic arrives, so a repair sent there is refused.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cli.NamespaceRepair(args[0])
	},
}

var enableCmd = &cobra.Command{
	Use:   "enable <feature>",
	Short: "Enable a feature for a namespace",
	Long:  "Enable a feature for a namespace. Supported features: webrtc, webrtc-stealth",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceEnable(args[0], ns)
	},
}

var disableCmd = &cobra.Command{
	Use:   "disable <feature>",
	Short: "Disable a feature for a namespace",
	Long:  "Disable a feature for a namespace. Supported features: webrtc, webrtc-stealth",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceDisable(args[0], ns)
	},
}

var webrtcStatusCmd = &cobra.Command{
	Use:   "webrtc-status",
	Short: "Show WebRTC service status for a namespace",
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceWebRTCStatus(ns)
	},
}

var keysCmd = &cobra.Command{
	Use:   "keys",
	Short: "Manage scoped API keys (bugboard #148)",
	Long: `Create, list, rotate and revoke a namespace's scoped API keys. Profiles:
invoke-only | app-runtime | admin.

Who may run these: the namespace's owner or an admin member, signed in with
'orama auth login --namespace <ns>' (RootWallet signs the challenge), or an
admin-scope key in ORAMA_TOKEN. A developer, runtime or reader member, and a key
holding anything less than admin, is refused with INSUFFICIENT_SCOPE: managing
keys is handing out authority. 'orama auth whoami' shows the role a credential
holds. The commands act on the namespace the credential belongs to; --namespace
picks which signed-in session to use.`,
}

var keysCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Mint a new scoped API key",
	Long: `Mint a new scoped API key. The key is shown once.

Which key goes where:
  - An app that needs nothing before sign-in ships no key: the sign-in
    handshake and public functions need no credential, and after sign-in the
    user's session reaches everything else.
  - An app that must call functions before sign-in ships an invoke-only key,
    narrowed with --resource to those functions.
  - A function that reclaims storage gets its own key, --scope storage, kept
    in the namespace's secrets. No key that ships in an app holds storage.
  - A backend that sends push notifications over HTTP gets its own key,
    --scope push-send, kept on the server. 'push' registers devices; only
    push-send sends, to any user or topic of the namespace.

Profiles: invoke-only (invoke), app-runtime (invoke, push, webrtc, proxy;
never storage or push-send), admin (the control plane; CI and a developer's
machine only).

--resource narrows the key to the functions it names: the gateway refuses it on
every other function, on HTTP invoke and the function WebSocket alike, with the
403 a member narrowed by 'members add --resource' gets. Without it the key
reaches the whole of its scopes.`,
	Example: `  orama namespace keys create --namespace anchat --scope invoke-only --resource fn:premium-status
  orama namespace keys create --namespace anchat --scope storage --label prune-fn
  orama namespace keys create --namespace anchat --scope invoke-only --resource 'fn:premium-*'
  orama namespace keys create --namespace anchat --scope invoke-only \
    --resource fn:subscription-read --resource fn:user-find-by-wallet`,
	RunE: func(cmd *cobra.Command, args []string) error {
		scope, _ := cmd.Flags().GetString("scope")
		label, _ := cmd.Flags().GetString("label")
		ns, _ := cmd.Flags().GetString("namespace")
		days, _ := cmd.Flags().GetInt("expires-in-days")
		resources, _ := cmd.Flags().GetStringArray("resource")
		return cli.NamespaceKeysCreate(printer.For(cmd), ns, scope, label, resources, days)
	},
}

var keysListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List scoped API keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceKeysList(ns)
	},
}

var keysRevokeCmd = &cobra.Command{
	Use:   "revoke",
	Short: "Revoke a single API key by id",
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetInt("id")
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceKeysRevoke(ns, id)
	},
}

var sessionPolicyCmd = &cobra.Command{
	Use:   "session-policy",
	Short: "Show or set who may sign in to a namespace and what its sessions bind",
	Long: `With no flags, show the namespace's session policy. With flags, set them; a flag
left out keeps its value.

  --sign-in members   only wallets holding a grant sign in (the default)
  --sign-in open      a wallet holding none may sign in too, as an end user of
                      the application. It gets a session and no API key, is
                      never granted anything, and reaches only what a grantless
                      wallet reaches. Closing it again ends those sessions at
                      their next refresh. A namespace nobody owns stays closed.

  --device-policy     optional | required | approval | function: what an end
                      user's sign-in must bind. Requiring devices revokes the
                      sign-in keys end users already hold. function: an
                      account's first device is active, and each later one
                      gets a restricted session until one of the namespace's
                      functions approves it (approve_device).

  --end-user-topics   the pub/sub topics an end user may subscribe to, list and
                      see presence on, comma-separated patterns ('*' matches any
                      run of characters, '/' included). It replaces the list,
                      and '' clears it. The default is none: an end user reads
                      no topic until it is listed. Members holding a grant read
                      what their grant says. A function's socket that bridges
                      topics to its users is not affected.

  --per-ip-limits     on | off (the default): ceilings on what one client
                      network (an IP address, an IPv6 /64) does, whatever
                      wallets it presents. For a namespace with open sign-in,
                      where a wallet costs nothing. Off by default because many
                      people can share one address (an office, carrier NAT).
  --new-wallets-per-ip-hour
                      when on, how many wallets that never signed in to the
                      namespace one network may sign in per hour, as end users
                      (default 20). Past it: 429 SIGN_IN_RATE_LIMITED.
  --invokes-per-ip-minute
                      when on, how many authenticated function invokes one
                      network may make per minute on each gateway (default
                      1200). Past it: 429 RATE_LIMITED scope=per_ip_authenticated.

Changing any of them needs write access to the namespace.

  orama namespace session-policy --namespace myapp --sign-in open
  orama namespace session-policy --namespace myapp --end-user-topics 'presence/*,users/global'
  orama namespace session-policy --namespace myapp --per-ip-limits on --new-wallets-per-ip-hour 10`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, _ := cmd.Flags().GetString("namespace")
		var change cli.SessionPolicyChange
		change.SignIn, _ = cmd.Flags().GetString("sign-in")
		change.DevicePolicy, _ = cmd.Flags().GetString("device-policy")
		change.PerIPLimits, _ = cmd.Flags().GetString("per-ip-limits")
		if cmd.Flags().Changed("end-user-topics") {
			list, _ := cmd.Flags().GetStringSlice("end-user-topics")
			change.EndUserTopics = &list
		}
		if cmd.Flags().Changed("new-wallets-per-ip-hour") {
			n, _ := cmd.Flags().GetInt("new-wallets-per-ip-hour")
			change.NewWalletsPerIPHour = &n
		}
		if cmd.Flags().Changed("invokes-per-ip-minute") {
			n, _ := cmd.Flags().GetInt("invokes-per-ip-minute")
			change.InvokesPerIPMinute = &n
		}
		return cli.NamespaceSessionPolicy(printer.For(cmd), ns, change)
	},
}

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a namespace and start its cluster",
	Long: `Create a namespace. The wallet you are signed in as becomes its owner.

Creating a namespace used to happen by itself: signing in to a name that did
not exist created it. So a typo made a namespace, and one belonged to whoever
happened to sign in first.

  orama namespace create myapp
  orama auth login --namespace myapp`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return cli.NamespaceCreate(args[0])
	},
}

var keysRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Mint a successor to a key and keep the old one working for an overlap",
	Long: `Mint a new key with the same grants, resources and label, and shorten the
original's life to the overlap.

Rotating by minting a new key and revoking the old one in the same breath is an
outage: whatever is deployed with the old key stops the moment the new one
exists. The overlap is the window in which to deploy the successor — both keys
work, and the original then expires on its own.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		id, _ := cmd.Flags().GetInt("id")
		overlap, _ := cmd.Flags().GetInt("overlap-days")
		expires, _ := cmd.Flags().GetInt("expires-in-days")
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceKeysRotate(ns, id, overlap, expires)
	},
}

var keysRevokeLegacyCmd = &cobra.Command{
	Use:   "revoke-legacy",
	Short: "Revoke ALL legacy (unscoped) keys — the cutover step",
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		ns, _ := cmd.Flags().GetString("namespace")
		return cli.NamespaceKeysRevokeLegacy(ns, force)
	},
}

func init() {
	deleteCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	enableCmd.Flags().String("namespace", "", "Namespace name")
	disableCmd.Flags().String("namespace", "", "Namespace name")
	webrtcStatusCmd.Flags().String("namespace", "", "Namespace name")
	sessionPolicyCmd.Flags().String("namespace", "", "Namespace name")
	sessionPolicyCmd.Flags().String("sign-in", "", "Who may sign in: members | open")
	sessionPolicyCmd.Flags().String("device-policy", "", "What an end user's sign-in must bind: optional | required | approval | function")
	sessionPolicyCmd.Flags().StringSlice("end-user-topics", nil, "Pub/sub topic patterns end users may read, comma-separated; '' clears the list")
	sessionPolicyCmd.Flags().String("per-ip-limits", "", "Per-client-network ceilings: on | off")
	sessionPolicyCmd.Flags().Int("new-wallets-per-ip-hour", 0, "When per-IP limits are on: new wallets one network may sign in per hour")
	sessionPolicyCmd.Flags().Int("invokes-per-ip-minute", 0, "When per-IP limits are on: authenticated invokes one network may make per minute per gateway")

	// The grant list comes from the gateway rather than a hand-written string,
	// which is how the help came to advertise grants the validator refused.
	keysCreateCmd.Flags().String("scope", "",
		"Profile (invoke-only|app-runtime|admin) or a comma-separated grant list ("+
			strings.Join(auth.AllGrants(), ", ")+")")
	keysCreateCmd.Flags().String("label", "", "Human label for the key")
	keysCreateCmd.Flags().StringArray("resource", nil,
		"Narrow the key to what this selector matches, e.g. fn:premium-status or 'fn:premium-*'. "+
			"Repeat for several; the key then reaches only these. Each must narrow one of its scopes "+
			"(fn: needs invoke), and every scope must be narrowed by one")
	keysCreateCmd.Flags().String("namespace", "", "Namespace name")
	keysListCmd.Flags().String("namespace", "", "Namespace name")
	keysRevokeCmd.Flags().Int("id", 0, "Key id to revoke")
	keysRevokeCmd.Flags().String("namespace", "", "Namespace name")
	keysRevokeLegacyCmd.Flags().Bool("force", false, "Skip confirmation prompt")
	keysRevokeLegacyCmd.Flags().String("namespace", "", "Namespace name")
	keysCreateCmd.Flags().Int("expires-in-days", 0,
		"How long the key lives, in days (default 90, max 365). A key that never expires is not on offer")
	keysRotateCmd.Flags().Int("id", 0, "Key id to rotate")
	keysRotateCmd.Flags().Int("overlap-days", 0,
		"How long the old key keeps working (default 7, max 30) — the window to deploy the new one")
	keysRotateCmd.Flags().Int("expires-in-days", 0, "How long the successor lives, in days (default 90)")
	keysRotateCmd.Flags().String("namespace", "", "Namespace name")

	keysCmd.AddCommand(keysCreateCmd)
	keysCmd.AddCommand(keysRotateCmd)
	keysCmd.AddCommand(keysListCmd)
	keysCmd.AddCommand(keysRevokeCmd)
	keysCmd.AddCommand(keysRevokeLegacyCmd)

	Cmd.AddCommand(createCmd)
	Cmd.AddCommand(listCmd)
	Cmd.AddCommand(deleteCmd)
	Cmd.AddCommand(repairCmd)
	Cmd.AddCommand(enableCmd)
	Cmd.AddCommand(disableCmd)
	Cmd.AddCommand(webrtcStatusCmd)
	Cmd.AddCommand(sessionPolicyCmd)
	Cmd.AddCommand(keysCmd)
	Cmd.AddCommand(backupSealCmd)
	Cmd.AddCommand(backupOpenCmd)
}
