package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFromFlagsDefaults(t *testing.T) {
	if got := FromFlags(nil); !reflect.DeepEqual(got, defaults()) {
		t.Fatalf("FromFlags(nil) = %+v, want %+v", got, defaults())
	}
}

func TestFromFlagsOverrides(t *testing.T) {
	args := []string{
		"--engine-rest", "http://engine:8080", "--engine-grpc=engine:9090",
		"--state-dir", "./state", "--workspace", "./workspace",
		"--model", "test-model", "--api-key", "test-key",
		"--api-base", "http://api/v1", "--ollama-base", "http://ollama/v1",
		"--max-tool-calls", "5", "--max-model-turns", "6", "--wall-clock", "7",
	}
	want := &Config{
		EngineREST: "http://engine:8080", EngineGRPC: "engine:9090",
		StateDir: "./state", Workspace: "./workspace", Model: "test-model",
		APIKey: "test-key", APIBase: "http://api/v1", OllamaBase: "http://ollama/v1",
		MaxToolCalls: 5, MaxModelTurns: 6, WallClockSec: 7,
	}
	if got := FromFlags(args); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromFlags() = %+v, want %+v", got, want)
	}
}

func TestFromFlagsConfigAndOverrides(t *testing.T) {
	files := []struct {
		name string
		data string
	}{
		{"config.yaml", "model: file-model\napi_key: file-key\nmax_tool_calls: 99\n"},
		{"config.json", `{"model":"file-model","api_key":"file-key","max_tool_calls":99}`},
	}
	for _, file := range files {
		t.Run(file.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), file.name)
			if err := os.WriteFile(path, []byte(file.data), 0600); err != nil {
				t.Fatal(err)
			}
			cases := []struct {
				name string
				args []string
			}{
				{"file only", []string{"-config", path}},
				{"config first", []string{"-config", path, "-model", "cli-model", "-max-tool-calls", "0"}},
				{"config middle", []string{"-model", "cli-model", "-config", path, "-max-tool-calls", "0"}},
				{"config last", []string{"-model", "cli-model", "-max-tool-calls", "0", "-config", path}},
				{"equals syntax", []string{"--model=cli-model", "--config=" + path, "--max-tool-calls=0"}},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					want := defaults()
					want.Model = "file-model"
					want.APIKey = "file-key"
					want.MaxToolCalls = 99
					if tc.name != "file only" {
						want.Model = "cli-model"
						want.MaxToolCalls = 0
					}
					if got := FromFlags(tc.args); !reflect.DeepEqual(got, want) {
						t.Fatalf("FromFlags() = %+v, want %+v", got, want)
					}
				})
			}
		})
	}
}
