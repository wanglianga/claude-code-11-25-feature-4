package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"recycletrace/backend/internal/app"
	"recycletrace/backend/internal/db"
)

func main() {
	dsn := flag.String("dsn", "", "postgres dsn (defaults to DATABASE_URL or ccdev)")
	flag.Parse()
	if *dsn == "" {
		*dsn = defaultDSN()
	}
	ctx := context.Background()
	d, err := db.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	a := app.New(d)
	if _, err := a.ResetDemo(ctx); err != nil {
		log.Fatalf("reset: %v", err)
	}
	if err := a.SeedDemo(ctx); err != nil {
		log.Fatalf("seed: %v", err)
	}
	log.Printf("seed complete")
}

func defaultDSN() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://ccdev:ccdev@127.0.0.1:5432/ccdev?sslmode=disable"
}

var _ = json.Marshal
var _ = fmt.Sprintf
var _ = time.Now
