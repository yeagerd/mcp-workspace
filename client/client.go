package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const clientVersion = "0.1.0"

// Execute is the entry point for the harness-client binary.
func Execute(args []string) error {
	fs := flag.NewFlagSet("harness-client", flag.ContinueOnError)
	var configPath string
	var binaryPath string
	var jsonOut bool
	var showVersion bool

	fs.StringVar(&configPath, "config", "", "path to config JSON file passed to hangar")
	fs.StringVar(&binaryPath, "binary", "", "path to hangar binary (default: bin/hangar)")
	fs.BoolVar(&jsonOut, "json", false, "emit raw JSON output instead of human-readable text")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")

	fs.Usage = printUsage

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// printUsage already called by fs.Usage; exit 0.
			os.Exit(0)
		}
		return err
	}

	if showVersion {
		fmt.Printf("harness-client %s\n", clientVersion)
		return nil
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		printUsage()
		os.Exit(0)
	}

	subcommand := remaining[0]
	subArgs := remaining[1:]

	opts := globalOpts{
		configPath: configPath,
		binaryPath: binaryPath,
		jsonOut:    jsonOut,
	}

	// Load .mcp.json from the working directory to pick up server binary,
	// args, and env (e.g. HANGAR_REPO_PATH) without requiring explicit flags.
	if cwd, err := os.Getwd(); err == nil {
		if entry := findProjectMCPConfig(cwd); entry != nil {
			if opts.binaryPath == "" && entry.Command != "" {
				opts.binaryPath = entry.Command
			}
			if opts.configPath == "" {
				opts.serverArgs = entry.Args
			}
			opts.serverEnv = entry.Env
		}
	}

	switch subcommand {
	case "list":
		return cmdList(opts, subArgs)
	case "create":
		return cmdCreate(opts, subArgs)
	case "delete":
		return cmdDelete(opts, subArgs)
	case "send":
		return cmdSend(opts, subArgs)
	case "read":
		return cmdRead(opts, subArgs)
	case "idle":
		return cmdIdle(opts, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "harness-client: unknown subcommand %q\n\n", subcommand)
		printUsage()
		os.Exit(1)
	}
	return nil
}

// globalOpts holds flags shared across all subcommands.
type globalOpts struct {
	configPath string
	binaryPath string
	jsonOut    bool
	serverArgs []string          // from .mcp.json
	serverEnv  map[string]string // from .mcp.json
}

func printUsage() {
	fmt.Print(`Usage: harness-client [--config <path>] [--binary <path>] [--json] [--version] <subcommand> [args]

Subcommands:
  list          List all workspaces
  create        Create a new workspace
  delete        Permanently delete a workspace
  send          Send text to a workspace session
  read          Read terminal output from a workspace
  idle          Check whether a workspace is idle

Flags:
  --config <path>   Path to config JSON file passed to hangar
  --binary <path>   Path to hangar binary (default: bin/hangar)
  --json            Emit raw JSON output instead of human-readable text
  --version         Print version and exit
`)
}
