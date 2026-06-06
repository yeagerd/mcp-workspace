package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdDelete(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client delete", flag.ContinueOnError)
	var confirm bool
	fs.BoolVar(&confirm, "confirm", false, "must be set to actually delete")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client delete: id is required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)

	if !confirm {
		fmt.Fprintf(os.Stderr, "harness-client delete: refusing to delete workspace %q without --confirm\n", id)
		os.Exit(1)
	}

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client delete: %v\n", err)
		return err
	}
	defer cleanup()

	raw, err := callTool(ctx, c, "workspace_delete", map[string]any{
		"id":      id,
		"confirm": true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client delete: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client delete: parsing response: %v\n", err)
		return err
	}
	fmt.Printf("deleted workspace %s\n", id)
	return nil
}
