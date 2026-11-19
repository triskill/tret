package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/shellexec"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Workers string `json:"workers" yaml:"workers"`
	// ModelProfiles are server-owned endpoints/credentials, never process variables.
	ModelProfiles map[string]llm.Profile `json:"model_profiles" yaml:"model_profiles"`
	EngineREST    string                 `json:"engine_rest" yaml:"engine_rest"`
	EngineGRPC    string                 `json:"engine_grpc" yaml:"engine_grpc"`
	// ExecRoot is the workspace directory exec-command-v1 jobs run in.
	ExecRoot   string `json:"exec_root" yaml:"exec_root"`
	Model      string `json:"model" yaml:"model"`
	APIKey     string `json:"api_key" yaml:"api_key"`
	APIBase    string `json:"api_base" yaml:"api_base"`
	OllamaBase string `json:"ollama_base" yaml:"ollama_base"`
}

func defaults() *Config {
	return &Config{
		Workers:    strings.Join([]string{llm.JobType, shellexec.JobType}, ","),
		EngineREST: "http://localhost:8080",
		EngineGRPC: "localhost:9090",
		ExecRoot:   ".",
		Model:      "gpt-4o-mini",
		APIBase:    "https://api.openai.com/v1",
		OllamaBase: "http://localhost:11434/v1",
	}
}

func loadFile(path string, cfg *Config) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		return json.Unmarshal(b, cfg)
	case ".yaml", ".yml":
		return yaml.Unmarshal(b, cfg)
	default:
		// try yaml then json
		if err := yaml.Unmarshal(b, cfg); err != nil {
			return json.Unmarshal(b, cfg)
		}
		return nil
	}
}

func FromFlags(args []string) *Config {
	cfg := defaults()

	fs := flag.NewFlagSet("main", flag.ExitOnError)
	var configFile string
	fs.StringVar(&cfg.Workers, "workers", cfg.Workers, "comma-separated built-in job types to serve")
	fs.StringVar(&configFile, "config", "", "config file (json/yaml)")
	fs.StringVar(&cfg.EngineREST, "engine-rest", cfg.EngineREST, "engine REST URL")
	fs.StringVar(&cfg.EngineGRPC, "engine-grpc", cfg.EngineGRPC, "engine gRPC URL")
	fs.StringVar(&cfg.ExecRoot, "exec-root", cfg.ExecRoot, "workspace directory for exec-command-v1 jobs")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "api key")
	fs.StringVar(&cfg.APIBase, "api-base", cfg.APIBase, "OpenAI-compatible API base URL")
	fs.StringVar(&cfg.OllamaBase, "ollama-base", cfg.OllamaBase, "Ollama API base URL")
	// Parse the complete flag set to find -config regardless of flag order.
	fs.Parse(args)
	if configFile != "" {
		if err := loadFile(configFile, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config load error: %v\n", err)
			os.Exit(1)
		}
		// Explicit flags take precedence over values loaded from the file.
		fs.Parse(args)
	}
	return cfg
}
