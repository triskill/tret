package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/triskill/tret/pkg/llm"
	"github.com/triskill/tret/pkg/shellexec"
	"github.com/triskill/tret/pkg/worker"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Workers string `json:"workers" yaml:"workers"`
	// ModelProfiles are server-owned endpoints/credentials, never process variables.
	ModelProfiles map[string]llm.Profile `json:"model_profiles" yaml:"model_profiles"`
	EngineREST    string                 `json:"engine_rest" yaml:"engine_rest"`
	EngineGRPC    string                 `json:"engine_grpc" yaml:"engine_grpc"`
	// ExecRoot is the workspace directory exec-command-v1 jobs run in.
	ExecRoot string `json:"exec_root" yaml:"exec_root"`
	Model    string `json:"model" yaml:"model"`
	APIKey   string `json:"api_key" yaml:"api_key"`
	APIBase  string `json:"api_base" yaml:"api_base"`
	// OllamaBase is used for the default profile when no API key is set.
	OllamaBase string `json:"ollama_base" yaml:"ollama_base"`
	// LockDuration is how long the engine reserves a delivered job for this
	// worker. It must outlast the longest job the worker runs, or the engine
	// hands the job to a second worker while the first is still busy.
	LockDuration Duration `json:"lock_duration" yaml:"lock_duration"`
	// MaxActiveJobs caps the jobs of one type this worker holds at once.
	// Zero means the engine's default.
	MaxActiveJobs int `json:"max_active_jobs" yaml:"max_active_jobs"`
	// ClientID names this worker at the engine. It must be unique per
	// process; the default is derived from the host and the pid.
	ClientID string `json:"client_id" yaml:"client_id"`
}

func defaults() *Config {
	return &Config{
		Workers:      strings.Join([]string{llm.JobType, shellexec.JobType}, ","),
		EngineREST:   "http://localhost:8080",
		EngineGRPC:   "localhost:9090",
		ExecRoot:     ".",
		Model:        "gpt-4o-mini",
		APIBase:      "https://api.openai.com/v1",
		OllamaBase:   "http://localhost:11434/v1",
		LockDuration: Duration(worker.DefaultLockDuration),
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

// FromFlags builds the configuration from the defaults, the config file and
// the flags, in that order of precedence. It reports an error instead of
// exiting, so the caller decides what to do with a bad configuration.
func FromFlags(args []string) (*Config, error) {
	cfg := defaults()

	// The flag values are kept apart from the configuration, so a config
	// file cannot overwrite a flag the operator gave.
	var (
		configFile    string
		workers       = cfg.Workers
		engineREST    = cfg.EngineREST
		engineGRPC    = cfg.EngineGRPC
		execRoot      = cfg.ExecRoot
		model         = cfg.Model
		apiKey        = cfg.APIKey
		apiBase       = cfg.APIBase
		ollamaBase    = cfg.OllamaBase
		lockDuration  = time.Duration(cfg.LockDuration)
		maxActiveJobs = cfg.MaxActiveJobs
		clientID      = cfg.ClientID
	)

	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.StringVar(&workers, "workers", workers, "comma-separated built-in job types to serve")
	fs.StringVar(&configFile, "config", "", "config file (json/yaml)")
	fs.StringVar(&engineREST, "engine-rest", engineREST, "engine REST URL")
	fs.StringVar(&engineGRPC, "engine-grpc", engineGRPC, "engine gRPC URL")
	fs.StringVar(&execRoot, "exec-root", execRoot, "workspace directory for exec-command-v1 jobs")
	fs.StringVar(&model, "model", model, "model")
	fs.StringVar(&apiKey, "api-key", apiKey, "api key")
	fs.StringVar(&apiBase, "api-base", apiBase, "OpenAI-compatible API base URL")
	fs.StringVar(&ollamaBase, "ollama-base", ollamaBase, "Ollama API base URL")
	fs.DurationVar(&lockDuration, "lock-duration", lockDuration, "how long the engine reserves a delivered job for this worker")
	fs.IntVar(&maxActiveJobs, "max-active-jobs", maxActiveJobs, "jobs of one type this worker holds at once (0: engine default)")
	fs.StringVar(&clientID, "client-id", clientID, "name of this worker at the engine (default: host and pid)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if configFile != "" {
		if err := loadFile(configFile, cfg); err != nil {
			return nil, fmt.Errorf("config file %s: %w", configFile, err)
		}
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["workers"] {
		cfg.Workers = workers
	}
	if explicit["engine-rest"] {
		cfg.EngineREST = engineREST
	}
	if explicit["engine-grpc"] {
		cfg.EngineGRPC = engineGRPC
	}
	if explicit["exec-root"] {
		cfg.ExecRoot = execRoot
	}
	if explicit["model"] {
		cfg.Model = model
	}
	if explicit["api-key"] {
		cfg.APIKey = apiKey
	}
	if explicit["api-base"] {
		cfg.APIBase = apiBase
	}
	if explicit["ollama-base"] {
		cfg.OllamaBase = ollamaBase
	}
	if explicit["lock-duration"] {
		cfg.LockDuration = Duration(lockDuration)
	}
	if explicit["max-active-jobs"] {
		cfg.MaxActiveJobs = maxActiveJobs
	}
	if explicit["client-id"] {
		cfg.ClientID = clientID
	}
	return cfg, nil
}

// Duration is a time.Duration which reads a Go duration string ("5m") from a
// config file and a number of nanoseconds from JSON.
type Duration time.Duration

func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var text string
	if err := value.Decode(&text); err != nil {
		var nanos int64
		if err := value.Decode(&nanos); err != nil {
			return fmt.Errorf("duration must be a string like \"5m\" or a number of nanoseconds")
		}
		*d = Duration(nanos)
		return nil
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err == nil {
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", text, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var nanos int64
	if err := json.Unmarshal(b, &nanos); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\" or a number of nanoseconds")
	}
	*d = Duration(nanos)
	return nil
}
