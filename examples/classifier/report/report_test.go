package report

import (
	"context"
	"testing"

	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

func TestHandle(t *testing.T) {
	output, err := Handle(context.Background(), &proto.WaitingJob{
		InputVariables: []byte(`{"classification":{"structured_output":{"approved":true}}}`),
		Headers:        map[string]string{"queue": "accounts"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output["queue"] != "accounts" || output["report"] != "Queue: accounts\nApproved: true" {
		t.Fatalf("unexpected report: %#v", output)
	}
}

func TestHandleInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		headers     map[string]string
	}{
		{name: "missing approved", input: `{}`, headers: map[string]string{"queue": "accepted"}},
		{name: "missing queue", input: `{"classification":{"structured_output":{"approved":true}}}`},
		{name: "malformed", input: `{`, headers: map[string]string{"queue": "accepted"}},
		{name: "null", input: `null`, headers: map[string]string{"queue": "accepted"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := Handle(context.Background(), &proto.WaitingJob{InputVariables: []byte(tc.input), Headers: tc.headers})
			if output != nil || err == nil || err.Retries == nil || *err.Retries != 0 || err.ErrorCode != "invalid_input" {
				t.Fatalf("expected terminal invalid_input, output=%#v err=%#v", output, err)
			}
		})
	}
}
