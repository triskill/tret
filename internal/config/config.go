package config

import "flag"

type Config struct {
	EngineREST string
	EngineGRPC string
	StateDir   string
	Workspace  string
	Model      string
	APIKey     string
	MaxToolCalls int
	MaxModelTurns int
	WallClockSec int
}

func FromFlags() *Config {
	c := &Config{}
	flag.StringVar(&c.EngineREST, "engine-rest", "http://localhost:8080", "engine REST URL")
	flag.StringVar(&c.EngineGRPC, "engine-grpc", "localhost:9090", "engine gRPC URL")
	flag.StringVar(&c.StateDir, "state-dir", "./state", "state dir")
	flag.StringVar(&c.Workspace, "workspace", ".", "workspace dir")
	flag.StringVar(&c.Model, "model", "gpt-4o-mini", "model")
	flag.StringVar(&c.APIKey, "api-key", "", "api key")
	flag.IntVar(&c.MaxToolCalls, "max-tool-calls", 24, "max tool calls")
	flag.IntVar(&c.MaxModelTurns, "max-model-turns", 12, "max model turns")
	flag.IntVar(&c.WallClockSec, "wall-clock", 900, "wall clock sec")
	flag.Parse()
	return c
}
