package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdCreate(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client create", flag.ContinueOnError)
	var branch string
	var repo string
	fs.StringVar(&branch, "branch", "", "git branch to create or check out (defaults to name)")
	fs.StringVar(&repo, "repo", "", "repo alias")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client create: name is required\n")
		fs.Usage()
		os.Exit(1)
	}
	name := fs.Arg(0)

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts.binaryPath, opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client create: %v\n", err)
		return err
	}
	defer cleanup()

	toolArgs := map[string]any{"name": name}
	if branch != "" {
		toolArgs["branch"] = branch
	}
	if repo != "" {
		toolArgs["repo"] = repo
	}

	raw, err := callTool(ctx, c, "workspace_create", toolArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client create: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var ws workspaceSummary
	if err := json.Unmarshal(raw, &ws); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client create: parsing response: %v\n", err)
		return err
	}
	printWorkspace(ws, os.Stdout)
	return nil
}
