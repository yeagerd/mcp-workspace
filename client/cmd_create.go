package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

// metaValues implements flag.Value for repeated --meta key=value flags.
type metaValues map[string]string

func (m metaValues) String() string { return "" }
func (m metaValues) Set(val string) error {
	idx := strings.IndexByte(val, '=')
	if idx < 0 {
		return fmt.Errorf("meta must be in key=value format")
	}
	m[val[:idx]] = val[idx+1:]
	return nil
}

func cmdCreate(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client create", flag.ContinueOnError)
	var branch string
	var baseBranch string
	var createBranch bool
	var prompt string
	meta := make(metaValues)
	fs.StringVar(&branch, "branch", "", "git branch to create or check out (defaults to name)")
	fs.StringVar(&baseBranch, "base-branch", "", "branch or commit to branch from when creating a new branch (defaults to HEAD)")
	fs.BoolVar(&createBranch, "create-branch", true, "create a new branch; set false to check out an existing one")
	fs.StringVar(&prompt, "prompt", "", "first prompt to send to Claude after startup (waits up to 30s for idle)")
	fs.Var(meta, "meta", "key=value metadata (repeatable: --meta k=v --meta k2=v2)")

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
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client create: %v\n", err)
		return err
	}
	defer cleanup()

	toolArgs := map[string]any{"name": name}
	if branch != "" {
		toolArgs["branch"] = branch
	}
	if baseBranch != "" {
		toolArgs["base_branch"] = baseBranch
	}
	if !createBranch {
		toolArgs["create_branch"] = false
	}
	if prompt != "" {
		toolArgs["prompt"] = prompt
	}
	if len(meta) > 0 {
		m := make(map[string]any, len(meta))
		for k, v := range meta {
			m[k] = v
		}
		toolArgs["meta"] = m
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
