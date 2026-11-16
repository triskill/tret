package worker

import (
	"context"
	"testing"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
)

func TestRegistryDispatch(t *testing.T) {
	r := NewRegistry()
	called := 0
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		called++
		return map[string]any{"ok": true}, nil
	}
	if err := r.Register("example-v1", handler); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("example-v1", handler); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.Register("", handler); err == nil {
		t.Fatal("empty type accepted")
	}
	if err := r.Register("missing", nil); err == nil {
		t.Fatal("nil handler accepted")
	}
	typ := "example-v1"
	out, err := r.Handle(context.Background(), &proto.WaitingJob{Type: &typ})
	if err != nil || out["ok"] != true || called != 1 {
		t.Fatalf("out=%v err=%v called=%d", out, err, called)
	}
	for _, job := range []*proto.WaitingJob{nil, {}} {
		_, err := r.Handle(context.Background(), job)
		if err == nil || err.ErrorCode != "unknown_job_type" || called != 1 {
			t.Fatalf("unknown job dispatched: %v", err)
		}
	}
	if got := r.Types(); len(got) != 1 || got[0] != typ {
		t.Fatalf("types=%v", got)
	}
}
