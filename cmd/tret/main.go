// Command tret starts a terminal chat backed by a BPMN agent loop, hosts
// generic workers, and deploys or starts other BPMN processes.
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintf(os.Stderr, `tret — BPMN-driven agent loops with generic job workers

Usage:
  tret server [flags]   host generic workers (llm-call-v1, exec-command-v1)
  tret deploy [flags]   deploy a BPMN file and print its definition key
  tret start [flags]    start a process and print its instance key
  tret [flags]          chat in the current directory (engine must be running)
  tret chat [flags]     same as 'tret [flags]'

New loops are BPMN files deployed to the engine; add capabilities by
registering your own job workers (see docs/workers.md).
Run 'tret <command> -h' for subcommand flags.
`)
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 {
		switch args[0] {
		case "chat":
			runChat(args[1:])
			return
		case "server":
			runServer(args[1:])
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
	runChat(args)
}
