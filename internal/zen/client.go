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

type Instance struct {
	Key                 int64  `json:"key"`
	BusinessKey         string `json:"businessKey"`
	State               string `json:"state"`
	ProcessDefinitionKey int64 `json:"processDefinitionKey"`
}

func (c *Client) GetInstanceByBusinessKey(businessKey string) (*Instance, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/v1/process-instances?businessKey=" + businessKey)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get instance failed: %s: %s", resp.Status, string(b))
	}
	var inst Instance
	if err := json.Unmarshal(b, &inst); err != nil {
		// maybe array
		var arr []Instance
		if err2 := json.Unmarshal(b, &arr); err2 == nil && len(arr) > 0 {
			return &arr[0], nil
		}
		return nil, fmt.Errorf("parse instance: %w", err)
	}
	return &inst, nil
}

type Job struct {
	Key   int64  `json:"key"`
	Type  string `json:"type"`
	State string `json:"state"`
}

func (c *Client) ListJobs(processInstanceKey int64) ([]Job, error) {
	resp, err := c.HTTPClient.Get(fmt.Sprintf("%s/v1/jobs?processInstanceKey=%d", c.BaseURL, processInstanceKey))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list jobs failed: %s: %s", resp.Status, string(b))
	}
	var jobs []Job
	if err := json.Unmarshal(b, &jobs); err != nil {
		return nil, fmt.Errorf("parse jobs: %w", err)
	}
	return jobs, nil
}

type Incident struct {
	Key     int64  `json:"key"`
	Message string `json:"message"`
	State   string `json:"state"`
}

func (c *Client) ListIncidents(processInstanceKey int64) ([]Incident, error) {
	resp, err := c.HTTPClient.Get(fmt.Sprintf("%s/v1/incidents?processInstanceKey=%d", c.BaseURL, processInstanceKey))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list incidents failed: %s: %s", resp.Status, string(b))
	}
	var incs []Incident
	if err := json.Unmarshal(b, &incs); err != nil {
		return nil, fmt.Errorf("parse incidents: %w", err)
	}
	return incs, nil
}
