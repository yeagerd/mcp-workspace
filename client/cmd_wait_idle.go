package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdWaitIdle(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client wait-idle", flag.ContinueOnError)
	var timeoutMs int64
	var thresholdMs int64
	var pollIntervalMs int64
	fs.Int64Var(&timeoutMs, "timeout-ms", 0, "maximum wait time in milliseconds (0 = server default)")
	fs.Int64Var(&thresholdMs, "threshold-ms", 0, "idle threshold override in milliseconds (0 = server default)")
	fs.Int64Var(&pollIntervalMs, "poll-interval-ms", 0, "poll interval in milliseconds (0 = server default)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client wait-idle: id is required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client wait-idle: %v\n", err)
		return err
	}
	defer cleanup()

	toolArgs := map[string]any{"id": id}
	if timeoutMs > 0 {
		toolArgs["timeout_ms"] = timeoutMs
	}
	if thresholdMs > 0 {
		toolArgs["threshold_ms"] = thresholdMs
	}
	if pollIntervalMs > 0 {
		toolArgs["poll_interval_ms"] = pollIntervalMs
	}

	raw, err := callTool(ctx, c, "workspace_wait_idle", toolArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client wait-idle: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var result struct {
		Idle     bool `json:"idle"`
		TimedOut bool `json:"timed_out"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client wait-idle: parsing response: %v\n", err)
		return err
	}

	if result.Idle && !result.TimedOut {
		fmt.Println("idle")
		return nil
	}

	fmt.Println("timed out")
	os.Exit(2)
	return nil
}
