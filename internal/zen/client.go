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

type StartInstanceResult struct {
	Key                 int64  `json:"key"`
	ProcessDefinitionKey int64  `json:"processDefinitionKey"`
	State               string `json:"state"`
}

func (c *Client) StartInstance(processDefinitionKey, businessKey string, variables map[string]any) (int64, error) {
	body := map[string]any{
		"processDefinitionKey": processDefinitionKey,
		"businessKey":          businessKey,
		"variables":           variables,
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/process-instances", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("start instance failed: %s: %s", resp.Status, string(rb))
	}
	var r StartInstanceResult
	if err := json.Unmarshal(rb, &r); err != nil {
		return 0, fmt.Errorf("parse start response: %w", err)
	}
	return r.Key, nil
}
