package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

func cmdSend(opts globalOpts, args []string) error {
	fs := flag.NewFlagSet("harness-client send", flag.ContinueOnError)
	var pressEnter bool
	fs.BoolVar(&pressEnter, "enter", true, "send Enter keystroke after text (use --enter=false to suppress)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "harness-client send: id and text are required\n")
		fs.Usage()
		os.Exit(1)
	}
	id := fs.Arg(0)
	text := strings.Join(fs.Args()[1:], " ")

	ctx := context.Background()
	c, cleanup, err := connect(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client send: %v\n", err)
		return err
	}
	defer cleanup()

	raw, err := callTool(ctx, c, "workspace_send", map[string]any{
		"id":          id,
		"text":        text,
		"press_enter": pressEnter,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness-client send: %v\n", err)
		return err
	}

	if opts.jsonOut {
		return prettyPrint(raw)
	}

	fmt.Printf("sent to %s\n", id)
	return nil
}
