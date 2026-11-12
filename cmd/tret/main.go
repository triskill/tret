// Command tret is the single entry point.
//
//	tret server [flags]   worker daemon: deploys the agent_loop process,
//	                      serves decide/fail-report jobs, owns durable state
//	tret agent [flags]    workspace-facing CLI: start or attach to a run,
//	                      answer its agent-reply user tasks
//	tret [flags]          same as 'tret agent [flags]'
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintf(os.Stderr, `tret — an AI agent whose control loop is an executable BPMN process

Usage:
  tret server [flags]   run the worker daemon (deploys, decides, owns state)
  tret agent [flags]    chat CLI: start/attach to a run, answer its user tasks (default)
  tret [flags]          same as 'tret agent [flags]'

Run 'tret server -h' or 'tret agent -h' for subcommand flags.
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
