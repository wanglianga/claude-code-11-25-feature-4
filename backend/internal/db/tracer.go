package db

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5"
)

type errTrace struct{}

func (errTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, data.SQL)
}

type traceKey struct{}

func (errTrace) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil {
		if sql, _ := ctx.Value(traceKey{}).(string); sql != "" {
			log.Printf("SQL FAILED: %v\n  %s", data.Err, sql)
		}
	}
}
