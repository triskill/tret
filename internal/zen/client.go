package zen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTPClient: &http.Client{}}
}

type DeployResult struct {
	ProcessDefinitionKey string `json:"processDefinitionKey"`
}

func (c *Client) Deploy(bpmn []byte) (string, bool, error) {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/process-definitions", bytes.NewReader(bpmn))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", false, fmt.Errorf("deploy failed: %s: %s", resp.Status, string(body))
	}
	var r DeployResult
	if err := json.Unmarshal(body, &r); err != nil {
		return "", false, fmt.Errorf("parse deploy response: %w", err)
	}
	return r.ProcessDefinitionKey, resp.StatusCode == http.StatusCreated, nil
}
