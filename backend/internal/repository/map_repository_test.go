package repository

import "testing"

func TestClusterZoomForCount(t *testing.T) {
	tests := []struct {
		name  string
		zoom  int
		count int
		want  int
	}{
		{name: "keeps requested zoom for sparse higher zoom viewports", zoom: 12, count: 20, want: 12},
		{name: "adds a mild zoom-out backoff on mid zooms", zoom: 10, count: 20, want: 9},
		{name: "backs off more levels for crowded viewports", zoom: 12, count: 121, want: 10},
		{name: "backs off several levels for very crowded viewports", zoom: 12, count: 450, want: 8},
		{name: "applies strong baseline backoff on low zoom", zoom: 4, count: 20, want: 1},
		{name: "clamps at world zoom", zoom: 2, count: 5000, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clusterZoomForCount(tt.zoom, tt.count); got != tt.want {
				t.Fatalf("clusterZoomForCount(%d, %d) = %d, want %d", tt.zoom, tt.count, got, tt.want)
			}
		})
	}
}
