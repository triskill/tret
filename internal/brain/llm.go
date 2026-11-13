package brain

import "context"

// FinishLLM is the MOCK_LLM test double: immediately takes the finish route.
type FinishLLM struct{}

func (f *FinishLLM) Complete(ctx context.Context, _ []Message) (*Response, error) {
	return &Response{Content: `{"route":"finish","message":"done"}`}, nil
}
