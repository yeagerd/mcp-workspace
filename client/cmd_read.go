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
	fs.IntVar(&lines, "lines", 0, "number of lines to capture (0 = server default)")

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

	toolArgs := map[string]any{"id": id}
	if lines > 0 {
		toolArgs["lines"] = lines
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
