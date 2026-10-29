// Package routepolicy is where a route says who may call it.
//
// Who may call what used to be three hand-maintained lists of path prefixes in
// the middleware — isPublicPath, requiredScope and requiresNamespaceOwnership —
// with nothing connecting any of them to the routes they described. A route
// could match none of them, or two that disagreed, and the only symptom was an
// endpoint answering the wrong thing to the wrong caller.
//
// That had already happened twice. /v1/node/enroll was exempted from the scope
// check because its handler validates a Bearer invite token, and never added to
// the public list — so the API-key middleware refused the invite token as a bad
// API key and enrolling a node could not work at all. /v1/operator/* matched
// none of the lists, so a key out of a public app bundle could mint a cluster
// invite.
//
// Here a policy is declared once, in a table, and the middleware reads the
// policy of the route the request actually matched — never the path string. A
// route with no declared policy cannot be registered.
package routepolicy

import (
	"net/http"
	"sort"
	"sync"
)

// Access says what kind of credential a route needs before anything else is
// checked.
type Access int

const (
	// Credential means the middleware must resolve an API key or a JWT. It is
	// the zero value, so a route nobody has decided about is not open.
	Credential Access = iota

	// Open means the route is deliberately reachable by anyone: health,
	// version, the key material a client needs to verify a token, the login
	// handshake.
	Open

	// HandlerAuth means the handler authenticates the caller itself — an
	// invite token, the cluster secret, a signed internal header. The
	// middleware must not try to resolve the credential first, or it refuses
	// the caller before the handler that understands it ever runs.
	HandlerAuth
)

// Anonymous reports whether the middleware lets the request through without a
// credential of its own. Open and HandlerAuth differ in why, which is worth
// recording, but not in what the middleware does.
func (a Access) Anonymous() bool { return a == Open || a == HandlerAuth }

func (a Access) String() string {
	switch a {
	case Open:
		return "open"
	case HandlerAuth:
		return "handler-auth"
	default:
		return "credential"
	}
}

// TokenRequirement says what kind of token a caller must present beyond holding
// the grant.
type TokenRequirement int

const (
	// AnyCredential: a bare API key is enough.
	AnyCredential TokenRequirement = iota

	// AnyToken: a JWT of some kind, so that possession of the key has been
	// proven by an exchange. A userless server-side job satisfies this; a
	// leaked key on its own does not.
	AnyToken

	// WalletToken: a genuine logged-in user. An app or a key never qualifies:
	// creating and listing namespaces is a person's act.
	WalletToken

	// PrincipalToken: a logged-in user or a deployed app's own workload token,
	// never a key or a key's exchange. This is what makes an extracted runtime
	// key worthless on the data plane, while an app its owner granted the
	// domain reaches it as itself (docs/whitepaper/technical-reference/vol1/14-authorization.md "A workload's identity").
	PrincipalToken
)

// Policy is what a route requires of its caller.
type Policy struct {
	// Access is the credential the middleware itself insists on.
	Access Access

	// Domain and Action are what the route does: which part of the platform,
	// and what to it. Empty means any valid credential will do.
	//
	// They replace a single `Scope` word. A word could say "the control
	// plane" and nothing narrower, so 58 routes declared the same one and a
	// grant could not be narrowed to any of them without holding all of them.
	// These are strings rather than the auth package's types because a policy
	// table must not depend on the thing it is describing.
	Domain string
	Action string

	// Operator makes the route the cluster operator's: the caller must be
	// signed in with a wallet on the cluster's operator list, which the scope
	// gate reads before the handler runs. It is not a permission, and no
	// permission satisfies it. A namespace's roles, grants and keys are a
	// tenant's authority: `*:*:*` held in a namespace is everything in that
	// namespace and nothing of the cluster. An API key, or a token exchanged
	// for one, never qualifies, whoever owns its namespace. Domain and Action
	// stay empty on such a route.
	Operator bool

	// Ownership requires the caller to hold a live grant in the namespace. It
	// is also what resolves that grant onto the request, which the data paths
	// read for its resource selector.
	Ownership bool

	// Token is the kind of token required on top of the grant. Admin callers
	// are exempt; see the scope middleware.
	Token TokenRequirement

	// NarrowedByGrant is for an Open route whose handler applies the caller's
	// resource selector (`fn:name=checkout`). Open skips the authorization and
	// scope gates, so without it no grant is resolved and a selector is never
	// read. A caller presenting no credential is unaffected.
	NarrowedByGrant bool

	// MainGateway keeps the route on the gateway that serves the cluster
	// registry rather than proxying it to a namespace gateway. API keys live
	// only in that registry, so a namespace gateway cannot answer for them.
	MainGateway bool

	// PendingDevice is what the route does for a pending device's restricted
	// session (the function device policy). The zero value refuses it, so a
	// route nobody decided about is closed to pending devices — whether or not
	// it is Open to everyone else: a pending session reaches what the namespace
	// opened to it and nothing more.
	PendingDevice PendingDeviceAccess

	// RequestLog is how much of a request the gateway records about itself. The
	// zero value is everything; the routes of a relayed fetch (bugboard #266)
	// declare less, since their whole point is that no node keeps a record
	// pairing an address, a size or a duration with a CID or a destination.
	RequestLog RequestLogLevel
}

// PendingDeviceAccess is what a route does for a pending device's session.
type PendingDeviceAccess int

const (
	// PendingRefused refuses a pending device's session with
	// DEVICE_PENDING_APPROVAL. It is the zero value.
	PendingRefused PendingDeviceAccess = iota
	// PendingOwnSession admits it to a route that acts on that session or its
	// device alone: refreshing or ending it, reading who it is, attesting the
	// device. The handler must not act on anything else for it.
	PendingOwnSession
	// PendingFunctionInvoke admits it to a function's invoke or WebSocket
	// route, where the invoker runs only a function deployed open to pending
	// devices for it.
	PendingFunctionInvoke
)

func (a PendingDeviceAccess) String() string {
	switch a {
	case PendingOwnSession:
		return "own-session"
	case PendingFunctionInvoke:
		return "function-invoke"
	default:
		return "refused"
	}
}

// RequestLogLevel is what a route leaves in the request log and the access log.
type RequestLogLevel int

const (
	// LogFull records the request: method, path, status, size, duration and the
	// caller's address.
	LogFull RequestLogLevel = iota
	// LogNoAddress records the request without the caller's address:
	// request_logs.ip is written empty.
	LogNoAddress
	// LogNone records nothing per request: no request_logs row and no access-log
	// line, not even a size or a duration. The request is only counted, by status,
	// in the request metrics.
	LogNone
)

// Table is every route's policy.
type Table struct {
	static  map[string]Policy
	dynamic map[string]func(*http.Request) Policy

	once    sync.Once
	matcher *http.ServeMux
}

// NewTable returns an empty table.
func NewTable() *Table {
	return &Table{
		static:  map[string]Policy{},
		dynamic: map[string]func(*http.Request) Policy{},
	}
}

// Add declares one policy for a set of patterns.
func (t *Table) Add(policy Policy, patterns ...string) *Table {
	for _, pattern := range patterns {
		t.declare(pattern)
		t.static[pattern] = policy
	}
	return t
}

// AddDynamic declares a policy that depends on the request.
//
// It is for the routes that are one registered pattern serving several
// operations, where the handler dispatches on the rest of the path itself: the
// policy has to dispatch the same way, and it belongs next to the handler that
// does so rather than in a list somewhere else.
func (t *Table) AddDynamic(pattern string, resolve func(*http.Request) Policy) *Table {
	t.declare(pattern)
	t.dynamic[pattern] = resolve
	return t
}

func (t *Table) declare(pattern string) {
	if _, dup := t.static[pattern]; dup {
		panic("routepolicy: " + pattern + " is declared twice")
	}
	if _, dup := t.dynamic[pattern]; dup {
		panic("routepolicy: " + pattern + " is declared twice")
	}
}

// Declared reports whether a pattern has a policy.
func (t *Table) Declared(pattern string) bool {
	if _, ok := t.static[pattern]; ok {
		return true
	}
	_, ok := t.dynamic[pattern]
	return ok
}

// Patterns returns every declared pattern, sorted.
func (t *Table) Patterns() []string {
	out := make([]string, 0, len(t.static)+len(t.dynamic))
	for pattern := range t.static {
		out = append(out, pattern)
	}
	for pattern := range t.dynamic {
		out = append(out, pattern)
	}
	sort.Strings(out)
	return out
}

// Static returns the policy declared for a pattern with Add. It reports false
// for a pattern declared with AddDynamic, whose policy depends on the request,
// and for one that is not declared.
func (t *Table) Static(pattern string) (Policy, bool) {
	p, ok := t.static[pattern]
	return p, ok
}

// For returns the policy of the route this request matches.
//
// A request that matches nothing gets the zero policy: a credential is
// required, no grant is enough to reach anything, and the mux answers 404. That
// is the fail-closed direction — an unmatched path is not an open one.
func (t *Table) For(r *http.Request) Policy {
	pattern := t.pattern(r)
	if pattern == "" {
		return Policy{}
	}
	if resolve, ok := t.dynamic[pattern]; ok {
		return resolve(r)
	}
	return t.static[pattern]
}

// pattern is the declared pattern this request matches, or "".
//
// Matching is delegated to a ServeMux holding every declared pattern, so the
// policy is selected by exactly the rules that select the handler. A path the
// mux would only reach by redirecting — one needing cleaning, or a case that
// does not match — resolves to nothing and is refused.
func (t *Table) pattern(r *http.Request) string {
	t.once.Do(func() {
		t.matcher = http.NewServeMux()
		for _, pattern := range t.Patterns() {
			t.matcher.Handle(pattern, http.NotFoundHandler())
		}
	})
	_, pattern := t.matcher.Handler(r)
	return pattern
}

// Mux registers handlers for the routes a table declares.
//
// A pattern the table does not declare panics rather than being served: a route
// whose policy nobody decided would otherwise fall through to the zero policy
// and be reachable by any credential at all. TestRoutePolicy_everyRegisteredRouteIsDeclared
// fails on it long before this can.
type Mux struct {
	table *Table
	mux   *http.ServeMux
}

// NewMux returns a mux that enforces the table.
func NewMux(table *Table) *Mux {
	return &Mux{table: table, mux: http.NewServeMux()}
}

// HandleFunc registers a handler for a declared pattern.
func (m *Mux) HandleFunc(pattern string, handler http.HandlerFunc) {
	m.Handle(pattern, handler)
}

// Handle registers a handler for a declared pattern.
func (m *Mux) Handle(pattern string, handler http.Handler) {
	m.require(pattern)
	m.mux.Handle(pattern, handler)
}

// RegisterAll lets a package that owns a family of routes put its own handlers
// on the mux. Every pattern in the family is checked against the table first,
// so the family cannot be wired without a policy.
func (m *Mux) RegisterAll(patterns []string, register func(*http.ServeMux)) {
	for _, pattern := range patterns {
		m.require(pattern)
	}
	register(m.mux)
}

func (m *Mux) require(pattern string) {
	if !m.table.Declared(pattern) {
		panic("routepolicy: " + pattern + " is registered with no policy; declare who may call it")
	}
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.mux.ServeHTTP(w, r) }
