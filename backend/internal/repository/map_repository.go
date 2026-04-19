package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"mapcluster/internal/db"
	"mapcluster/internal/domain"
)

type MapRepository struct {
	pool *pgxpool.Pool
}

func NewMapRepository(pool *pgxpool.Pool) *MapRepository {
	return &MapRepository{pool: pool}
}

func (r *MapRepository) ListViewportFeatures(ctx context.Context, viewport domain.Viewport, mode domain.VisualizationMode) ([]domain.Feature, error) {
	rows, err := r.pool.Query(ctx, `
		WITH visible AS (
			SELECT COUNT(*)::int AS cnt
			FROM map_feature_clusters
			WHERE zoom_level = $5
			  AND geom && ST_MakeEnvelope($1, $2, $3, $4, 4326)
		),
		effective AS (
			SELECT GREATEST(
				0,
				LEAST(
					22,
					$5 - (
						CASE
							WHEN cnt > 1200 THEN 6
							WHEN cnt > 700 THEN 5
							WHEN cnt > 350 THEN 4
							WHEN cnt > 180 THEN 3
							WHEN cnt > 90 THEN 2
							WHEN cnt > 40 THEN 1
							ELSE 0
						END +
						CASE
							WHEN $5 <= 4 THEN 3
							WHEN $5 <= 7 THEN 2
							WHEN $5 <= 10 THEN 1
							ELSE 0
						END
					)
				)
			) AS zoom_level
			FROM visible
		)
		SELECT
			feature_id,
			feature_count,
			names[1] AS name,
			symbol_code,
			ST_X(geom) AS lon,
			ST_Y(geom) AS lat
		FROM map_feature_clusters
		WHERE zoom_level = (SELECT zoom_level FROM effective)
		  AND geom && ST_MakeEnvelope($1, $2, $3, $4, 4326)
		ORDER BY feature_id
	`, viewport.BBox[0], viewport.BBox[1], viewport.BBox[2], viewport.BBox[3], viewport.Zoom)
	if err != nil {
		return nil, fmt.Errorf("query viewport features: %w", err)
	}
	defer rows.Close()

	features := make([]domain.Feature, 0)
	for rows.Next() {
		var (
			featureID    string
			featureCount int
			name         string
			symbolCode   string
			lon          float64
			lat          float64
		)
		if err := rows.Scan(&featureID, &featureCount, &name, &symbolCode, &lon, &lat); err != nil {
			return nil, fmt.Errorf("scan viewport feature: %w", err)
		}

		properties := make(map[string]any, 2)
		if mode == domain.VisualizationModeHeatmap {
			properties["intensity"] = featureCount
		} else {
			properties["count"] = featureCount
			if featureCount > 1 {
				properties["cluster"] = true
			} else {
				properties["name"] = name
				properties["symbol_code"] = symbolCode
			}
		}

		features = append(features, domain.Feature{
			ID:   featureID,
			Type: "Feature",
			Geometry: domain.Geometry{
				Type:        "Point",
				Coordinates: []float64{lon, lat},
			},
			Properties: properties,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate viewport features: %w", err)
	}
	return features, nil
}

func clusterZoomForCount(zoom int, count int) int {
	backoff := 0
	switch {
	case count > 1200:
		backoff = 6
	case count > 700:
		backoff = 5
	case count > 350:
		backoff = 4
	case count > 180:
		backoff = 3
	case count > 90:
		backoff = 2
	case count > 40:
		backoff = 1
	}

	switch {
	case zoom <= 4:
		backoff += 3
	case zoom <= 7:
		backoff += 2
	case zoom <= 10:
		backoff += 1
	}

	zoom -= backoff
	if zoom < 0 {
		return 0
	}
	if zoom > 22 {
		return 22
	}
	return zoom
}

func (r *MapRepository) PopulateRandomPoints(ctx context.Context, count int, viewport domain.Viewport) (int64, error) {
	if count <= 0 {
		return 0, nil
	}

	commandTag, err := r.pool.Exec(ctx, `
		INSERT INTO map_features (name, symbol_code, geom)
		SELECT
			format('Random Point %s', n),
			'SFGPUCI----K',
			ST_SetSRID(
				ST_MakePoint(
					LEAST(180.0, GREATEST(-180.0, $2::double precision + random() * ($4::double precision - $2::double precision))),
					LEAST(90.0, GREATEST(-90.0, $3::double precision + random() * ($5::double precision - $3::double precision)))
				),
				4326
			)
		FROM generate_series(1, $1) AS n
	`, count, viewport.BBox[0], viewport.BBox[1], viewport.BBox[2], viewport.BBox[3])
	if err != nil {
		return 0, fmt.Errorf("insert random points: %w", err)
	}

	if err := db.RefreshClusters(ctx, r.pool); err != nil {
		return 0, fmt.Errorf("refresh clusters after random point insert: %w", err)
	}

	return commandTag.RowsAffected(), nil
}

func (r *MapRepository) InsertPoints(ctx context.Context, points []domain.Point) (int64, error) {
	if len(points) == 0 {
		return 0, nil
	}

	names := make([]string, 0, len(points))
	symbolCodes := make([]string, 0, len(points))
	lons := make([]float64, 0, len(points))
	lats := make([]float64, 0, len(points))
	for _, point := range points {
		names = append(names, point.Name)
		symbolCodes = append(symbolCodes, point.SymbolCode)
		lons = append(lons, point.Lon)
		lats = append(lats, point.Lat)
	}

	commandTag, err := r.pool.Exec(ctx, `
		INSERT INTO map_features (name, symbol_code, geom)
		SELECT
			t.name,
			t.symbol_code,
			ST_SetSRID(ST_MakePoint(t.lon, t.lat), 4326)
		FROM UNNEST($1::text[], $2::text[], $3::double precision[], $4::double precision[])
			AS t(name, symbol_code, lon, lat)
	`, names, symbolCodes, lons, lats)
	if err != nil {
		return 0, fmt.Errorf("insert points: %w", err)
	}

	if err := db.RefreshClusters(ctx, r.pool); err != nil {
		return 0, fmt.Errorf("refresh clusters after insert: %w", err)
	}

	return commandTag.RowsAffected(), nil
}

func (r *MapRepository) ClearAllPoints(ctx context.Context) (int64, error) {
	var count int64
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM map_features").Scan(&count); err != nil {
		return 0, fmt.Errorf("count map features before clear: %w", err)
	}

	if _, err := r.pool.Exec(ctx, "TRUNCATE map_feature_clusters, map_features RESTART IDENTITY"); err != nil {
		return 0, fmt.Errorf("clear map features: %w", err)
	}

	return count, nil
}
