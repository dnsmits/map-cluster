package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	statements := []string{
		"CREATE EXTENSION IF NOT EXISTS postgis",
		`CREATE TABLE IF NOT EXISTS map_features (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			symbol_code TEXT NOT NULL,
			geom geometry(Point, 4326) NOT NULL,
			geom_3857 geometry(Point, 3857),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		"CREATE INDEX IF NOT EXISTS idx_map_features_geom ON map_features USING GIST (geom)",
		"CREATE INDEX IF NOT EXISTS idx_map_features_geom_3857 ON map_features USING GIST (geom_3857)",
		`CREATE TABLE IF NOT EXISTS map_feature_clusters (
			id BIGSERIAL PRIMARY KEY,
			zoom_level SMALLINT NOT NULL,
			feature_id TEXT NOT NULL,
			feature_count INTEGER NOT NULL,
			ids BIGINT[] NOT NULL,
			names TEXT[] NOT NULL,
			symbol_code TEXT NOT NULL,
			geom geometry(Point, 4326) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE (zoom_level, feature_id)
		)`,
		"CREATE INDEX IF NOT EXISTS idx_map_feature_clusters_geom ON map_feature_clusters USING GIST (geom)",
		"CREATE INDEX IF NOT EXISTS idx_map_feature_clusters_zoom_level ON map_feature_clusters (zoom_level)",
		`CREATE OR REPLACE FUNCTION map_features_sync_mercator() RETURNS trigger AS $$
		BEGIN
			NEW.geom_3857 := ST_Transform(NEW.geom, 3857);
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`,
		`DROP TRIGGER IF EXISTS trg_map_features_sync_mercator ON map_features`,
		`CREATE TRIGGER trg_map_features_sync_mercator
			BEFORE INSERT OR UPDATE OF geom ON map_features
			FOR EACH ROW EXECUTE FUNCTION map_features_sync_mercator()`,
		`UPDATE map_features SET geom_3857 = ST_Transform(geom, 3857) WHERE geom_3857 IS NULL`,
	}

	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("exec schema statement: %w", err)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM map_features").Scan(&count); err != nil {
		return fmt.Errorf("count map_features: %w", err)
	}
	if count > 0 {
		if err := RefreshClusters(ctx, pool); err != nil {
			return err
		}
		return nil
	}

	seedStatements := []string{
		`INSERT INTO map_features (name, symbol_code, geom) VALUES
			('Alpha Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-77.0365, 38.8977), 4326)),
			('Bravo Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-122.4194, 37.7749), 4326)),
			('Charlie Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-87.6298, 41.8781), 4326)),
			('Delta Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-95.3698, 29.7604), 4326)),
			('Echo Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-80.1918, 25.7617), 4326)),
			('Foxtrot Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-118.2437, 34.0522), 4326)),
			('Golf Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-71.0589, 42.3601), 4326)),
			('Hotel Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-122.3321, 47.6062), 4326)),
			('India Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-104.9903, 39.7392), 4326)),
			('Juliet Team', 'SFGPUCI----K', ST_SetSRID(ST_MakePoint(-73.9352, 40.7306), 4326))`,
	}

	for _, statement := range seedStatements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("seed map_features: %w", err)
		}
	}
	if err := RefreshClusters(ctx, pool); err != nil {
		return err
	}
	return nil
}

func RefreshClusters(ctx context.Context, pool *pgxpool.Pool) error {
	statements := []string{
		"TRUNCATE map_feature_clusters RESTART IDENTITY",
		`INSERT INTO map_feature_clusters (zoom_level, feature_id, feature_count, ids, names, symbol_code, geom)
		WITH zoom_levels AS (
			SELECT generate_series(0, 22) AS zoom
		),
		prepared AS (
			SELECT
				z.zoom,
				mf.id,
				mf.name,
				mf.symbol_code,
				mf.geom_3857,
				CASE
					WHEN z.zoom >= 15 THEN 0::double precision
					WHEN z.zoom >= 13 THEN 700::double precision
					WHEN z.zoom >= 11 THEN 2200::double precision
					WHEN z.zoom >= 9 THEN 8000::double precision
					WHEN z.zoom >= 7 THEN 30000::double precision
					WHEN z.zoom >= 5 THEN 85000::double precision
					ELSE 180000::double precision
				END AS cell_size
			FROM map_features mf
			CROSS JOIN zoom_levels z
		),
		bucketed AS (
			SELECT
				zoom,
				id,
				name,
				symbol_code,
				geom_3857,
				CASE
					WHEN cell_size = 0 THEN 'point:' || id::text
					ELSE format(
						'cluster:z%s:%s:%s',
						zoom,
						floor(ST_X(geom_3857) / cell_size)::bigint,
						floor(ST_Y(geom_3857) / cell_size)::bigint
					)
				END AS feature_id
			FROM prepared
		),
		grouped AS (
			SELECT
				zoom,
				feature_id,
				COUNT(*) AS feature_count,
				ARRAY_AGG(id ORDER BY id) AS ids,
				ARRAY_AGG(name ORDER BY id) AS names,
				ARRAY_AGG(symbol_code ORDER BY id) AS symbol_codes,
				ST_Transform(ST_Centroid(ST_Collect(geom_3857)), 4326) AS geom_4326
			FROM bucketed
			GROUP BY zoom, feature_id
		)
		SELECT
			zoom,
			feature_id,
			feature_count,
			ids,
			names,
			symbol_codes[1] AS symbol_code,
			geom_4326
		FROM grouped`,
	}

	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("refresh clusters: %w", err)
		}
	}
	return nil
}
