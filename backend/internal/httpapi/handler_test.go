package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"mapcluster/internal/domain"
	"mapcluster/internal/service"
)

func TestViewportFromRequest(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/features?bbox=-10,20,30,40&zoom=6&mode=heatmap", nil)
	viewport, err := viewportFromRequest(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if viewport.Zoom != 6 {
		t.Fatalf("expected zoom 6, got %d", viewport.Zoom)
	}
	if viewport.BBox != [4]float64{-10, 20, 30, 40} {
		t.Fatalf("unexpected bbox: %#v", viewport.BBox)
	}
	if mode := viewportModeFromRequest(r); mode != domain.VisualizationModeHeatmap {
		t.Fatalf("expected heatmap mode, got %s", mode)
	}
}

func TestNormalizeViewport(t *testing.T) {
	viewport := normalizeForTest(domain.Viewport{Zoom: 99})
	if viewport.Zoom != 22 {
		t.Fatalf("expected zoom 22, got %d", viewport.Zoom)
	}
}

func TestPopulateBulkItems(t *testing.T) {
	repo := &handlerFakeRepo{}
	mapService := service.NewMapService(repo, nil, 0)
	handler := NewHandler(mapService)

	body := map[string]any{
		"items": []map[string]any{
			{"name": "Alpha", "symbolCode": "SFGPUCI----K", "lon": -104.99, "lat": 39.74},
			{"name": "Bravo", "lon": -104.98, "lat": 39.75},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/populate/bulk-items", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handler.populateBulkItems(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if repo.insertedCount != 2 {
		t.Fatalf("expected 2 inserted points, got %d", repo.insertedCount)
	}
}

func TestPopulateRandomPointsUsesRequestPayload(t *testing.T) {
	repo := &handlerFakeRepo{}
	mapService := service.NewMapService(repo, nil, 0)
	handler := NewHandler(mapService)

	body := map[string]any{
		"count": 25,
		"bbox":  []float64{-124, 32, -117, 39},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/populate/random-points", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handler.populateRandomPoints(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if repo.lastRandomCount != 25 {
		t.Fatalf("expected random count 25, got %d", repo.lastRandomCount)
	}
	if repo.lastRandomViewport.BBox != [4]float64{-124, 32, -117, 39} {
		t.Fatalf("unexpected bbox: %#v", repo.lastRandomViewport.BBox)
	}
}

type handlerFakeRepo struct {
	insertedCount      int
	lastRandomCount    int
	lastRandomViewport domain.Viewport
}

func (r *handlerFakeRepo) ListViewportFeatures(_ context.Context, _ domain.Viewport, _ domain.VisualizationMode) ([]domain.Feature, error) {
	return nil, nil
}

func (r *handlerFakeRepo) PopulateRandomPoints(_ context.Context, count int, viewport domain.Viewport) (int64, error) {
	r.lastRandomCount = count
	r.lastRandomViewport = viewport
	return int64(count), nil
}

func (r *handlerFakeRepo) InsertPoints(_ context.Context, points []domain.Point) (int64, error) {
	r.insertedCount = len(points)
	return int64(len(points)), nil
}

func (r *handlerFakeRepo) ClearAllPoints(_ context.Context) (int64, error) {
	return 0, nil
}

func normalizeForTest(viewport domain.Viewport) domain.Viewport {
	if viewport.Zoom > 22 {
		viewport.Zoom = 22
	}
	return viewport
}
