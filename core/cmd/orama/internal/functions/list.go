package functions

import (
	"fmt"
	"strings"

	"github.com/Orama-Network/orama/core/cmd/orama/internal/printer"
	"github.com/spf13/cobra"
)

var listAll bool

// ListCmd lists all deployed functions.
var ListCmd = &cobra.Command{
	Use:   "list",
	Short: "List deployed functions",
	Long: `Lists the current version of every function deployed in the current namespace.

--all lists every version row the namespace holds instead, whatever its
status: active, inactive (disabled), superseded (replaced by a later deploy
or a rollback) and deleted. A row stays until it is pruned (past the newest
10 versions) or purged ('orama function delete --purge').`,
	Args: cobra.NoArgs,
	RunE: runList,
}

func init() {
	ListCmd.Flags().BoolVar(&listAll, "all", false, "List every version, including disabled, superseded and deleted ones")
}

func runList(cmd *cobra.Command, args []string) error {
	endpoint := "/v1/functions"
	if listAll {
		endpoint += "?all=true"
	}
	result, err := apiGet(endpoint)
	if err != nil {
		return err
	}

	out := printer.For(cmd)
	functions, _ := result["functions"].([]interface{})

	if len(functions) == 0 && !out.JSONMode() {
		out.Printf("No functions deployed.\n")
		return nil
	}
	if listAll {
		return printAllVersions(out, functions)
	}

	rows := make([][]string, 0, len(functions))
	for _, f := range functions {
		fn, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		publicStr := "no"
		if valBool(fn, "is_public") {
			publicStr = "yes"
		}
		rows = append(rows, []string{
			valStr(fn, "name"),
			fmt.Sprintf("%d", valNum(fn, "version")),
			valStr(fn, "status"),
			fmt.Sprintf("%dMB", valNum(fn, "memory_limit_mb")),
			fmt.Sprintf("%ds", valNum(fn, "timeout_seconds")),
			publicStr,
			invocableBy(fn),
		})
	}

	if err := out.Table([]string{"NAME", "VERSION", "STATUS", "MEMORY", "TIMEOUT", "PUBLIC", "INVOCABLE BY"}, rows); err != nil {
		return err
	}
	out.Printf("\nTotal: %d function(s)\n", len(functions))
	return nil
}

// printAllVersions prints every version row of `list --all`.
func printAllVersions(out *printer.Printer, functions []interface{}) error {
	rows := make([][]string, 0, len(functions))
	for _, f := range functions {
		fn, ok := f.(map[string]interface{})
		if !ok {
			continue
		}
		rows = append(rows, []string{
			valStr(fn, "name"),
			fmt.Sprintf("%d", valNum(fn, "version")),
			valStr(fn, "status"),
			valStr(fn, "wasm_cid"),
			valStr(fn, "updated_at"),
		})
	}
	if err := out.Table([]string{"NAME", "VERSION", "STATUS", "WASM CID", "UPDATED"}, rows); err != nil {
		return err
	}
	out.Printf("\nTotal: %d version(s)\n", len(functions))
	return nil
}

// invocableBy is the functions a function admits as its callers
// (invocable_by), comma-separated, or "-" when any caller may invoke it.
func invocableBy(fn map[string]interface{}) string {
	names, _ := fn["invocable_by"].([]interface{})
	if len(names) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%v", n))
	}
	return strings.Join(parts, ",")
}

func valStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// valStrOrDash is valStr for a field that may be null or absent, shown as "-".
func valStrOrDash(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return "-"
}

func valNum(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func valBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
