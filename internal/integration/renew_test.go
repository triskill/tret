package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
	"github.com/triskill/tret/internal/zen"
	"github.com/triskill/tret/pkg/worker"
)

// slowBPMN is a process with a single task of the job type "slow-v1".
const slowBPMN = `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" xmlns:zenbpm="http://zenbpm.pbinitiative.org/1.0" id="SlowDefinitions" targetNamespace="http://tret/examples">
 <bpmn:process id="slow_process" name="Slow process" isExecutable="true">
  <bpmn:startEvent id="Start"><bpmn:outgoing>ToSlow</bpmn:outgoing></bpmn:startEvent>
  <bpmn:sequenceFlow id="ToSlow" sourceRef="Start" targetRef="Slow" />
  <bpmn:serviceTask id="Slow" name="Slow task">
   <bpmn:extensionElements>
    <zenbpm:taskDefinition type="slow-v1" retries="1" />
    <bpmn:ioMapping><zenbpm:output source="=done" target="done" /></bpmn:ioMapping>
   </bpmn:extensionElements>
   <bpmn:incoming>ToSlow</bpmn:incoming><bpmn:outgoing>ToEnd</bpmn:outgoing>
  </bpmn:serviceTask>
  <bpmn:sequenceFlow id="ToEnd" sourceRef="Slow" targetRef="End" />
  <bpmn:endEvent id="End" name="End"><bpmn:incoming>ToEnd</bpmn:incoming></bpmn:endEvent>
 </bpmn:process>
</bpmn:definitions>`

// TestLockRenewalLive proves that the lock of a delivered job is renewed
// while the job runs. A job which outlives the lock of its subscription is
// handed to a second worker by the engine, and its side effect happens
// twice; with the renewal the job runs once.
func TestLockRenewalLive(t *testing.T) {
	if os.Getenv("TRET_INTEGRATION") != "1" {
		t.Skip("set TRET_INTEGRATION=1 to use a dedicated live ZenBPM engine")
	}
	rest, addr := os.Getenv("TRET_INTEGRATION_REST"), os.Getenv("TRET_INTEGRATION_GRPC")
	if rest == "" {
		rest = "http://localhost:8080"
	}
	if addr == "" {
		addr = "localhost:9090"
	}
	client := zen.NewClient(strings.TrimRight(rest, "/"))
	// The job type is unique per run, so the worker of another run does not
	// pick the job up.
	jobType := fmt.Sprintf("slow-v1-%d", time.Now().UnixNano())
	definition, _, err := client.Deploy([]byte(strings.ReplaceAll(slowBPMN, "slow-v1", jobType)))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := zen.DialGRPC(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	const lock = 10 * time.Second
	var calls atomic.Int32
	registry := worker.NewRegistry()
	// The declared runtime is short, the job outlives it: only a renewed
	// lock keeps the engine from handing the job to a second worker.
	if err := registry.Register(worker.Spec{JobType: jobType, LockDuration: lock, MaxRuntime: time.Second}, func(ctx context.Context, job *proto.WaitingJob) (map[string]any, *zenclient.WorkerError) {
		calls.Add(1)
		select {
		case <-time.After(3 * lock):
		case <-ctx.Done():
		}
		return map[string]any{"done": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := zen.NewGrpcClient(conn).RegisterWorkerWithOptions(ctx, fmt.Sprintf("renew-%d", time.Now().UnixNano()), registry.Handle, registry.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	registry.SetRenewer(handle)

	key, err := client.StartInstance(definition, fmt.Sprintf("renew-%d", time.Now().UnixNano()), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CancelInstance(key) })
	awaitState(t, client, key, "completed", 90*time.Second)
	if got := calls.Load(); got != 1 {
		t.Errorf("the side effect ran %d times, want 1: the lock of the job was not renewed", got)
	}
}

// awaitState polls until the instance reaches the wanted state.
func awaitState(t *testing.T, client *zen.Client, key int64, state string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		instance, err := client.GetInstance(key)
		if err != nil {
			t.Fatal(err)
		}
		if instance.State == state {
			return
		}
		if instance.State == "failed" || instance.State == "terminated" {
			incidents, _ := client.ListIncidents(key)
			t.Fatalf("the instance ended in %q: incidents=%#v", instance.State, incidents)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the instance did not reach %q within %s", state, within)
}
