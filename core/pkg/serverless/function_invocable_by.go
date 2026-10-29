package serverless

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A function's parents: `invocable_by` in function.yaml (feat-971). A function
// that names them runs only when one of them calls it with function_invoke or
// function_invoke_async; a client is refused on every route that reaches it
// (HTTP invoke, /v1/invoke, its WebSocket), and so is any other function of
// the namespace but the function itself.
//
//	invocable_by: [rpc-router]
//
// It exists for an app that sends every call through one router function that
// checks each request (the device, the session, the app build) before it
// forwards: without it the functions behind the router are reachable directly
// by any session of the namespace, and a modified client skips the router.
// `internal: true` does not do this, because the router invokes as the caller
// and the caller is not an admin.
//
// The parent's call is still the caller's: the child sees the same wallet,
// subject, device and attestation as the parent, and the caller still needs
// whatever the child's own public/private rule asks. A gateway-started
// invocation (a trigger, the claims provider, and a chain they started) and an
// admin caller are not refused, as for an internal function.
//
// Parents are matched by name, not by version or row id: a redeploy or a
// rollback of the router keeps its access. A parent deleted after the child
// was deployed leaves a name no running function has, so nothing reaches the
// child but triggers and admins until a function of that name is deployed
// again or the child is redeployed without it.

// MaxInvocableBy bounds how many parents one function names. The check is a
// scan of the list on every invocation of the function; a router-style app
// names one or two.
const MaxInvocableBy = 16

// fieldInvocableBy is the field a refused list is reported under, as
// function.yaml spells it.
const fieldInvocableBy = "invocable_by"

// functionNamePattern is what a function name is: the CLI refuses any other
// in function.yaml (core/cmd/orama/internal/functions/helpers.go), and a
// parent is a function name.
var functionNamePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// validateInvocableBy refuses a list that could never be what its author
// meant: a name that is no function name, the function itself, a repeat, too
// many names, or a function whose clients reach it on purpose.
func validateInvocableBy(def *FunctionDefinition) error {
	parents := def.InvocableBy
	if len(parents) == 0 {
		return nil
	}
	if len(parents) > MaxInvocableBy {
		return &ValidationError{Field: fieldInvocableBy,
			Message: fmt.Sprintf("names at most %d functions, got %d", MaxInvocableBy, len(parents))}
	}
	seen := make(map[string]struct{}, len(parents))
	for _, p := range parents {
		if !functionNamePattern.MatchString(p) {
			return &ValidationError{Field: fieldInvocableBy, Message: fmt.Sprintf("%q is not a function name", p)}
		}
		if p == def.Name {
			return &ValidationError{Field: fieldInvocableBy,
				Message: fmt.Sprintf("%q names the function itself, whose calls to itself are always admitted", p)}
		}
		if _, dup := seen[p]; dup {
			return &ValidationError{Field: fieldInvocableBy, Message: fmt.Sprintf("%q is named twice", p)}
		}
		seen[p] = struct{}{}
	}
	switch {
	case def.IsInternal:
		return &ValidationError{Field: fieldInvocableBy,
			Message: "an internal function is already refused to every caller but an admin; invocable_by would add nothing"}
	case def.WSPersistent:
		return &ValidationError{Field: fieldInvocableBy,
			Message: "a ws_persistent function is opened by clients over its WebSocket, which invocable_by refuses"}
	case def.WSAuth == WSAuthCapability:
		return &ValidationError{Field: fieldInvocableBy,
			Message: "a ws_auth: capability function is opened by clients holding a capability, which invocable_by refuses"}
	}
	return nil
}

// parentName is one row of checkParentsExist's query.
type parentName struct {
	Name string `db:"name"`
}

// checkParentsExist refuses a list naming a function the namespace does not
// have (no active or disabled version): a misspelt parent would otherwise
// leave the function reachable by nothing. One query per deploy, never per
// invocation.
func (r *Registry) checkParentsExist(ctx context.Context, namespace string, parents []string) error {
	if len(parents) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(parents)), ", ")
	query := `SELECT DISTINCT name FROM functions WHERE namespace = ? AND status IN (?, ?) AND name IN (` + placeholders + `)`
	args := make([]interface{}, 0, len(parents)+3)
	args = append(args, namespace, string(FunctionStatusActive), string(FunctionStatusInactive))
	for _, p := range parents {
		args = append(args, p)
	}
	var rows []parentName
	if err := r.db.Query(ctx, &rows, query, args...); err != nil {
		return fmt.Errorf("failed to look up the functions invocable_by names: %w", err)
	}
	found := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		found[row.Name] = struct{}{}
	}
	for _, p := range parents {
		if _, ok := found[p]; !ok {
			return &ValidationError{Field: fieldInvocableBy,
				Message: fmt.Sprintf("%q is not a function of namespace %q; deploy it first", p, namespace)}
		}
	}
	return nil
}

// authorizeParent refuses an invocation of a function that declares
// invocable_by unless a parent it names made it, the function itself made it,
// the gateway started it, or the caller is an admin. A call the function makes
// to itself is admitted because the invocation making it was: nothing else
// can have started that one.
func authorizeParent(fn *Function, req *InvokeRequest) error {
	if len(fn.InvocableBy) == 0 || req.SystemOriginated || req.CallerIsAdmin ||
		req.ParentFunction == fn.Name || slices.Contains(fn.InvocableBy, req.ParentFunction) {
		return nil
	}
	if req.ParentFunction != "" {
		return fmt.Errorf("%w: %q may be invoked by %s, not by %q",
			ErrFunctionNotDirectlyInvocable, fn.Name, strings.Join(fn.InvocableBy, ", "), req.ParentFunction)
	}
	return RefuseClientInvocation(fn, req.CallerIsAdmin)
}

// RefuseClientInvocation is the refusal of a client that names fn directly,
// nil when fn admits one. For a caller outside this package that has fetched
// the function and never reaches Invoke before it commits to the call: the
// WebSocket upgrade, which refuses before it opens a socket every frame of
// which would be refused.
func RefuseClientInvocation(fn *Function, callerIsAdmin bool) error {
	if len(fn.InvocableBy) == 0 || callerIsAdmin {
		return nil
	}
	return fmt.Errorf("%w: %q runs only when %s calls it with function_invoke or function_invoke_async",
		ErrFunctionNotDirectlyInvocable, fn.Name, strings.Join(fn.InvocableBy, ", "))
}

// invocableBySeparator joins the names in the registry's column. A function
// name cannot contain it (functionNamePattern).
const invocableBySeparator = ","

// invocableByColumn is the list as the registry stores it: the names joined by
// invocableBySeparator, "" for none, which is what every function deployed
// before the column has.
func invocableByColumn(parents []string) string {
	return strings.Join(parents, invocableBySeparator)
}

// invocableByFromColumn is the list a function row holds, nil for none.
func invocableByFromColumn(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, invocableBySeparator)
}
