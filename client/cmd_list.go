package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdList(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client list", flag.ContinueOnError)
	var includeArchived bool
	var repo string
	fs.BoolVar(&includeArchived, "include-archived", false, "include archived and orphaned workspaces")
	fs.StringVar(&repo, "repo", "", "filter by repo alias")

	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client list: %v\n", err)
		return err
	}
	defer cleanup()

	toolArgs := map[string]any{}
	if includeArchived {
		toolArgs["include_archived"] = true
	}
	if repo != "" {
		toolArgs["repo"] = repo
	}

	raw, err := callTool(ctx, c, "workspace_list", toolArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client list: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var summaries []workspaceSummary
	if err := json.Unmarshal(raw, &summaries); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client list: parsing response: %v\n", err)
		return err
	}
	printTable(summaries, os.Stdout)
	return nil
}
