package config

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var serverOnlyFlags = []string{"workers", "exec-root", "exec-workspace", "client-id", "max-active-jobs", "log-level"}

func TestFromChatFlagsHelp(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "help")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stderr := os.Stderr
	os.Stderr = output
	defer func() { os.Stderr = stderr }()

	if _, err := FromChatFlags([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help: got %v, want flag.ErrHelp", err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(output)
	if err != nil {
		t.Fatal(err)
	}
	help := string(b)
	for _, want := range []string{"Usage of chat:", `default ".tret/journal"`, "-config ", "-engine-rest ", "-engine-grpc ", "-model ", "-api-key ", "-api-base ", "-ollama-base ", "-lock-duration ", "-journal-dir ", "-journal-lease "} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
	for _, name := range serverOnlyFlags {
		if strings.Contains(help, "-"+name+" ") {
			t.Errorf("help includes server-only flag %q", name)
		}
	}
}

func TestFromChatFlagsRejectsServerFlags(t *testing.T) {
	for _, name := range serverOnlyFlags {
		if _, err := FromChatFlags([]string{"--" + name + "=1"}); err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -"+name) {
			t.Errorf("--%s: got %v, want undefined flag error", name, err)
		}
	}
}

func TestFromChatFlagsSharedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	path := filepath.Join(dir, "config.yaml")
	data := `workers: llm-call-v1
exec_root: worker-dir
exec_workspace: instance
client_id: worker-a
max_active_jobs: 3
log_level: debug
model: file-model
journal_dir: file-journal
model_profiles:
  reviewer:
    model: review-model
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args    []string
		model   string
		journal string
	}{
		{nil, "file-model", "file-journal"},
		{[]string{"--model=cli-model", "--config", path, "--journal-dir=cli-journal"}, "cli-model", "cli-journal"},
		{[]string{"--journal-dir="}, "file-model", ".tret/journal"},
	} {
		got, err := FromChatFlags(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if got.Model != tc.model || got.JournalDir != tc.journal || got.ModelProfiles["reviewer"].Model != "review-model" {
			t.Errorf("FromChatFlags(%v) = %+v, want model %q, journal %q and reviewer profile", tc.args, got, tc.model, tc.journal)
		}
	}
}

func TestFromChatFlagsJournalDefault(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := FromChatFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.JournalDir != ".tret/journal" {
		t.Errorf("chat journal = %q, want .tret/journal", got.JournalDir)
	}
}
