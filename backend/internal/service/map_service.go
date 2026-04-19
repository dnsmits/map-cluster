package service

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"mapcluster/internal/cache"
	"mapcluster/internal/domain"
)

//go:embed mcdonalds_points.json
var mcdonaldsPointsJSON embed.FS

type PointRepository interface {
	ListViewportFeatures(ctx context.Context, viewport domain.Viewport, mode domain.VisualizationMode) ([]domain.Feature, error)
	PopulateRandomPoints(ctx context.Context, count int, viewport domain.Viewport) (int64, error)
	InsertPoints(ctx context.Context, points []domain.Point) (int64, error)
	ClearAllPoints(ctx context.Context) (int64, error)
}

type MapService struct {
	repo      PointRepository
	cache     *cache.RedisCache
	cacheTTL  time.Duration
	mu        sync.Mutex
	listeners map[int]chan struct{}
	nextID    int
}

func NewMapService(repo PointRepository, cache *cache.RedisCache, cacheTTL time.Duration) *MapService {
	return &MapService{repo: repo, cache: cache, cacheTTL: cacheTTL, listeners: map[int]chan struct{}{}}
}

func (s *MapService) SubscribeUpdates() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	s.mu.Lock()
	if s.listeners == nil {
		s.listeners = map[int]chan struct{}{}
	}
	id := s.nextID
	s.nextID++
	s.listeners[id] = ch
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		listener, ok := s.listeners[id]
		if ok {
			delete(s.listeners, id)
			close(listener)
		}
		s.mu.Unlock()
	}

	return ch, cancel
}

func (s *MapService) ViewportFeatures(ctx context.Context, viewport domain.Viewport, mode domain.VisualizationMode) ([]domain.Feature, error) {
	viewport = normalizeViewport(viewport)
	mode = domain.NormalizeVisualizationMode(string(mode))
	cacheKey := cacheKey(viewport, mode)

	if s.cache != nil {
		if cached, ok, err := s.cache.Get(ctx, cacheKey); err == nil && ok {
			var features []domain.Feature
			if err := json.Unmarshal(cached, &features); err == nil {
				return features, nil
			}
		}
	}

	features, err := s.repo.ListViewportFeatures(ctx, viewport, mode)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(features)
	if err != nil {
		return nil, fmt.Errorf("marshal features: %w", err)
	}

	if s.cache != nil {
		if err := s.cache.Set(ctx, cacheKey, payload, s.cacheTTL); err != nil {
			log.Printf("cache set failed for %s: %v", cacheKey, err)
		}
	}

	return features, nil
}

func (s *MapService) PopulateRandomPoints(ctx context.Context, count int, viewport domain.Viewport) (int64, error) {
	if count <= 0 {
		return 0, fmt.Errorf("count must be greater than zero")
	}
	viewport = normalizeViewport(viewport)

	inserted, err := s.repo.PopulateRandomPoints(ctx, count, viewport)
	if err != nil {
		return 0, err
	}

	s.invalidateViewportCache(ctx)
	s.notifyDataChanged()

	return inserted, nil
}

func (s *MapService) PopulateMcDonaldsPoints(ctx context.Context) (int64, error) {
	points, err := pointsFromJSON()
	if err != nil {
		return 0, err
	}
	if len(points) == 0 {
		return 0, fmt.Errorf("no points found in embedded dataset")
	}

	inserted, err := s.repo.InsertPoints(ctx, points)
	if err != nil {
		return 0, err
	}

	s.invalidateViewportCache(ctx)
	s.notifyDataChanged()

	return inserted, nil
}

func (s *MapService) PopulateBulkPoints(ctx context.Context, points []domain.Point) (int64, error) {
	normalizedPoints, err := normalizePoints(points)
	if err != nil {
		return 0, err
	}

	inserted, err := s.repo.InsertPoints(ctx, normalizedPoints)
	if err != nil {
		return 0, err
	}

	s.invalidateViewportCache(ctx)
	s.notifyDataChanged()

	return inserted, nil
}

func (s *MapService) ClearAllPoints(ctx context.Context) (int64, error) {
	cleared, err := s.repo.ClearAllPoints(ctx)
	if err != nil {
		return 0, err
	}

	s.invalidateViewportCache(ctx)
	s.notifyDataChanged()

	return cleared, nil
}

func normalizeViewport(viewport domain.Viewport) domain.Viewport {
	if viewport.Zoom < 0 {
		viewport.Zoom = 0
	}
	if viewport.Zoom > 22 {
		viewport.Zoom = 22
	}
	if viewport.BBox == [4]float64{} {
		viewport.BBox = [4]float64{-180, -90, 180, 90}
	}
	if viewport.BBox[0] > viewport.BBox[2] {
		viewport.BBox[0], viewport.BBox[2] = viewport.BBox[2], viewport.BBox[0]
	}
	if viewport.BBox[1] > viewport.BBox[3] {
		viewport.BBox[1], viewport.BBox[3] = viewport.BBox[3], viewport.BBox[1]
	}
	return viewport
}

func cacheKey(viewport domain.Viewport, mode domain.VisualizationMode) string {
	parts := []string{
		fmt.Sprintf("%.4f", viewport.BBox[0]),
		fmt.Sprintf("%.4f", viewport.BBox[1]),
		fmt.Sprintf("%.4f", viewport.BBox[2]),
		fmt.Sprintf("%.4f", viewport.BBox[3]),
		fmt.Sprintf("z%d", viewport.Zoom),
	}
	parts = append(parts, string(mode))
	return "viewport:" + strings.Join(parts, ":")
}

func (s *MapService) invalidateViewportCache(ctx context.Context) {
	if s.cache == nil {
		return
	}

	if err := s.cache.DeleteByPrefix(ctx, "viewport:"); err != nil {
		log.Printf("cache invalidation failed: %v", err)
	}
}

func (s *MapService) notifyDataChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, listener := range s.listeners {
		select {
		case listener <- struct{}{}:
		default:
		}
	}
}

func pointsFromJSON() ([]domain.Point, error) {
	raw, err := mcdonaldsPointsJSON.ReadFile("mcdonalds_points.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded mcdonalds dataset: %w", err)
	}

	points := make([]domain.Point, 0)
	if err := json.Unmarshal(raw, &points); err != nil {
		return nil, fmt.Errorf("unmarshal embedded mcdonalds dataset: %w", err)
	}

	for i := range points {
		points[i].Name = strings.TrimSpace(points[i].Name)
		if points[i].SymbolCode == "" {
			points[i].SymbolCode = "SFGPUCI----K"
		}
	}

	return points, nil
}

func normalizePoints(points []domain.Point) ([]domain.Point, error) {
	if len(points) == 0 {
		return nil, fmt.Errorf("at least one point is required")
	}

	normalized := make([]domain.Point, len(points))
	for i, point := range points {
		point.Name = strings.TrimSpace(point.Name)
		point.SymbolCode = strings.TrimSpace(point.SymbolCode)
		if point.Name == "" {
			return nil, fmt.Errorf("items[%d].name is required", i)
		}
		if point.SymbolCode == "" {
			point.SymbolCode = "SFGPUCI----K"
		}
		if point.Lon < -180 || point.Lon > 180 {
			return nil, fmt.Errorf("items[%d].lon must be between -180 and 180", i)
		}
		if point.Lat < -90 || point.Lat > 90 {
			return nil, fmt.Errorf("items[%d].lat must be between -90 and 90", i)
		}
		normalized[i] = point
	}

	return normalized, nil
}
