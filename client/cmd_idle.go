package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdIdle(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client idle", flag.ContinueOnError)
	var thresholdMs int64
	fs.Int64Var(&thresholdMs, "threshold-ms", 0, "idle threshold override in milliseconds (0 = server default)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client idle: id is required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts.binaryPath, opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client idle: %v\n", err)
		return err
	}
	defer cleanup()

	toolArgs := map[string]any{"id": id}
	if thresholdMs > 0 {
		toolArgs["threshold_ms"] = thresholdMs
	}

	raw, err := callTool(ctx, c, "workspace_idle", toolArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client idle: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var result struct {
		Idle      bool  `json:"idle"`
		ElapsedMs int64 `json:"elapsed_ms"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client idle: parsing response: %v\n", err)
		return err
	}

	if result.Idle {
		fmt.Printf("idle (elapsed %d ms)\n", result.ElapsedMs)
		return nil
	}

	fmt.Printf("busy (elapsed %d ms)\n", result.ElapsedMs)
	os.Exit(2)
	return nil
}
