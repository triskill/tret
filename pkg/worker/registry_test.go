package worker

import (
	"context"
	"testing"
	"time"

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
	if err := r.Register(Spec{JobType: "example-v1"}, handler); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Spec{JobType: "example-v1"}, handler); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.Register(Spec{JobType: ""}, handler); err == nil {
		t.Fatal("empty type accepted")
	}
	if err := r.Register(Spec{JobType: "missing"}, nil); err == nil {
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

// A lock which does not outlast the worker runtime lets the engine hand the
// job to a second worker while the first is still working on it.
func TestRegistryRefusesShortLock(t *testing.T) {
	r := NewRegistry()
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) { return nil, nil }
	err := r.Register(Spec{JobType: "slow-v1", LockDuration: time.Minute, MaxRuntime: 2 * time.Minute}, handler)
	if err == nil {
		t.Fatal("lock shorter than the runtime accepted")
	}
	if err := r.Register(Spec{JobType: "slow-v1", LockDuration: 5 * time.Minute, MaxRuntime: 2 * time.Minute}, handler); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryOptionsAlwaysSetALock(t *testing.T) {
	r := NewRegistry()
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) { return nil, nil }
	if err := r.Register(Spec{JobType: "example-v1"}, handler); err != nil {
		t.Fatal(err)
	}
	if len(r.Options()) != 1 {
		t.Fatalf("options=%v", r.Options())
	}
	// A subscription without a lock duration would take the engine default
	// of 30 s, which is shorter than many jobs run.
	if len(Options(Spec{JobType: "example-v1"})) != 1 {
		t.Fatal("no options built")
	}
	if len(Options()) != 0 {
		t.Fatal("options built without a spec")
	}
}

func TestRegistryDrainWaitsForRunningJobs(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	started := make(chan struct{})
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		close(started)
		<-release
		return nil, nil
	}
	if err := r.Register(Spec{JobType: "example-v1"}, handler); err != nil {
		t.Fatal(err)
	}
	typ := "example-v1"
	go func() {
		r.Handle(context.Background(), &proto.WaitingJob{Type: &typ})
	}()
	<-started
	r.StopAccepting()
	if _, err := r.Handle(context.Background(), &proto.WaitingJob{Type: &typ}); err == nil {
		t.Error("accepted a late delivery while the admitted job was still running")
	}
	drained := make(chan error, 1)
	go func() { drained <- r.Drain(context.Background()) }()
	select {
	case <-drained:
		t.Fatal("drain returned while a job was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return")
	}
}

func TestRegistryDrainStopsIntake(t *testing.T) {
	r := NewRegistry()
	typ := "example-v1"
	if err := r.Register(Spec{JobType: typ}, func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		t.Error("handler ran for a delivery after shutdown began")
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Handle(context.Background(), &proto.WaitingJob{Type: &typ}); err == nil {
		t.Error("accepted a delivery after drain")
	}
}

func TestRegistryDrainRespectsContext(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	handler := func(context.Context, *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		close(started)
		<-release
		return nil, nil
	}
	if err := r.Register(Spec{JobType: "example-v1"}, handler); err != nil {
		t.Fatal(err)
	}
	typ := "example-v1"
	go func() { r.Handle(context.Background(), &proto.WaitingJob{Type: &typ}) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Drain(ctx); err == nil {
		t.Fatal("drain ignored the deadline")
	}
}
