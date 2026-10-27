package functions

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	logsLimit    int
	logsWASMOnly bool
)

// LogsCmd retrieves function invocation history.
//
// Default view: invocation history (always populated when the function has
// been invoked) — request_id, status, duration, error_message, plus any
// WASM-emitted log entries nested per record.
//
// --wasm-only switches to the legacy view that returns ONLY entries
// emitted by the function via log_info / log_error (often empty).
var LogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "Get invocation history for a function",
	Long: `Retrieves the most recent invocations for a deployed function.

Each invocation record shows: timestamp, request_id, status, duration_ms,
and (if any) the error message. A second line says where the duration went:
the node whose gateway ran it, the wait for an execution slot (queue), loading
the module (load: 0 when it was in memory) and the database calls with their
summed time (db). WASM functions that emit log entries via log_info /
log_error have those entries nested under each record.

Pass --wasm-only to retrieve only the WASM-emitted log lines (legacy
behavior; rarely useful on functions that don't call log_info).`,
	Args: functionNameArgs(1),
	RunE: runLogs,
}

func init() {
	LogsCmd.Flags().IntVar(&logsLimit, "limit", 50, "Maximum number of records to retrieve")
	LogsCmd.Flags().BoolVar(&logsWASMOnly, "wasm-only", false,
		"Show only WASM-emitted log entries (legacy view)")
}

func runLogs(cmd *cobra.Command, args []string) error {
	name := args[0]

	endpoint := "/v1/functions/" + name + "/logs"
	q := []string{}
	if logsLimit > 0 {
		q = append(q, "limit="+strconv.Itoa(logsLimit))
	}
	if logsWASMOnly {
		q = append(q, "wasm_only=1")
	}
	if len(q) > 0 {
		endpoint += "?" + strings.Join(q, "&")
	}

	result, err := apiGet(endpoint)
	if err != nil {
		return err
	}

	if logsWASMOnly {
		return printWASMLogs(name, result)
	}
	return printInvocations(name, result)
}

// printInvocations renders the default invocation-history view.
func printInvocations(name string, result map[string]interface{}) error {
	invs, ok := result["invocations"].([]interface{})
	if !ok || len(invs) == 0 {
		fmt.Printf("No invocations found for function %q.\n", name)
		return nil
	}

	for _, entry := range invs {
		inv, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		started := valStr(inv, "started_at")
		status := valStr(inv, "status")
		reqID := valStr(inv, "request_id")
		duration := valNumberAsString(inv, "duration_ms")
		errMsg := valStr(inv, "error_message")

		// Header line per invocation.
		fmt.Printf("[%s] %s  request=%s  duration=%sms\n",
			started, strings.ToUpper(status), reqID, duration)
		// Where the duration went (bug-963). A record written before the
		// gateway recorded it has no node.
		if node := valStr(inv, "node_id"); node != "" {
			fmt.Printf("  node=%s  queue=%sms  load=%sms  db=%s calls/%sms\n",
				node, valNumberAsString(inv, "queue_ms"), valNumberAsString(inv, "load_ms"),
				valNumberAsString(inv, "db_calls"), valNumberAsString(inv, "db_ms"))
		}
		if errMsg != "" {
			fmt.Printf("  error: %s\n", errMsg)
		}

		// Nested WASM logs (if any).
		if wasmLogs, ok := inv["wasm_logs"].([]interface{}); ok {
			for _, l := range wasmLogs {
				le, ok := l.(map[string]interface{})
				if !ok {
					continue
				}
				fmt.Printf("    %s [%s] %s\n",
					valStr(le, "timestamp"),
					strings.ToUpper(valStr(le, "level")),
					valStr(le, "message"))
			}
		}
	}

	fmt.Printf("\nShowing %d invocation(s). Use --wasm-only for the legacy log-line view.\n",
		len(invs))
	return nil
}

// printWASMLogs renders the legacy WASM-only view.
func printWASMLogs(name string, result map[string]interface{}) error {
	logs, ok := result["logs"].([]interface{})
	if !ok || len(logs) == 0 {
		fmt.Printf("No WASM-emitted logs found for function %q. "+
			"Tip: drop --wasm-only to see invocation history.\n", name)
		return nil
	}

	for _, entry := range logs {
		log, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		ts := valStr(log, "timestamp")
		level := valStr(log, "level")
		msg := valStr(log, "message")
		fmt.Printf("[%s] %s: %s\n", ts, level, msg)
	}

	fmt.Printf("\nShowing %d log(s)\n", len(logs))
	return nil
}

// valNumberAsString formats a JSON number field as a clean integer string.
func valNumberAsString(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return "0"
	}
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	default:
		return fmt.Sprintf("%v", n)
	}
}
