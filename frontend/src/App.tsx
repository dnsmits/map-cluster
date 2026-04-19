import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import L, { type Layer, type LayerGroup, type Map as LeafletMap } from 'leaflet';
import 'leaflet.heat';
import { MapContainer, TileLayer, useMap } from 'react-leaflet';
import { decode, encode } from '@msgpack/msgpack';
import ms from 'milsymbol';

type Viewport = {
  bbox: [number, number, number, number];
  zoom: number;
};

type Feature = {
  id?: string;
  type: 'Feature';
  geometry: {
    type: 'Point';
    coordinates: [number, number];
  };
  properties: {
    cluster?: boolean;
    count?: number;
    intensity?: number;
    name?: string;
    symbol_code?: string;
  };
};

type StreamMessage = {
  t: 's' | 'd' | 'e';
  a?: Feature[];
  u?: Feature[];
  r?: string[];
  e?: string;
};

type BasemapMode = 'street' | 'satellite';
type VisualizationMode = 'cluster' | 'heatmap';

type BasemapConfig = {
  url: string;
  attribution: string;
};

type HeatPoint = [number, number, number];
type HeatLayer = Layer & {
  setLatLngs: (latlngs: HeatPoint[]) => HeatLayer;
};

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080';
const websocketUrl = apiBaseUrl.replace(/^http/, 'ws');
const VIEWPORT_SEND_DEBOUNCE_MS = 120;
const VIEWPORT_COORD_PRECISION = 4;
const FRUSTUM_PAD = 0.25;
const MAX_SYMBOL_ICON_CACHE = 500;

const basemaps: Record<BasemapMode, BasemapConfig> = {
  street: {
    url: 'https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png',
    attribution: '&copy; OpenStreetMap contributors'
  },
  satellite: {
    url: 'https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/{z}/{y}/{x}',
    attribution: 'Tiles &copy; Esri'
  }
};

function featureKey(feature: Feature): string {
  if (feature.id) {
    return feature.id;
  }
  const [lon, lat] = feature.geometry.coordinates;
  return `${feature.properties.cluster ? 'c' : 'p'}:${lon.toFixed(6)}:${lat.toFixed(6)}`;
}

function toHeatPoint(feature: Feature): HeatPoint {
  const [lon, lat] = feature.geometry.coordinates;
  const weight = feature.properties.intensity ?? feature.properties.count ?? 1;
  const normalized = Math.min(1, 0.2 + Math.log2(weight + 1) / 6);
  return [lat, lon, normalized];
}

function createHeatLayer(points: HeatPoint[]): HeatLayer {
  const heatFactory = (
    L as unknown as {
      heatLayer: (latlngs: HeatPoint[], options?: Record<string, unknown>) => HeatLayer;
    }
  ).heatLayer;

  return heatFactory(points, {
    radius: 32,
    blur: 24,
    minOpacity: 0.3,
    maxZoom: 10,
    gradient: {
      0.2: '#58d68d',
      0.45: '#7bdff2',
      0.65: '#ffd166',
      0.85: '#ff9f1c',
      1.0: '#ff6b35'
    }
  });
}

function roundCoordinate(value: number): number {
  const scale = 10 ** VIEWPORT_COORD_PRECISION;
  return Math.round(value * scale) / scale;
}

function featureInBounds(feature: Feature, bounds: L.LatLngBounds): boolean {
  const [lon, lat] = feature.geometry.coordinates;
  return bounds.contains([lat, lon]);
}

function escapeHtml(input: string): string {
  return input
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function featurePopupHtml(feature: Feature): string {
  const [lon, lat] = feature.geometry.coordinates;
  const props = feature.properties;

  if (props.cluster) {
    const count = props.count ?? 1;
    return `<strong>Cluster</strong><br/>Count: ${count}<br/>Lat/Lon: ${lat.toFixed(4)}, ${lon.toFixed(4)}`;
  }

  const name = props.name ?? props.symbol_code ?? 'Unknown';
  return `<strong>${escapeHtml(name)}</strong><br/>Lat/Lon: ${lat.toFixed(4)}, ${lon.toFixed(4)}`;
}

function createSymbolIcon(symbolCode: string): L.DivIcon {
  const symbol = new ms.Symbol(symbolCode, { size: 20, frame: true });
  const size = symbol.getSize();
  const anchor = symbol.getAnchor();

  return L.divIcon({
    className: 'symbol-marker',
    html: symbol.asSVG(),
    iconSize: [size.width, size.height],
    iconAnchor: [anchor.x, anchor.y]
  });
}

function featureToLayer(feature: Feature, canvasRenderer: L.Renderer, iconCache: Map<string, L.DivIcon>): Layer {
  const [lon, lat] = feature.geometry.coordinates;
  const latLng: [number, number] = [lat, lon];
  const count = feature.properties.count ?? 1;

  if (feature.properties.cluster) {
    const radius = Math.max(12, Math.min(26, 10 + Math.log2(count + 1) * 4));
    return L.circleMarker(latLng, {
      renderer: canvasRenderer,
      radius,
      color: '#ffb703',
      weight: 2,
      fillColor: '#fb8500',
      fillOpacity: 0.84
    })
      .bindTooltip(`${count} symbols`, { direction: 'top', offset: [0, -8] })
      .bindPopup(featurePopupHtml(feature));
  }

  const symbolCode = feature.properties.symbol_code ?? 'SFGPUCI----K';
  let icon = iconCache.get(symbolCode);
  if (!icon) {
    if (iconCache.size >= MAX_SYMBOL_ICON_CACHE) {
      const oldestCacheKey = iconCache.keys().next().value;
      if (oldestCacheKey) {
        iconCache.delete(oldestCacheKey);
      }
    }
    icon = createSymbolIcon(symbolCode);
    iconCache.set(symbolCode, icon);
  }

  return L.marker(latLng, {
    icon
  }).bindPopup(featurePopupHtml(feature));
}

function viewportFromMap(map: LeafletMap): Viewport {
  const bounds = map.getBounds();
  return {
    bbox: [bounds.getWest(), bounds.getSouth(), bounds.getEast(), bounds.getNorth()],
    zoom: map.getZoom()
  };
}

type ViewportStreamProps = {
  visualizationMode: VisualizationMode;
  showMapItems: boolean;
  onStatusChange: (status: 'connecting' | 'connected' | 'reconnecting' | 'offline') => void;
};

type MutationAction = 'random' | 'clear' | 'mcdonalds';

function ViewportStream({ visualizationMode, showMapItems, onStatusChange }: ViewportStreamProps) {
  const map = useMap();
  const socketRef = useRef<WebSocket | null>(null);
  const layerGroupRef = useRef<LayerGroup | null>(null);
  const featureLayersRef = useRef<Map<string, Layer>>(new Map());
  const featureStoreRef = useRef<Map<string, Feature>>(new Map());
  const heatPointsRef = useRef<Map<string, HeatPoint>>(new Map());
  const heatLayerRef = useRef<HeatLayer | null>(null);
  const showMapItemsRef = useRef(showMapItems);
  const visualizationModeRef = useRef<VisualizationMode>(visualizationMode);
  const reconnectTimerRef = useRef<number | null>(null);
  const viewportSendTimerRef = useRef<number | null>(null);
  const lastViewportPayloadRef = useRef<string>('');
  const canvasRendererRef = useRef<L.Renderer | null>(null);
  const symbolIconCacheRef = useRef<Map<string, L.DivIcon>>(new Map());
  const currentBoundsRef = useRef<L.LatLngBounds | null>(null);

  const clearFeatureLayers = useMemo(() => {
    return () => {
      featureLayersRef.current.forEach((layer) => layerGroupRef.current?.removeLayer(layer));
      featureLayersRef.current.clear();
    };
  }, []);

  const removeFeatureLayer = useMemo(() => {
    return (key: string) => {
      const layer = featureLayersRef.current.get(key);
      if (!layer) {
        return;
      }
      layerGroupRef.current?.removeLayer(layer);
      featureLayersRef.current.delete(key);
    };
  }, []);

  const ensureHeatLayer = useMemo(() => {
    return () => {
      if (heatLayerRef.current) {
        return heatLayerRef.current;
      }
      const layer = createHeatLayer([]);
      heatLayerRef.current = layer;
      return layer;
    };
  }, []);

  const removeHeatLayer = useMemo(() => {
    return () => {
      if (!heatLayerRef.current || !layerGroupRef.current) {
        return;
      }
      layerGroupRef.current.removeLayer(heatLayerRef.current);
    };
  }, []);

  const rebuildHeatPoints = useMemo(() => {
    return () => {
      heatPointsRef.current.clear();
      featureStoreRef.current.forEach((feature, key) => {
        heatPointsRef.current.set(key, toHeatPoint(feature));
      });
    };
  }, []);

  const refreshHeatLayer = useMemo(() => {
    return () => {
      if (!layerGroupRef.current) {
        return;
      }

      const heatLayer = ensureHeatLayer();
      heatLayer.setLatLngs(Array.from(heatPointsRef.current.values()));

      if (showMapItemsRef.current && visualizationModeRef.current === 'heatmap') {
        layerGroupRef.current.addLayer(heatLayer);
      } else {
        layerGroupRef.current.removeLayer(heatLayer);
      }
    };
  }, [ensureHeatLayer]);

  const upsertFeatureLayer = useMemo(() => {
    return (feature: Feature) => {
      const key = featureKey(feature);
      const bounds = currentBoundsRef.current;
      const shouldShow =
        showMapItemsRef.current &&
        visualizationModeRef.current === 'cluster' &&
        !!bounds &&
        featureInBounds(feature, bounds);

      if (!shouldShow) {
        removeFeatureLayer(key);
        return;
      }

      if (!canvasRendererRef.current) {
        canvasRendererRef.current = L.canvas({ padding: FRUSTUM_PAD });
      }

      removeFeatureLayer(key);
      const layer = featureToLayer(feature, canvasRendererRef.current as L.Renderer, symbolIconCacheRef.current);
      featureLayersRef.current.set(key, layer);
      layerGroupRef.current?.addLayer(layer);
    };
  }, [removeFeatureLayer]);

  const syncVisibleLayers = useMemo(() => {
    return () => {
      const bounds = map.getBounds().pad(FRUSTUM_PAD);
      currentBoundsRef.current = bounds;

      if (!showMapItemsRef.current || visualizationModeRef.current !== 'cluster') {
        clearFeatureLayers();
        return;
      }

      featureStoreRef.current.forEach((feature, key) => {
        if (!featureInBounds(feature, bounds)) {
          removeFeatureLayer(key);
          return;
        }

        if (!featureLayersRef.current.has(key)) {
          upsertFeatureLayer(feature);
        }
      });
    };
  }, [clearFeatureLayers, map, removeFeatureLayer, upsertFeatureLayer]);

  const applySnapshot = useMemo(() => {
    return (features: Feature[]) => {
      featureStoreRef.current.clear();
      for (const feature of features) {
        featureStoreRef.current.set(featureKey(feature), feature);
      }

      if (visualizationModeRef.current === 'heatmap') {
        clearFeatureLayers();
        rebuildHeatPoints();
        refreshHeatLayer();
        return;
      }

      removeHeatLayer();
      syncVisibleLayers();
    };
  }, [clearFeatureLayers, rebuildHeatPoints, refreshHeatLayer, removeHeatLayer, syncVisibleLayers]);

  const applyDelta = useMemo(() => {
    return (message: StreamMessage) => {
      for (const feature of message.a ?? []) {
        const key = featureKey(feature);
        featureStoreRef.current.set(key, feature);

        if (visualizationModeRef.current === 'heatmap') {
          heatPointsRef.current.set(key, toHeatPoint(feature));
        } else {
          upsertFeatureLayer(feature);
        }
      }

      for (const feature of message.u ?? []) {
        const key = featureKey(feature);
        featureStoreRef.current.set(key, feature);

        if (visualizationModeRef.current === 'heatmap') {
          heatPointsRef.current.set(key, toHeatPoint(feature));
        } else {
          upsertFeatureLayer(feature);
        }
      }

      for (const id of message.r ?? []) {
        featureStoreRef.current.delete(id);

        if (visualizationModeRef.current === 'heatmap') {
          heatPointsRef.current.delete(id);
        } else {
          removeFeatureLayer(id);
        }
      }

      if (visualizationModeRef.current === 'heatmap') {
        refreshHeatLayer();
        return;
      }

      removeHeatLayer();
      syncVisibleLayers();
    };
  }, [refreshHeatLayer, removeFeatureLayer, removeHeatLayer, syncVisibleLayers, upsertFeatureLayer]);

  const decodeStreamMessage = useMemo(() => {
    return async (payload: string | ArrayBuffer | Blob): Promise<StreamMessage> => {
      if (typeof payload === 'string') {
        return JSON.parse(payload) as StreamMessage;
      }

      if (payload instanceof ArrayBuffer) {
        return decode(new Uint8Array(payload)) as StreamMessage;
      }

      return decode(new Uint8Array(await payload.arrayBuffer())) as StreamMessage;
    };
  }, []);

  const sendViewportNow = useMemo(() => {
    return (force = false) => {
      if (!socketRef.current || socketRef.current.readyState !== WebSocket.OPEN) {
        return;
      }

      const viewport = viewportFromMap(map);

      const payload = {
        bbox: [
          roundCoordinate(viewport.bbox[0]),
          roundCoordinate(viewport.bbox[1]),
          roundCoordinate(viewport.bbox[2]),
          roundCoordinate(viewport.bbox[3])
        ],
        z: viewport.zoom,
        m: visualizationModeRef.current === 'heatmap' ? 'h' : 'c',
        f: 'm'
      };
      const payloadKey = JSON.stringify(payload);

      if (!force && payloadKey === lastViewportPayloadRef.current) {
        return;
      }
      socketRef.current.send(encode(payload));
      lastViewportPayloadRef.current = payloadKey;
    };
  }, [map]);

  const scheduleViewportSend = useMemo(() => {
    return (force = false) => {
      if (viewportSendTimerRef.current) {
        window.clearTimeout(viewportSendTimerRef.current);
        viewportSendTimerRef.current = null;
      }

      if (force) {
        sendViewportNow(true);
        return;
      }

      viewportSendTimerRef.current = window.setTimeout(() => {
        viewportSendTimerRef.current = null;
        sendViewportNow(false);
      }, VIEWPORT_SEND_DEBOUNCE_MS);
    };
  }, [sendViewportNow]);

  useEffect(() => {
    showMapItemsRef.current = showMapItems;

    if (visualizationModeRef.current === 'heatmap') {
      refreshHeatLayer();
      return;
    }

    if (!showMapItems && layerGroupRef.current) {
      clearFeatureLayers();
      removeHeatLayer();
      return;
    }

    syncVisibleLayers();
  }, [clearFeatureLayers, refreshHeatLayer, removeHeatLayer, showMapItems, syncVisibleLayers]);

  useEffect(() => {
    visualizationModeRef.current = visualizationMode;
    scheduleViewportSend(true);

    if (visualizationMode === 'heatmap') {
      clearFeatureLayers();
      rebuildHeatPoints();
      refreshHeatLayer();
      return;
    }

    removeHeatLayer();
    syncVisibleLayers();
  }, [clearFeatureLayers, rebuildHeatPoints, refreshHeatLayer, removeHeatLayer, scheduleViewportSend, syncVisibleLayers, visualizationMode]);

  useEffect(() => {
    const layerGroup = L.layerGroup().addTo(map);
    layerGroupRef.current = layerGroup;
    currentBoundsRef.current = map.getBounds().pad(FRUSTUM_PAD);

    return () => {
      if (heatLayerRef.current) {
        layerGroup.removeLayer(heatLayerRef.current);
        heatLayerRef.current = null;
      }
      layerGroup.remove();
      layerGroupRef.current = null;
      featureLayersRef.current.clear();
      featureStoreRef.current.clear();
      heatPointsRef.current.clear();
    };
  }, [map]);

  useEffect(() => {
    const connect = () => {
      onStatusChange('connecting');
      const socket = new WebSocket(`${websocketUrl}/ws/stream`);
      socket.binaryType = 'arraybuffer';
      socketRef.current = socket;

      socket.onopen = () => {
        onStatusChange('connected');
        scheduleViewportSend(true);
      };

      socket.onmessage = (event) => {
        void decodeStreamMessage(event.data as string | ArrayBuffer | Blob)
          .then((message) => {
            if (message.t === 'e') {
              onStatusChange('offline');
              return;
            }

            if (message.t === 's') {
              applySnapshot(message.a ?? []);
              return;
            }

            if (message.t === 'd') {
              applyDelta(message);
            }
          })
          .catch(() => {
            onStatusChange('offline');
          });
      };

      socket.onclose = () => {
        onStatusChange('reconnecting');
        if (reconnectTimerRef.current) {
          window.clearTimeout(reconnectTimerRef.current);
        }
        reconnectTimerRef.current = window.setTimeout(connect, 800);
      };

      socket.onerror = () => {
        socket.close();
      };
    };

    connect();

    return () => {
      if (reconnectTimerRef.current) {
        window.clearTimeout(reconnectTimerRef.current);
      }
      socketRef.current?.close();
    };
  }, [applyDelta, applySnapshot, decodeStreamMessage, map, onStatusChange, scheduleViewportSend]);

  useEffect(() => {
    const refresh = () => {
      syncVisibleLayers();
      scheduleViewportSend(false);
    };
    map.on('moveend', refresh);
    map.on('zoomend', refresh);

    return () => {
      map.off('moveend', refresh);
      map.off('zoomend', refresh);
    };
  }, [map, scheduleViewportSend, syncVisibleLayers]);

  useEffect(() => {
    return () => {
      if (viewportSendTimerRef.current) {
        window.clearTimeout(viewportSendTimerRef.current);
      }
    };
  }, []);

  return null;
}

type MapRefBridgeProps = {
  onMapReady: (map: LeafletMap) => void;
};

function MapRefBridge({ onMapReady }: MapRefBridgeProps) {
  const map = useMap();

  useEffect(() => {
    onMapReady(map);
  }, [map, onMapReady]);

  return null;
}

export function App() {
  const [basemapMode, setBasemapMode] = useState<BasemapMode>('street');
  const [visualizationMode, setVisualizationMode] = useState<VisualizationMode>('cluster');
  const [showMapItems, setShowMapItems] = useState(true);
  const [status, setStatus] = useState<'connecting' | 'connected' | 'reconnecting' | 'offline'>('connecting');
  const [randomPointCountInput, setRandomPointCountInput] = useState('500');
  const [mutationAction, setMutationAction] = useState<MutationAction | null>(null);
  const [actionMessage, setActionMessage] = useState('');
  const mapRef = useRef<LeafletMap | null>(null);

  const setMapRef = useCallback((map: LeafletMap) => {
    mapRef.current = map;
  }, []);

  const postMutation = useCallback(async (path: string, payload?: unknown) => {
    const response = await fetch(`${apiBaseUrl}${path}`, {
      method: 'POST',
      headers: payload ? { 'Content-Type': 'application/json' } : undefined,
      body: payload ? JSON.stringify(payload) : undefined
    });

    const result = (await response.json().catch(() => ({}))) as {
      inserted?: number;
      cleared?: number;
      error?: string;
    };

    if (!response.ok) {
      throw new Error(result.error ?? `Request failed with status ${response.status}`);
    }

    return result;
  }, []);

  const onPopulateRandomInViewport = useCallback(async () => {
    const map = mapRef.current;
    if (!map) {
      setActionMessage('Map is not ready yet.');
      return;
    }

    if (randomPointCountInput.trim() === '') {
      setActionMessage('Enter a random point count greater than zero.');
      return;
    }

    const parsedCount = Number.parseInt(randomPointCountInput, 10);
    if (!Number.isFinite(parsedCount) || parsedCount <= 0) {
      setActionMessage('Enter a valid random point count greater than zero.');
      return;
    }
    const count = Math.max(1, Math.min(50000, parsedCount));
    if (count !== parsedCount) {
      setRandomPointCountInput(String(count));
    }

    setMutationAction('random');
    setActionMessage('');

    try {
      const viewport = viewportFromMap(map);
      const result = await postMutation('/api/v1/populate/random-points', {
        count,
        bbox: viewport.bbox,
        zoom: viewport.zoom
      });
      setActionMessage(`Added ${result.inserted ?? 0} random points in viewport.`);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to add random points.';
      setActionMessage(message);
    } finally {
      setMutationAction(null);
    }
  }, [postMutation, randomPointCountInput]);

  const onClearAllData = useCallback(async () => {
    setMutationAction('clear');
    setActionMessage('');

    try {
      const result = await postMutation('/api/v1/clear-points');
      setActionMessage(`Cleared ${result.cleared ?? 0} points.`);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to clear data.';
      setActionMessage(message);
    } finally {
      setMutationAction(null);
    }
  }, [postMutation]);

  const onAddMcDonalds = useCallback(async () => {
    setMutationAction('mcdonalds');
    setActionMessage('');

    try {
      const result = await postMutation('/api/v1/populate/mcdonalds');
      setActionMessage(`Added ${result.inserted ?? 0} McDonald's points.`);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to add McDonald\'s points.';
      setActionMessage(message);
    } finally {
      setMutationAction(null);
    }
  }, [postMutation]);

  return (
    <div className="app-shell">
      <section className="settings-rail" aria-label="Map settings">
        <div className="settings-rail__title">Map</div>

        <button
          type="button"
          className={basemapMode === 'satellite' ? 'rail-toggle is-active' : 'rail-toggle'}
          aria-pressed={basemapMode === 'satellite'}
          aria-label={`Switch to ${basemapMode === 'street' ? 'satellite' : 'street'} basemap`}
          onClick={() => setBasemapMode(basemapMode === 'street' ? 'satellite' : 'street')}
        >
          <span className="rail-toggle__k">Base</span>
          <span className="rail-toggle__v">{basemapMode === 'street' ? 'Street' : 'Sat'}</span>
        </button>

        <button
          type="button"
          className={visualizationMode === 'heatmap' ? 'rail-toggle is-active-warm' : 'rail-toggle'}
          aria-pressed={visualizationMode === 'heatmap'}
          aria-label={`Switch to ${visualizationMode === 'cluster' ? 'heatmap' : 'cluster'} visualization`}
          onClick={() => setVisualizationMode(visualizationMode === 'cluster' ? 'heatmap' : 'cluster')}
        >
          <span className="rail-toggle__k">View</span>
          <span className="rail-toggle__v">{visualizationMode === 'cluster' ? 'Cluster' : 'Heat'}</span>
        </button>

        <button
          type="button"
          className={showMapItems ? 'rail-toggle is-active-warm' : 'rail-toggle'}
          aria-pressed={showMapItems}
          aria-label={`${showMapItems ? 'Hide' : 'Show'} rendered map items`}
          onClick={() => setShowMapItems(!showMapItems)}
        >
          <span className="rail-toggle__k">Items</span>
          <span className="rail-toggle__v">{showMapItems ? 'On' : 'Off'}</span>
        </button>

        <div className="random-action-group">
          <label className="rail-input" htmlFor="random-point-count">
            <span className="rail-input__k">Random N</span>
            <input
              id="random-point-count"
              type="number"
              inputMode="numeric"
              min={1}
              max={50000}
              value={randomPointCountInput}
              onChange={(event) => {
                const nextValue = event.target.value.trim();
                if (nextValue === '') {
                  setRandomPointCountInput('');
                  return;
                }

                const parsed = Number.parseInt(nextValue, 10);
                if (!Number.isFinite(parsed)) {
                  return;
                }

                setRandomPointCountInput(String(Math.max(1, Math.min(50000, parsed))));
              }}
              onBlur={() => {
                if (randomPointCountInput.trim() === '') {
                  setRandomPointCountInput('500');
                }
              }}
            />
          </label>

          <button
            type="button"
            className="rail-action rail-action-strong"
            onClick={() => void onPopulateRandomInViewport()}
            disabled={mutationAction !== null}
          >
            {mutationAction === 'random' ? 'Adding...' : 'Random in view'}
          </button>
        </div>

        <button
          type="button"
          className="rail-action"
          onClick={() => void onAddMcDonalds()}
          disabled={mutationAction !== null}
        >
          {mutationAction === 'mcdonalds' ? 'Adding...' : 'Add McDonalds'}
        </button>

        <button
          type="button"
          className="rail-action rail-action-danger"
          onClick={() => void onClearAllData()}
          disabled={mutationAction !== null}
        >
          {mutationAction === 'clear' ? 'Clearing...' : 'Clear All Data'}
        </button>

        <div className={`status-pill status-${status}`}>{status}</div>
        {actionMessage ? <div className="action-pill">{actionMessage}</div> : null}
      </section>

      <section className="map-column">
        <MapContainer center={[39.5, -98.35]} zoom={4} zoomControl={true} className="map-root">
          <TileLayer key={basemapMode} attribution={basemaps[basemapMode].attribution} url={basemaps[basemapMode].url} />
          <MapRefBridge onMapReady={setMapRef} />
          <ViewportStream
            visualizationMode={visualizationMode}
            showMapItems={showMapItems}
            onStatusChange={setStatus}
          />
        </MapContainer>
      </section>
    </div>
  );
}
