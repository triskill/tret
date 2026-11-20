package zen

import (
	"context"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
	"github.com/triskill/tret/pkg/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func DialGRPC(addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func NewGrpcClient(conn *grpc.ClientConn) *zenclient.Grpc {
	return zenclient.NewGrpc(conn)
}

type WorkerFunc = zenclient.WorkerFunc
type WorkerError = zenclient.WorkerError

// Register subscribes a worker to job types with a lock which outlasts the
// work, so a job that runs longer than the engine's default lock is not
// handed to a second worker while the first is still busy with it.
func Register(ctx context.Context, g *zenclient.Grpc, clientID string, f zenclient.WorkerFunc, types ...string) error {
	_, err := worker.Subscribe(ctx, g, clientID, f, types...)
	return err
}

type WaitingJob = proto.WaitingJob
