package main

import (
	"context"
	"fmt"
	"go.vicoworks.com/agent-reliability-lab/internal/config"
	"go.vicoworks.com/agent-reliability-lab/internal/dataset"
	"go.vicoworks.com/agent-reliability-lab/internal/store"
	migrations "go.vicoworks.com/agent-reliability-lab/sql"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/admin init [--apply] | seed")
		os.Exit(2)
	}
	if os.Args[1] == "init" && (len(os.Args) < 3 || os.Args[2] != "--apply") {
		fmt.Print(migrations.Init)
		return
	}
	cfg, err := config.Load("async")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Pool.Close()
	switch os.Args[1] {
	case "init":
		_, err = db.Pool.Exec(ctx, migrations.Init)
	case "seed":
		_, err = dataset.Seed(ctx, db)
	default:
		err = fmt.Errorf("unknown command")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "operation failed; verify schema and database access")
		os.Exit(1)
	}
	fmt.Println("completed")
}
