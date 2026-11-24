package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/triskill/tret/internal/zen"
)

// The operator commands answer the questions an operator has about a
// deployment: what is running, what failed, and what the engine did with it.
// They read the engine and change nothing but the incidents and the jobs the
// operator names.

const defaultEngineURL = "http://localhost:8080"

// instanceStates are the states the engine knows an instance in.
var instanceStates = []string{"active", "completed", "failed", "terminated"}

func runStatus(args []string) {
	if err := executeStatus(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeStatus(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(output)
	engineURL := fs.String("engine", defaultEngineURL, "engine REST URL")
	state := fs.String("state", "active", "instance state: active, completed, failed, terminated or all")
	limit := fs.Int("limit", 0, "maximum number of instances to show (0: all)")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return fmt.Errorf("status takes no positional arguments")
	}
	if *limit < 0 {
		return fmt.Errorf("-limit must not be negative")
	}
	filter := *state
	if filter == "all" {
		filter = ""
	}
	if filter != "" && !slices.Contains(instanceStates, filter) {
		return fmt.Errorf("-state must be one of %s or all", strings.Join(instanceStates, ", "))
	}
	instances, err := zen.NewClient(*engineURL).ListInstances(filter)
	if err != nil {
		return err
	}
	if *limit > 0 && len(instances) > *limit {
		instances = instances[:*limit]
	}
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "KEY\tSTATE\tPROCESS\tINCIDENTS\tBUSINESS KEY")
	for _, instance := range instances {
		fmt.Fprintf(table, "%d\t%s\t%s\t%d\t%s\n", instance.Key, instance.State, instance.BPMNProcessID, instance.IncidentCount, instance.BusinessKey)
	}
	return table.Flush()
}

func runInstance(args []string) {
	if err := executeInstance(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeInstance(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("instance", flag.ContinueOnError)
	fs.SetOutput(output)
	engineURL := fs.String("engine", defaultEngineURL, "engine REST URL")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	key, err := oneKey(fs.Name(), positional)
	if err != nil {
		return err
	}
	instance, err := zen.NewClient(*engineURL).GetInstance(key)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "key: %d\nstate: %s\nprocess: %s\ndefinition: %d\nbusiness key: %s\nincidents: %d\nvariables:\n",
		instance.Key, instance.State, instance.BPMNProcessID, instance.ProcessDefinitionKey, instance.BusinessKey, instance.IncidentCount); err != nil {
		return err
	}
	variables, err := json.MarshalIndent(instance.Variables, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the variables of instance %d: %w", key, err)
	}
	_, err = fmt.Fprintln(output, string(variables))
	return err
}

func runJobs(args []string) {
	if err := executeJobs(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeJobs(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	fs.SetOutput(output)
	engineURL := fs.String("engine", defaultEngineURL, "engine REST URL")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	key, err := oneKey(fs.Name(), positional)
	if err != nil {
		return err
	}
	jobs, err := zen.NewClient(*engineURL).ListJobs(key)
	if err != nil {
		return err
	}
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "KEY\tTYPE\tSTATE\tELEMENT\tATTEMPTS\tRETRIES")
	for _, job := range jobs {
		fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%d\t%d\n", job.Key, job.Type, job.State, job.ElementID, job.Attempts, job.Retries)
	}
	return table.Flush()
}

func runIncidents(args []string) {
	if err := executeIncidents(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeIncidents(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("incidents", flag.ContinueOnError)
	fs.SetOutput(output)
	engineURL := fs.String("engine", defaultEngineURL, "engine REST URL")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	key, err := oneKey(fs.Name(), positional)
	if err != nil {
		return err
	}
	incidents, err := zen.NewClient(*engineURL).ListIncidents(key)
	if err != nil {
		return err
	}
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "KEY\tELEMENT\tJOB\tRESOLVED\tMESSAGE")
	for _, incident := range incidents {
		fmt.Fprintf(table, "%d\t%s\t%d\t%s\t%s\n", incident.Key, incident.ElementID, incident.JobKey, resolvedWord(incident.Resolved()), incident.Message)
	}
	return table.Flush()
}

func runResolve(args []string) {
	if err := executeResolve(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeResolve(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(output)
	engineURL := fs.String("engine", defaultEngineURL, "engine REST URL")
	retries := fs.Int("retries", 1, "how many times the failed step may run again")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	key, err := oneKey(fs.Name(), positional)
	if err != nil {
		return err
	}
	if *retries < 0 {
		return fmt.Errorf("-retries must not be negative")
	}
	if err := zen.NewClient(*engineURL).ResolveIncident(key, *retries); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "resolved incident %d\n", key)
	return err
}

func runComplete(args []string) {
	if err := executeComplete(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeComplete(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("complete", flag.ContinueOnError)
	fs.SetOutput(output)
	engineURL := fs.String("engine", defaultEngineURL, "engine REST URL")
	variablesFile := fs.String("variables", "", "JSON object file with the variables of the answer")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	key, err := oneKey(fs.Name(), positional)
	if err != nil {
		return err
	}
	variables := map[string]any{}
	if *variablesFile != "" {
		data, err := os.ReadFile(*variablesFile)
		if err != nil {
			return fmt.Errorf("read variables file: %w", err)
		}
		if err := json.Unmarshal(data, &variables); err != nil {
			return fmt.Errorf("parse variables file as JSON object: %w", err)
		}
		if variables == nil {
			return fmt.Errorf("variables file must contain a JSON object")
		}
	}
	if err := zen.NewClient(*engineURL).CompleteJob(key, variables); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "completed job %d\n", key)
	return err
}

func runVersion(args []string) {
	if err := executeVersion(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeVersion(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(output)
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	_, err := fmt.Fprintf(output, "tret %s\n", buildVersion())
	return err
}

// buildVersion is the version of the binary. A build from a repository
// carries the version of the commit it was made from.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}

func runCapabilities(args []string) {
	if err := executeCapabilities(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeCapabilities(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	fs.SetOutput(output)
	positional, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return fmt.Errorf("capabilities takes no positional arguments")
	}
	_, err = fmt.Fprint(output, `job types:
  llm-call-v1      one chat completion against an OpenAI-compatible endpoint,
                   with an optional JSON schema for the answer
  exec-command-v1  one shell command in the workspace of the instance

commands:
  server        host the generic workers
  chat          talk to the assistant in the working directory
  deploy        deploy a BPMN file
  start         start a process
  status        list the instances of the engine
  instance      show one instance and its variables
  jobs          list the jobs of an instance
  incidents     list the incidents of an instance
  resolve       resolve an incident, so that the failed step runs again
  complete      complete a job, e.g. to answer a human task
  version       print the version
  capabilities  print this list
`)
	return err
}

// oneKey reads the single key a command works on.
func oneKey(name string, args []string) (int64, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("%s needs exactly one key", name)
	}
	key, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || key <= 0 {
		return 0, fmt.Errorf("%q is not a key", args[0])
	}
	return key, nil
}

// parseFlags parses the flags of a command and returns its positional
// arguments. The flags may come before or after them, so that both
// "tret instance 42 -engine URL" and "tret instance -engine URL 42" work.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	positional := []string{}
	for {
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		if err := fs.Parse(rest[1:]); err != nil {
			return nil, err
		}
	}
}

// resolvedWord says in a word whether an incident was resolved.
func resolvedWord(resolved bool) string {
	if resolved {
		return "yes"
	}
	return "no"
}
