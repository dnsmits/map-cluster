package domain

import "strings"

type VisualizationMode string

const (
	VisualizationModeCluster VisualizationMode = "cluster"
	VisualizationModeHeatmap VisualizationMode = "heatmap"
)

func NormalizeVisualizationMode(value string) VisualizationMode {
	switch VisualizationMode(strings.ToLower(strings.TrimSpace(value))) {
	case VisualizationModeHeatmap:
		return VisualizationModeHeatmap
	default:
		return VisualizationModeCluster
	}
}

type Viewport struct {
	BBox [4]float64 `json:"bbox" msgpack:"bbox"`
	Zoom int        `json:"zoom" msgpack:"zoom"`
}

type Point struct {
	ID         int64
	Name       string
	SymbolCode string
	Lon        float64
	Lat        float64
}

type FeatureCollection struct {
	Type     string    `json:"type" msgpack:"type"`
	Features []Feature `json:"features" msgpack:"features"`
}

type Feature struct {
	ID         string         `json:"id,omitempty" msgpack:"id,omitempty"`
	Type       string         `json:"type" msgpack:"type"`
	Geometry   Geometry       `json:"geometry" msgpack:"geometry"`
	Properties map[string]any `json:"properties" msgpack:"properties"`
}

type ViewportDelta struct {
	Type     string    `json:"type" msgpack:"type"`
	Viewport Viewport  `json:"viewport" msgpack:"viewport"`
	Added    []Feature `json:"added,omitempty" msgpack:"added,omitempty"`
	Updated  []Feature `json:"updated,omitempty" msgpack:"updated,omitempty"`
	Removed  []string  `json:"removed,omitempty" msgpack:"removed,omitempty"`
}

type Geometry struct {
	Type        string    `json:"type" msgpack:"type"`
	Coordinates []float64 `json:"coordinates" msgpack:"coordinates"`
}
