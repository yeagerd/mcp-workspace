package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdRead(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client read", flag.ContinueOnError)
	var lines int
	var waitIdle bool
	var timeoutMs int64
	var sinceLine int
	fs.IntVar(&lines, "lines", 0, "number of lines to capture (0 = server default of 200)")
	fs.BoolVar(&waitIdle, "wait-idle", true, "wait until the workspace is idle before returning")
	fs.Int64Var(&timeoutMs, "timeout-ms", 0, "max time to wait for idle in milliseconds (0 = server default of 1 hour)")
	fs.IntVar(&sinceLine, "since-line", 0, "return only lines at or after this offset from the start of the captured buffer")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client read: id is required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client read: %v\n", err)
		return err
	}
	defer cleanup()

	toolArgs := map[string]any{
		"id":        id,
		"wait_idle": waitIdle,
	}
	if lines > 0 {
		toolArgs["lines"] = lines
	}
	if timeoutMs > 0 {
		toolArgs["timeout_ms"] = timeoutMs
	}
	if sinceLine > 0 {
		toolArgs["since_line"] = sinceLine
	}

	raw, err := callTool(ctx, c, "workspace_read", toolArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client read: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client read: parsing response: %v\n", err)
		return err
	}
	fmt.Print(result.Content)
	return nil
}
