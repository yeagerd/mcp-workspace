package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func cmdAttachHint(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client attach-hint", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "harness-client attach-hint: id is required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client attach-hint: %v\n", err)
		return err
	}
	defer cleanup()

	raw, err := callTool(ctx, c, "workspace_attach_hint", map[string]any{"id": id})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client attach-hint: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	var result struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(os.Stderr, "harness-client attach-hint: parsing response: %v\n", err)
		return err
	}
	fmt.Println(result.Command)
	return nil
}
