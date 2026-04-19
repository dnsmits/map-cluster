package service

import (
	"context"
	"testing"
	"time"

	"mapcluster/internal/domain"
)

type fakeRepo struct {
	gotViewport    domain.Viewport
	gotMode        domain.VisualizationMode
	populateCount  int
	randomViewport domain.Viewport
	bulkPoints     []domain.Point
	insertedCount  int
	clearCount     int64
}

func (f *fakeRepo) ListViewportFeatures(_ context.Context, viewport domain.Viewport, mode domain.VisualizationMode) ([]domain.Feature, error) {
	f.gotViewport = viewport
	f.gotMode = mode
	return []domain.Feature{
		{
			ID:   "point:1",
			Type: "Feature",
			Geometry: domain.Geometry{
				Type:        "Point",
				Coordinates: []float64{1, 1},
			},
			Properties: map[string]any{
				"cluster":     false,
				"count":       1,
				"symbol_code": "SFGPUCI----K",
			},
		},
	}, nil
}

func (f *fakeRepo) PopulateRandomPoints(_ context.Context, count int, viewport domain.Viewport) (int64, error) {
	f.populateCount = count
	f.randomViewport = viewport
	return int64(count), nil
}

func (f *fakeRepo) InsertPoints(_ context.Context, points []domain.Point) (int64, error) {
	f.bulkPoints = append([]domain.Point(nil), points...)
	f.insertedCount = len(points)
	return int64(len(points)), nil
}

func (f *fakeRepo) ClearAllPoints(_ context.Context) (int64, error) {
	return f.clearCount, nil
}

func TestViewportFeaturesNormalizesViewportAndReturnsFeatures(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)

	features, err := service.ViewportFeatures(context.Background(), domain.Viewport{
		Zoom: 99,
		BBox: [4]float64{10, 20, -10, -20},
	}, domain.VisualizationModeHeatmap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if repo.gotViewport.Zoom != 22 {
		t.Fatalf("expected normalized zoom 22, got %d", repo.gotViewport.Zoom)
	}
	if repo.gotViewport.BBox != [4]float64{-10, -20, 10, 20} {
		t.Fatalf("unexpected normalized bbox: %#v", repo.gotViewport.BBox)
	}
	if repo.gotMode != domain.VisualizationModeHeatmap {
		t.Fatalf("expected heatmap mode, got %s", repo.gotMode)
	}
	if len(features) != 1 {
		t.Fatalf("expected one feature, got %d", len(features))
	}
	if features[0].ID != "point:1" {
		t.Fatalf("unexpected feature id: %s", features[0].ID)
	}
}

func TestPopulateRandomPointsDelegatesToRepository(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)

	inserted, err := service.PopulateRandomPoints(context.Background(), 100000, domain.Viewport{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inserted != 100000 {
		t.Fatalf("expected 100000 inserted points, got %d", inserted)
	}
	if repo.populateCount != 100000 {
		t.Fatalf("expected repository to receive count 100000, got %d", repo.populateCount)
	}
}

func TestPopulateRandomPointsRejectsNonPositiveCount(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)

	_, err := service.PopulateRandomPoints(context.Background(), 0, domain.Viewport{})
	if err == nil {
		t.Fatal("expected error for non-positive count")
	}
	if repo.populateCount != 0 {
		t.Fatalf("expected repository not to be called, got count %d", repo.populateCount)
	}
}

func TestPopulateRandomPointsBroadcastsUpdate(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)
	updates, cancel := service.SubscribeUpdates()
	defer cancel()

	if _, err := service.PopulateRandomPoints(context.Background(), 1, domain.Viewport{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-updates:
	case <-time.After(time.Second):
		t.Fatal("expected update notification")
	}
}

func TestPopulateMcDonaldsPointsDelegatesToRepository(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)

	inserted, err := service.PopulateMcDonaldsPoints(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inserted <= 0 {
		t.Fatalf("expected embedded dataset to insert points, got %d", inserted)
	}
	if repo.insertedCount != int(inserted) {
		t.Fatalf("expected repository to receive %d points, got %d", inserted, repo.insertedCount)
	}
}

func TestPopulateBulkPointsNormalizesAndDelegatesToRepository(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)

	inserted, err := service.PopulateBulkPoints(context.Background(), []domain.Point{{
		Name:       "  Custom Item  ",
		SymbolCode: "",
		Lon:        -104.8,
		Lat:        39.7,
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("expected 1 inserted point, got %d", inserted)
	}
	if len(repo.bulkPoints) != 1 {
		t.Fatalf("expected repository to receive 1 point, got %d", len(repo.bulkPoints))
	}
	if repo.bulkPoints[0].Name != "Custom Item" {
		t.Fatalf("expected trimmed name, got %q", repo.bulkPoints[0].Name)
	}
	if repo.bulkPoints[0].SymbolCode != "SFGPUCI----K" {
		t.Fatalf("expected default symbol code, got %q", repo.bulkPoints[0].SymbolCode)
	}
}

func TestPopulateBulkPointsRejectsInvalidPoints(t *testing.T) {
	repo := &fakeRepo{}
	service := NewMapService(repo, nil, 0)

	_, err := service.PopulateBulkPoints(context.Background(), []domain.Point{{Name: "", Lon: 0, Lat: 0}})
	if err == nil {
		t.Fatal("expected validation error for blank name")
	}
	if len(repo.bulkPoints) != 0 {
		t.Fatalf("expected repository not to be called, got %d points", len(repo.bulkPoints))
	}
}

func TestClearAllPointsDelegatesToRepository(t *testing.T) {
	repo := &fakeRepo{clearCount: 42}
	service := NewMapService(repo, nil, 0)

	cleared, err := service.ClearAllPoints(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleared != 42 {
		t.Fatalf("expected 42 cleared points, got %d", cleared)
	}
}
