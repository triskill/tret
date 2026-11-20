// Command worker serves only the application's custom report task.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pbinitiative/zenbpm/pkg/zenclient"
	"github.com/triskill/tret/examples/classifier/report"
	"github.com/triskill/tret/pkg/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	address := flag.String("engine-grpc", "localhost:9090", "ZenBPM gRPC address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	conn, err := grpc.NewClient(*address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	// worker.Subscribe asks the engine for a lock which outlasts the job, so
	// a slow report is not handed to a second worker while this one is still
	// working on it.
	if _, err := worker.Subscribe(ctx, zenclient.NewGrpc(conn), "classifier-example-report", report.Handle, "example-report-v1"); err != nil {
		log.Fatal(err)
	}
	log.Print("serving example-report-v1")
	<-ctx.Done()
}
