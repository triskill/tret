package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFromFlagsDefaults(t *testing.T) {
	got := FromFlags(nil)
	want := defaults()
	if got.Workers != "llm-call-v1,exec-command-v1" {
		t.Fatalf("default workers = %q", got.Workers)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromFlags(nil) = %+v, want %+v", got, want)
	}
}

func TestFromFlagsOverrides(t *testing.T) {
	args := []string{
		"--engine-rest", "http://engine:8080", "--engine-grpc=engine:9090",
		"--exec-root", "./worker-dir", "--workers", "llm-call-v1",
		"--model", "test-model", "--api-key", "test-key",
		"--api-base", "http://api/v1", "--ollama-base", "http://ollama/v1",
	}
	want := defaults()
	want.EngineREST = "http://engine:8080"
	want.EngineGRPC = "engine:9090"
	want.ExecRoot = "./worker-dir"
	want.Workers = "llm-call-v1"
	want.Model = "test-model"
	want.APIKey = "test-key"
	want.APIBase = "http://api/v1"
	want.OllamaBase = "http://ollama/v1"
	if got := FromFlags(args); !reflect.DeepEqual(got, want) {
		t.Fatalf("FromFlags() = %+v, want %+v", got, want)
	}
}

func TestFileAndProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "workers: llm-call-v1\nmodel_profiles:\n  reviewer:\n    base_url: http://provider/v1\n    model: review-model\n    api_key: secret\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got := FromFlags([]string{"-config", path})
	p := got.ModelProfiles["reviewer"]
	if got.Workers != "llm-call-v1" || p.BaseURL != "http://provider/v1" || p.Model != "review-model" || p.APIKey != "secret" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestConfigFileBeatsDefaultsFlagsBeatFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "model: file-model\napi_key: file-key\nexec_root: file-dir\nworkers: llm-call-v1\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	// File values beat defaults.
	got := FromFlags([]string{"-config", path})
	if got.Model != "file-model" || got.APIKey != "file-key" || got.ExecRoot != "file-dir" || got.Workers != "llm-call-v1" {
		t.Fatalf("file values not applied: %+v", got)
	}
	// Explicit flags win regardless of position relative to -config.
	for _, args := range [][]string{
		{"-config", path, "-model", "cli-model", "-exec-root", "cli-dir"},
		{"-model", "cli-model", "-config", path, "-exec-root", "cli-dir"},
		{"--model=cli-model", "--config=" + path, "--exec-root=cli-dir"},
	} {
		got := FromFlags(args)
		if got.Model != "cli-model" || got.ExecRoot != "cli-dir" || got.APIKey != "file-key" {
			t.Fatalf("FromFlags(%v) = %+v", args, got)
		}
	}
}
