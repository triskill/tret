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

func FromFlags() *Config {
	pre := flag.NewFlagSet("pre", flag.ExitOnError)
	configPath := pre.String("config", "", "config file (json/yaml)")
	pre.Parse(os.Args[1:])

	cfg := defaults()
	if *configPath != "" {
		if err := loadFile(*configPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "config load error: %v\n", err)
			os.Exit(1)
		}
	}

	fs := flag.NewFlagSet("main", flag.ExitOnError)
	var configFile string
	fs.StringVar(&configFile, "config", "", "config file (json/yaml)")
	fs.StringVar(&cfg.EngineREST, "engine-rest", cfg.EngineREST, "engine REST URL")
	fs.StringVar(&cfg.EngineGRPC, "engine-grpc", cfg.EngineGRPC, "engine gRPC URL")
	fs.StringVar(&cfg.StateDir, "state-dir", cfg.StateDir, "state dir")
	fs.StringVar(&cfg.Workspace, "workspace", cfg.Workspace, "workspace dir")
	fs.StringVar(&cfg.Model, "model", cfg.Model, "model")
	fs.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "api key")
	fs.IntVar(&cfg.MaxToolCalls, "max-tool-calls", cfg.MaxToolCalls, "max tool calls")
	fs.IntVar(&cfg.MaxModelTurns, "max-model-turns", cfg.MaxModelTurns, "max model turns")
	fs.IntVar(&cfg.WallClockSec, "wall-clock", cfg.WallClockSec, "wall clock sec")
	fs.Parse(os.Args[1:])
	return cfg
}
