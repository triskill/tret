package main

import (
	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/pkg/llm"
	"testing"
)

func TestModelProfiles(t *testing.T) {
	cfg, err := config.FromFlags([]string{"-config", "../../config.example.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := modelProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The endpoint of the default profile is the configured one. A key is a
	// credential, never a choice of endpoint: a local endpoint which needs
	// no key must not be replaced by another one.
	if profiles["default"].BaseURL != cfg.APIBase {
		t.Fatalf("the default endpoint is %q, want the configured %q", profiles["default"].BaseURL, cfg.APIBase)
	}
	cfg.APIBase = "http://localhost:11434/v1"
	cfg.APIKey = ""
	profiles, err = modelProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if profiles["default"].BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("the default endpoint is %q", profiles["default"].BaseURL)
	}
	cfg.APIKey = "secret"
	profiles, err = modelProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if profiles["default"].BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("a key moved the endpoint to %q", profiles["default"].BaseURL)
	}
	cfg.ModelProfiles = map[string]llm.Profile{"default": {BaseURL: "http://custom/v1", Model: "test"}}
	profiles, err = modelProfiles(cfg)
	if err != nil || profiles["default"].BaseURL != "http://custom/v1" {
		t.Fatalf("named profile lost: %v %v", profiles, err)
	}
}
