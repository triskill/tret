// Command tret hosts reusable BPMN workers and deploys/starts processes.
// The default agent command is the bundled interactive coding-agent example.
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintf(os.Stderr, `tret — a toolkit for BPMN-driven AI harnesses

Usage:
  tret server [flags]   host selected workers (optional bundled agent deployment)
  tret agent [flags]    chat CLI: start/attach to a run, answer its user tasks (default)
  tret deploy [flags]   deploy a BPMN file and print its definition key
  tret start [flags]    start a process and print its instance key
  tret [flags]          same as 'tret agent [flags]'

Run 'tret <command> -h' for subcommand flags.
`)
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "server":
			runServer(args[1:])
			return
		case "agent":
			runAgent(args[1:])
			return
		case "deploy":
			runDeploy(args[1:])
			return
		case "start":
			runStart(args[1:])
			return
		case "-h", "--help", "help":
			usage()
			return
		}
		if args[0][0] != '-' {
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
			usage()
			os.Exit(2)
		}
	}
	runAgent(args)
}
