package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/triskill/tret/internal/zen"
)

func runDeploy(args []string) {
	if err := executeDeploy(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeDeploy(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(output)
	file := fs.String("file", "", "BPMN file to deploy (required)")
	engineURL := fs.String("engine", "http://localhost:8080", "engine REST URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || fs.NArg() != 0 {
		return fmt.Errorf("deploy requires -file and no positional arguments")
	}
	bpmn, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("read BPMN file: %w", err)
	}
	key, _, err := zen.NewClient(*engineURL).Deploy(bpmn)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, key)
	return err
}

func runStart(args []string) {
	if err := executeStart(args, os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func executeStart(args []string, output io.Writer) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(output)
	process := fs.String("process", "", "BPMN process ID (latest deployed version)")
	definition := fs.Int64("definition", 0, "exact process definition key")
	variablesFile := fs.String("variables", "", "JSON object file containing process variables")
	businessKey := fs.String("business-key", "", "business key for the new instance")
	engineURL := fs.String("engine", "http://localhost:8080", "engine REST URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	processSet, definitionSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "process":
			processSet = true
		case "definition":
			definitionSet = true
		}
	})
	if processSet == definitionSet || (processSet && *process == "") || fs.NArg() != 0 {
		return fmt.Errorf("start requires exactly one of -process or -definition and no positional arguments")
	}
	if definitionSet && *definition <= 0 {
		return fmt.Errorf("-definition must be a positive key")
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
	zc := zen.NewClient(*engineURL)
	key := *definition
	if !definitionSet {
		def, err := zc.GetLatestProcessDefinition(*process)
		if err != nil {
			return err
		}
		key = def.Key
	}
	instanceKey, err := zc.StartInstance(key, *businessKey, variables)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, instanceKey)
	return err
}
