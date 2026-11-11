package main

import (
	"fmt"
	"os"

	"github.com/pbinitiative/zenbpm-go/internal/config"
)

func main() {
	cfg := config.FromFlags()
	fmt.Printf("server starting; engine=%s state=%s\n", cfg.EngineREST, cfg.StateDir)
	// placeholder
	os.Exit(0)
}
