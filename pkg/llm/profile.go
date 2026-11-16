// Package llm provides a process-neutral, single-completion BPMN worker.
package llm

// Profile holds server-owned connection settings. Process variables select a
// profile by name; they cannot override its endpoint, model, or credentials.
type Profile struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
	Model   string `json:"model" yaml:"model"`
	APIKey  string `json:"api_key" yaml:"api_key"`
}
