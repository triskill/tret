// Package llm provides a process-neutral, single-completion BPMN worker.
package llm

import (
	"fmt"
	"net/url"
	"strings"
)

// Profile holds server-owned connection settings. Process variables select a
// profile by name; they cannot override its endpoint, model, or credentials.
type Profile struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
	Model   string `json:"model" yaml:"model"`
	APIKey  string `json:"api_key" yaml:"api_key"`
}

// Validate reports whether the profile can serve a request. A broken profile
// is a server misconfiguration, not a bad request from the process: it is
// checked at startup, so it never reaches a job as an error code.
func (p Profile) Validate() error {
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("base_url is required")
	}
	endpoint, err := url.Parse(p.BaseURL)
	if err != nil {
		return fmt.Errorf("base_url %q is not a URL: %w", p.BaseURL, err)
	}
	if endpoint.Host == "" {
		return fmt.Errorf("base_url %q has no host", p.BaseURL)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("base_url %q must use http or https", p.BaseURL)
	}
	if strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}
