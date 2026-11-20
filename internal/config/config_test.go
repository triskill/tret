package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFromFlagsDefaults(t *testing.T) {
	got, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
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
		"--lock-duration", "7m", "--max-active-jobs", "3", "--client-id", "worker-a",
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
	want.LockDuration = Duration(7 * time.Minute)
	want.MaxActiveJobs = 3
	want.ClientID = "worker-a"
	got, err := FromFlags(args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FromFlags() = %+v, want %+v", got, want)
	}
}

func TestFileAndProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "workers: llm-call-v1\nmodel_profiles:\n  reviewer:\n    base_url: http://provider/v1\n    model: review-model\n    api_key: secret\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := FromFlags([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	p := got.ModelProfiles["reviewer"]
	if got.Workers != "llm-call-v1" || p.BaseURL != "http://provider/v1" || p.Model != "review-model" || p.APIKey != "secret" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestConfigFileBeatsDefaultsFlagsBeatFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "model: file-model\napi_key: file-key\nexec_root: file-dir\nworkers: llm-call-v1\nlock_duration: 9m\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	// File values beat defaults.
	got, err := FromFlags([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "file-model" || got.APIKey != "file-key" || got.ExecRoot != "file-dir" || got.Workers != "llm-call-v1" {
		t.Fatalf("file values not applied: %+v", got)
	}
	if got.LockDuration.Duration() != 9*time.Minute {
		t.Fatalf("lock duration = %s", got.LockDuration)
	}
	// Explicit flags win regardless of position relative to -config.
	for _, args := range [][]string{
		{"-config", path, "-model", "cli-model", "-exec-root", "cli-dir", "-lock-duration", "11m"},
		{"-model", "cli-model", "-config", path, "-exec-root", "cli-dir", "-lock-duration", "11m"},
		{"--model=cli-model", "--config=" + path, "--exec-root=cli-dir", "--lock-duration=11m"},
	} {
		got, err := FromFlags(args)
		if err != nil {
			t.Fatal(err)
		}
		if got.Model != "cli-model" || got.ExecRoot != "cli-dir" || got.APIKey != "file-key" {
			t.Fatalf("FromFlags(%v) = %+v", args, got)
		}
		if got.LockDuration.Duration() != 11*time.Minute {
			t.Fatalf("FromFlags(%v): lock duration = %s", args, got.LockDuration)
		}
	}
}

// A broken configuration is reported, not applied.
func TestFromFlagsReportsErrors(t *testing.T) {
	if _, err := FromFlags([]string{"-unknown"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if _, err := FromFlags([]string{"-config", filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing config file accepted")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("lock_duration: not-a-duration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FromFlags([]string{"-config", path}); err == nil {
		t.Fatal("invalid duration accepted")
	}
}

// A key the configuration does not know is a mistake, not a remark.
func TestUnknownConfigKeyIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("worker: llm-call-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FromFlags([]string{"-config", path}); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

// The configuration of a directory is the file in it.
func TestDefaultConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, defaultConfigFile)
	if err := os.WriteFile(path, []byte("model: from-the-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(previous)
	got, err := FromFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "from-the-file" {
		t.Fatalf("model = %q", got.Model)
	}
}

func TestDurationFormats(t *testing.T) {
	var d Duration
	if err := d.UnmarshalJSON([]byte(`"90s"`)); err != nil || d.Duration() != 90*time.Second {
		t.Fatalf("json string: %v %s", err, d)
	}
	if err := d.UnmarshalJSON([]byte(`1000000000`)); err != nil || d.Duration() != time.Second {
		t.Fatalf("json number: %v %s", err, d)
	}
	if err := d.UnmarshalJSON([]byte(`"nonsense"`)); err == nil {
		t.Fatal("invalid json duration accepted")
	}
}
