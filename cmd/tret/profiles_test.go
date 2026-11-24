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
	if profiles["default"].BaseURL != cfg.OllamaBase {
		t.Fatal("local default endpoint changed")
	}
	cfg.ModelProfiles = map[string]llm.Profile{"default": {BaseURL: "http://custom/v1", Model: "test"}}
	profiles, err = modelProfiles(cfg)
	if err != nil || profiles["default"].BaseURL != "http://custom/v1" {
		t.Fatalf("named profile lost: %v %v", profiles, err)
	}
}
