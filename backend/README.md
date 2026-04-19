# MapCluster

MapCluster is a Go + React stack for streaming clustered map data with PostGIS, Redis, websockets, and MIL-STD-2525 rendering.

## What it does

- Uses PostgreSQL with PostGIS as the source of truth
- Precomputes zoom-aware clusters into PostGIS so viewport requests avoid runtime aggregation
- Caches viewport responses in Redis to reduce repeat query latency
- Streams snapshot and delta updates over websockets as the map changes
- Renders MIL-STD-2525 symbols in the React Leaflet client with `milsymbol`, with a heatmap view built from the same viewport data
- Runs as a Docker stack

## Project Layout

- `cmd/api` contains the Go server entrypoint
- `internal/` contains config, cache, db, repository, service, and HTTP layers
- `../frontend` contains the React + Vite Leaflet client

## GeoJSON

GeoJSON is the wire format for features because it is a good fit for Leaflet and can carry rendering metadata in `properties`.
Each feature includes a stable `id` so websocket clients can apply incremental updates.

Cluster features use properties like `cluster`, `count`, `ids`, `names`, and `symbol_code`.

## Endpoints

- `GET /healthz`
- `GET /api/v1/features?bbox=minLon,minLat,maxLon,maxLat&zoom=Z&mode=cluster|heatmap`
- `POST /api/v1/populate/random-points` (inserts 1,000 random points and rebuilds clusters)
- `POST /api/v1/populate/mcdonalds` (loads the embedded McDonald's JSON dataset and rebuilds clusters)
- `POST /api/v1/populate/bulk-items` (accepts a JSON body with `items` and inserts all valid points in one batch)
- `POST /api/v1/clear-points` (clears all map points and clusters)
- `WS /ws/stream`

Example bulk payload:

```json
{
  "items": [
    {
      "name": "Alpha Team",
      "symbol_code": "SFGPUCI----K",
      "lon": -104.99,
      "lat": 39.74
    },
    {
      "name": "Bravo Team",
      "lon": -104.98,
      "lat": 39.75
    }
  ]
}
```

## Websocket protocol

Send a viewport update like this:

```json
{
  "type": "viewport",
  "bbox": [-122.6, 37.6, -122.2, 37.9],
  "zoom": 10,
  "mode": "cluster"
}
```

The first server response is a snapshot:

```json
{
  "type": "snapshot",
  "viewport": {
    "bbox": [-122.6, 37.6, -122.2, 37.9],
    "zoom": 10
  },
  "added": []
}
```

Subsequent updates are deltas:

```json
{
  "type": "delta",
  "viewport": {
    "bbox": [-122.6, 37.6, -122.2, 37.9],
    "zoom": 11
  },
  "added": [],
  "updated": [],
  "removed": []
}
```

## Run locally

```bash
docker compose up --build
```

The React frontend is served on `http://localhost:3000` and the API is available on `http://localhost:8080`.
