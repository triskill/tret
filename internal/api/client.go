package api

import (
	"fmt"
	"net/http"
	"time"
)

type Instruction struct {
	TaskID      string `json:"task_id"`
	Command     string `json:"command"`
	Context     string `json:"context"`
	IsToolCall  bool   `json:"is_tool_call"`
}

type Report struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"` 
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) FetchInstruction(taskID string) (*Instruction, error) {
	return &Instruction{
		TaskID:     taskID,
		Command:    "echo 'Hello from ZenBPM via Go Agent!'",
		Context:    "Initial task",
		IsToolCall: true,
	}, nil
}

func (c *Client) ReportResult(report Report) error {
	fmt.Printf("[API] Reporting task %s as %s\n", report.TaskID, report.Status)
	return nil
}
