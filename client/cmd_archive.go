package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdArchive(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client archive", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client archive: id is required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client archive: %v\n", err)
		return err
	}
	defer cleanup()

	raw, err := callTool(ctx, c, "workspace_archive", map[string]any{"id": id})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client archive: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var ws workspaceSummary
	if err := json.Unmarshal(raw, &ws); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client archive: parsing response: %v\n", err)
		return err
	}
	printWorkspace(ws, os.Stdout)
	return nil
}
