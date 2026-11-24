package main

import (
	"fmt"
	"github.com/triskill/tret/internal/config"
	"github.com/triskill/tret/pkg/llm"
)

func modelProfiles(cfg *config.Config) (map[string]llm.Profile, error) {
	// The endpoint of the default profile is the configured one. The key is
	// a credential: a local endpoint which needs none must not be replaced
	// by another one behind the operator's back.
	base := cfg.APIBase
	profiles := make(map[string]llm.Profile, len(cfg.ModelProfiles)+1)
	for name, profile := range cfg.ModelProfiles {
		profiles[name] = profile
	}
	if _, ok := profiles["default"]; !ok {
		profiles["default"] = llm.Profile{BaseURL: base, Model: cfg.Model, APIKey: cfg.APIKey}
	}
	for name, profile := range profiles {
		if err := profile.Validate(); err != nil {
			return nil, fmt.Errorf("model profile %q is unusable: %w", name, err)
		}
	}
	return profiles, nil
}
