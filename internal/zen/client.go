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
	ProcessDefinitionKey int64 `json:"processDefinitionKey"`
}

func (c *Client) Deploy(bpmn []byte) (int64, bool, error) {
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/v1/process-definitions", bytes.NewReader(bpmn))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, false, fmt.Errorf("deploy failed: %s: %s", resp.Status, string(body))
	}
	var r DeployResult
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, false, fmt.Errorf("parse deploy response: %w", err)
	}
	return r.ProcessDefinitionKey, resp.StatusCode == http.StatusCreated, nil
}

type ProcessDefinition struct {
	Key           int64  `json:"key"`
	Version       int    `json:"version"`
	BpmnProcessID string `json:"bpmnProcessId"`
}

type processDefinitionsPage struct {
	Items []ProcessDefinition `json:"items"`
}

// GetLatestProcessDefinition returns the newest deployed version of the
// process with the given BPMN process id, or an error if none is deployed.
func (c *Client) GetLatestProcessDefinition(bpmnProcessID string) (*ProcessDefinition, error) {
	req, err := http.NewRequest(http.MethodGet,
		c.BaseURL+"/v1/process-definitions?bpmnProcessId="+bpmnProcessID+"&onlyLatest=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list process definitions failed: %s: %s", resp.Status, string(body))
	}
	var page processDefinitionsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("parse process definitions response: %w", err)
	}
	for i := range page.Items {
		if page.Items[i].BpmnProcessID == bpmnProcessID {
			return &page.Items[i], nil
		}
	}
	return nil, fmt.Errorf("process definition %q not deployed", bpmnProcessID)
}

type StartInstanceResult struct {
	Key                  int64  `json:"key"`
	ProcessDefinitionKey int64  `json:"processDefinitionKey"`
	State                string `json:"state"`
}

func (c *Client) StartInstance(processDefinitionKey int64, businessKey string, variables map[string]any) (int64, error) {
	body := map[string]any{
		"processDefinitionKey": processDefinitionKey,
		"businessKey":          businessKey,
		"variables":            variables,
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
	Key                  int64          `json:"key"`
	BusinessKey          string         `json:"businessKey"`
	State                string         `json:"state"`
	ProcessDefinitionKey int64          `json:"processDefinitionKey"`
	Variables            map[string]any `json:"variables"`
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
	// engine returns {partitions:[{items:[...] }]}
	var wrap struct {
		Partitions []struct {
			Items []Instance `json:"items"`
		} `json:"partitions"`
	}
	if err := json.Unmarshal(b, &wrap); err == nil && len(wrap.Partitions) > 0 && len(wrap.Partitions[0].Items) > 0 {
		return &wrap.Partitions[0].Items[0], nil
	}
	var inst Instance
	if err := json.Unmarshal(b, &inst); err == nil && inst.Key != 0 {
		return &inst, nil
	}
	var arr []Instance
	if err := json.Unmarshal(b, &arr); err == nil && len(arr) > 0 {
		return &arr[0], nil
	}
	return nil, fmt.Errorf("parse instance: %s", string(b))
}

type Job struct {
	Key            int64          `json:"key"`
	Type           string         `json:"type"`
	State          string         `json:"state"`
	InputVariables map[string]any `json:"inputVariables"`
}

func (c *Client) GetInstance(key int64) (*Instance, error) {
	resp, err := c.HTTPClient.Get(fmt.Sprintf("%s/v1/process-instances/%d", c.BaseURL, key))
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
		return nil, err
	}
	return &inst, nil
}

func (c *Client) ListJobs(processInstanceKey int64) ([]Job, error) {
	var jobs []Job
	for page := 1; ; page++ {
		resp, err := c.HTTPClient.Get(fmt.Sprintf("%s/v1/process-instances/%d/jobs?page=%d", c.BaseURL, processInstanceKey, page))
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list jobs failed: %s: %s", resp.Status, string(b))
		}
		var wrap struct {
			Items      []Job `json:"items"`
			Page       int   `json:"page"`
			Size       int   `json:"size"`
			TotalCount int   `json:"totalCount"`
		}
		if err := json.Unmarshal(b, &wrap); err != nil {
			var items []Job
			if err := json.Unmarshal(b, &items); err != nil {
				return nil, fmt.Errorf("parse jobs: %w", err)
			}
			return append(jobs, items...), nil
		}
		jobs = append(jobs, wrap.Items...)
		if len(wrap.Items) == 0 || wrap.Size == 0 || wrap.Page*wrap.Size >= wrap.TotalCount {
			return jobs, nil
		}
	}
}

type Incident struct {
	Key     int64  `json:"key"`
	Message string `json:"message"`
	State   string `json:"state"`
}

func (c *Client) ListIncidents(processInstanceKey int64) ([]Incident, error) {
	resp, err := c.HTTPClient.Get(fmt.Sprintf("%s/v1/process-instances/%d/incidents", c.BaseURL, processInstanceKey))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list incidents failed: %s: %s", resp.Status, string(b))
	}
	var wrap struct {
		Items []Incident `json:"items"`
	}
	if err := json.Unmarshal(b, &wrap); err == nil {
		return wrap.Items, nil
	}
	var incs []Incident
	if err := json.Unmarshal(b, &incs); err != nil {
		return nil, fmt.Errorf("parse incidents: %w", err)
	}
	return incs, nil
}

// Typed outcomes
type OutcomeError struct {
	Kind    string
	Message string
}

func (e *OutcomeError) Error() string { return e.Kind + ": " + e.Message }

func (c *Client) CompleteJob(jobKey int64, variables map[string]any) error {
	b, _ := json.Marshal(map[string]any{"variables": variables})
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/jobs/%d/complete", c.BaseURL, jobKey), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("complete job failed: %s: %s", resp.Status, string(rb))
	}
	return nil
}

func (c *Client) CancelInstance(key int64) error {
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/process-instances/%d/cancel", c.BaseURL, key), nil)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel failed: %s: %s", resp.Status, string(b))
	}
	return nil
}
