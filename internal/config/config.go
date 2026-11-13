package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	EngineREST    string `json:"engine_rest" yaml:"engine_rest"`
	EngineGRPC    string `json:"engine_grpc" yaml:"engine_grpc"`
	StateDir      string `json:"state_dir" yaml:"state_dir"`
	Workspace     string `json:"workspace" yaml:"workspace"`
	Model         string `json:"model" yaml:"model"`
	APIKey        string `json:"api_key" yaml:"api_key"`
	APIBase       string `json:"api_base" yaml:"api_base"`
	OllamaBase    string `json:"ollama_base" yaml:"ollama_base"`
	MaxToolCalls  int    `json:"max_tool_calls" yaml:"max_tool_calls"`
	MaxModelTurns int    `json:"max_model_turns" yaml:"max_model_turns"`
	WallClockSec  int    `json:"wall_clock_sec" yaml:"wall_clock_sec"`
}

func defaults() *Config {
	return &Config{
		EngineREST:    "http://localhost:8080",
		EngineGRPC:    "localhost:9090",
		StateDir:      "./data",
		Workspace:     ".",
		Model:         "gpt-4o-mini",
		APIBase:       "https://api.openai.com/v1",
		OllamaBase:    "http://localhost:11434/v1",
		MaxToolCalls:  24,
		MaxModelTurns: 12,
		WallClockSec:  900,
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
	fs.StringVar(&configFile, "config", "", "config file (json/yaml)")
	fs.StringVar(&cfg.EngineREST, "engine-rest", cfg.EngineREST, "engine REST URL")
	fs.StringVar(&cfg.EngineGRPC, "engine-grpc", cfg.EngineGRPC, "engine gRPC URL")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "state dir")
	fs.StringVar(&cfg.Workspace, "workspace", cfg.Workspace, "workspace dir")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "api key")
	fs.StringVar(&cfg.APIBase, "api-base", cfg.APIBase, "OpenAI-compatible API base URL")
	fs.StringVar(&cfg.OllamaBase, "ollama-base", cfg.OllamaBase, "Ollama API base URL")
	fs.IntVar(&cfg.MaxToolCalls, "max-tool-calls", cfg.MaxToolCalls, "max tool calls")
	fs.IntVar(&cfg.MaxModelTurns, "max-model-turns", cfg.MaxModelTurns, "max model turns")
	fs.IntVar(&cfg.WallClockSec, "wall-clock", cfg.WallClockSec, "wall clock sec")
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
