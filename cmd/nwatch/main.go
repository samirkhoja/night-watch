package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/samirkhoja/night-watch/internal/app"
	"github.com/samirkhoja/night-watch/internal/ui"
)

var cliVersion = "dev"

func main() {
	ctx := context.Background()
	opts, err := parseCLIOptions(os.Args[1:])
	if err != nil {
		ui.Error(os.Stderr, err.Error())
		os.Exit(1)
	}

	if opts.ShowVersion {
		fmt.Fprintf(os.Stdout, "nwatch %s\n", resolveCLIVersion())
		return
	}

	application, err := app.New(os.Stdin, os.Stdout, app.Options{
		ConfigPath:   opts.ConfigPath,
		MaxSteps:     opts.MaxSteps,
		Version:      resolveCLIVersion(),
		AutoApproval: opts.AutoApproval,
	})
	if err != nil {
		ui.Error(os.Stderr, err.Error())
		os.Exit(1)
	}

	args := opts.Args
	if len(args) == 0 {
		if err := application.RunChat(ctx, opts.Continue); err != nil {
			ui.Error(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}

	switch args[0] {
	case "setup":
		if err := application.RunSetup(ctx); err != nil {
			ui.Error(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "chat":
		if err := application.RunChat(ctx, opts.Continue); err != nil {
			ui.Error(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "ask":
		prompt := strings.TrimSpace(strings.Join(args[1:], " "))
		if prompt == "" {
			ui.Error(os.Stderr, "Usage: nwatch ask <prompt>")
			os.Exit(1)
		}
		if err := application.RunAsk(ctx, prompt, opts.Continue); err != nil {
			ui.Error(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "runbook":
		if err := runRunbookCommand(ctx, application, args[1:]); err != nil {
			ui.Error(os.Stderr, err.Error())
			os.Exit(1)
		}
	case "help", "--help", "-h":
		printHelp()
	default:
		// Treat unknown subcommand as a one-shot prompt.
		prompt := strings.TrimSpace(strings.Join(args, " "))
		if err := application.RunAsk(ctx, prompt, opts.Continue); err != nil {
			ui.Error(os.Stderr, err.Error())
			os.Exit(1)
		}
	}
}

func printHelp() {
	fmt.Print(`Night Watch

Usage:
  nwatch [--config <file>] [--max-steps <n>]                 Start interactive chat
  nwatch [--continue]                      Continue from a previous session
  nwatch [--config <file>] [--max-steps <n>] setup           Run setup flow
  nwatch [--config <file>] [--max-steps <n>] [--continue] chat            Start interactive chat
  nwatch [--config <file>] [--max-steps <n>] [--continue] ask <prompt>    Run one prompt and exit
  nwatch [--config <file>] [--max-steps <n>] runbook <command>            Manage installed runbooks
  nwatch [--auto-approval] ask <prompt>                 Run one prompt and skip approval prompts
  nwatch --version                                      Show CLI version
  nwatch [--config <file>] [--max-steps <n>] help            Show this help

Flags:
  -c, --config <file>  Optional custom settings JSON file (highest precedence)
      --max-steps <n>  Optional hard cap for parent-agent steps (omit for unlimited)
      --continue       Show recent sessions and load one into context
      --auto-approval  Skip command approval prompts for this process
      --version        Show CLI version
`)
}

type cliOptions struct {
	ConfigPath   string
	MaxSteps     int
	Continue     bool
	AutoApproval bool
	ShowVersion  bool
	Args         []string
}

func parseCLIOptions(args []string) (cliOptions, error) {
	var opts cliOptions
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		if arg == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if arg == "-c" || arg == "--config" {
			if i+1 >= len(args) {
				return cliOptions{}, errors.New("missing value for --config")
			}
			opts.ConfigPath = strings.TrimSpace(args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(arg, "--config=") {
			opts.ConfigPath = strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
			continue
		}
		if arg == "--max-steps" {
			if i+1 >= len(args) {
				return cliOptions{}, errors.New("missing value for --max-steps")
			}
			value := strings.TrimSpace(args[i+1])
			maxSteps, err := strconv.Atoi(value)
			if err != nil || maxSteps < 1 {
				return cliOptions{}, errors.New("--max-steps must be a positive integer")
			}
			opts.MaxSteps = maxSteps
			i++
			continue
		}
		if strings.HasPrefix(arg, "--max-steps=") {
			value := strings.TrimSpace(strings.TrimPrefix(arg, "--max-steps="))
			maxSteps, err := strconv.Atoi(value)
			if err != nil || maxSteps < 1 {
				return cliOptions{}, errors.New("--max-steps must be a positive integer")
			}
			opts.MaxSteps = maxSteps
			continue
		}
		if arg == "--continue" {
			opts.Continue = true
			continue
		}
		if arg == "--auto-approval" {
			opts.AutoApproval = true
			continue
		}
		if arg == "-v" || arg == "--version" {
			opts.ShowVersion = true
			continue
		}
		if arg == "-h" || arg == "--help" {
			rest = append(rest, args[i:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			return cliOptions{}, fmt.Errorf("unknown option: %s", arg)
		}

		rest = append(rest, args[i:]...)
		break
	}

	opts.Args = rest
	return opts, nil
}

func resolveCLIVersion() string {
	version := strings.TrimSpace(cliVersion)
	if version != "" && version != "dev" {
		return version
	}
	buildInfo, ok := debug.ReadBuildInfo()
	if ok {
		mainVersion := strings.TrimSpace(buildInfo.Main.Version)
		if mainVersion != "" && mainVersion != "(devel)" {
			return mainVersion
		}
	}
	if version == "" {
		return "dev"
	}
	return version
}

type runbookInstallOptions struct {
	Source string
	Name   string
	Ref    string
	Subdir string
	Force  bool
}

func runRunbookCommand(ctx context.Context, application *app.App, args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stdout, runbookHelpText())
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "install":
		opts, err := parseRunbookInstallOptions(args[1:])
		if err != nil {
			return err
		}
		return application.RunRunbookInstall(ctx, opts.Source, opts.Name, opts.Ref, opts.Subdir, opts.Force)
	case "list":
		return application.RunRunbookList(ctx)
	case "inspect":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New("usage: nwatch runbook inspect <id>")
		}
		return application.RunRunbookInspect(ctx, args[1])
	case "remove", "delete", "rm":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return errors.New("usage: nwatch runbook remove <id>")
		}
		return application.RunRunbookRemove(ctx, args[1])
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stdout, runbookHelpText())
		return nil
	default:
		return fmt.Errorf("unknown runbook command: %s\n\n%s", args[0], runbookHelpText())
	}
}

func parseRunbookInstallOptions(args []string) (runbookInstallOptions, error) {
	var opts runbookInstallOptions
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		switch {
		case arg == "--force":
			opts.Force = true
		case arg == "--name":
			if i+1 >= len(args) {
				return runbookInstallOptions{}, errors.New("missing value for --name")
			}
			opts.Name = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--name="):
			opts.Name = strings.TrimSpace(strings.TrimPrefix(arg, "--name="))
		case arg == "--ref":
			if i+1 >= len(args) {
				return runbookInstallOptions{}, errors.New("missing value for --ref")
			}
			opts.Ref = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--ref="):
			opts.Ref = strings.TrimSpace(strings.TrimPrefix(arg, "--ref="))
		case arg == "--subdir":
			if i+1 >= len(args) {
				return runbookInstallOptions{}, errors.New("missing value for --subdir")
			}
			opts.Subdir = strings.TrimSpace(args[i+1])
			i++
		case strings.HasPrefix(arg, "--subdir="):
			opts.Subdir = strings.TrimSpace(strings.TrimPrefix(arg, "--subdir="))
		case strings.HasPrefix(arg, "-"):
			return runbookInstallOptions{}, fmt.Errorf("unknown runbook install option: %s", arg)
		default:
			if opts.Source != "" {
				return runbookInstallOptions{}, fmt.Errorf("unexpected argument: %s", arg)
			}
			opts.Source = arg
		}
	}
	if strings.TrimSpace(opts.Source) == "" {
		return runbookInstallOptions{}, errors.New("usage: nwatch runbook install <source> [--name <name>] [--ref <git-ref>] [--subdir <path>] [--force]")
	}
	return opts, nil
}

func runbookHelpText() string {
	return strings.TrimSpace(`
Runbook commands:
  nwatch runbook install <source> [--name <name>] [--ref <git-ref>] [--subdir <path>] [--force]
  nwatch runbook list
  nwatch runbook inspect <id>
  nwatch runbook remove <id>

Sources:
  - local markdown file or directory
  - git URL (https://..., ssh://..., git@...)
`)
}
