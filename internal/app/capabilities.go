package app

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// databaseCapabilities records which optional PostgreSQL extensions this
// deployment actually has. They are detected at start rather than assumed,
// because an offline site may not be able to install contrib modules and the
// service has to work either way.
type databaseCapabilities struct {
	Trigram bool
	Vector  bool
}

func detectCapabilities(ctx context.Context, db *pgxpool.Pool) databaseCapabilities {
	var capabilities databaseCapabilities
	_ = db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_trgm'),
		EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector')`).
		Scan(&capabilities.Trigram, &capabilities.Vector)
	return capabilities
}
