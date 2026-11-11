package zen

import (
	"context"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/pbinitiative/zenbpm/pkg/zenclient/proto"
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

func Register(ctx context.Context, g *zenclient.Grpc, clientID string, f zenclient.WorkerFunc, types ...string) error {
	_, err := g.RegisterWorker(ctx, clientID, f, types...)
	return err
}

type WaitingJob = proto.WaitingJob
