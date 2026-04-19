package httpapi

import (
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"

	"mapcluster/internal/domain"
	"mapcluster/internal/service"
)

type Handler struct {
	service  *service.MapService
	upgrader websocket.Upgrader
}

const viewportCoalesceWindow = 45 * time.Millisecond

func NewHandler(service *service.MapService) *Handler {
	return &Handler{
		service: service,
		upgrader: websocket.Upgrader{
			CheckOrigin:       func(r *http.Request) bool { return true },
			ReadBufferSize:    1024,
			WriteBufferSize:   1024,
			EnableCompression: true,
		},
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.notFound)
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/api/v1/features", h.features)
	mux.HandleFunc("/api/v1/populate/random-points", h.populateRandomPoints)
	mux.HandleFunc("/api/v1/populate/mcdonalds", h.populateMcDonalds)
	mux.HandleFunc("/api/v1/populate/bulk-items", h.populateBulkItems)
	mux.HandleFunc("/api/v1/clear-points", h.clearAllPoints)
	mux.HandleFunc("/ws/stream", h.stream)
	return withCORS(withGzip(mux))
}

func (h *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) features(w http.ResponseWriter, r *http.Request) {
	viewport, err := viewportFromRequest(r)
	if err != nil {
		handleError(w, http.StatusBadRequest, err)
		return
	}

	features, err := h.service.ViewportFeatures(r.Context(), viewport, viewportModeFromRequest(r))
	if err != nil {
		handleError(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(domain.FeatureCollection{
		Type:     "FeatureCollection",
		Features: features,
	}); err != nil {
		return
	}
}

func (h *Handler) populateRandomPoints(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var request populateRandomPointsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		handleError(w, http.StatusBadRequest, errors.New("invalid random points payload"))
		return
	}

	count := 1000
	if request.Count != nil {
		count = *request.Count
	}
	if count <= 0 {
		handleError(w, http.StatusBadRequest, errors.New("count must be greater than zero"))
		return
	}

	viewport := domain.Viewport{}
	if len(request.BBox) > 0 {
		if len(request.BBox) != 4 {
			handleError(w, http.StatusBadRequest, errors.New("bbox must contain 4 values"))
			return
		}
		copy(viewport.BBox[:], request.BBox)
	}
	if request.Zoom != nil {
		viewport.Zoom = *request.Zoom
	}

	inserted, err := h.service.PopulateRandomPoints(r.Context(), count, viewport)
	if err != nil {
		handleError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"inserted": inserted,
	})
}

func (h *Handler) clearAllPoints(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	cleared, err := h.service.ClearAllPoints(r.Context())
	if err != nil {
		handleError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"cleared": cleared,
	})
}

func (h *Handler) populateMcDonalds(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	inserted, err := h.service.PopulateMcDonaldsPoints(r.Context())
	if err != nil {
		handleError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"inserted": inserted,
		"source":   "embedded-json",
	})
}

func (h *Handler) populateBulkItems(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var request bulkItemsRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		handleError(w, http.StatusBadRequest, errors.New("invalid bulk items payload"))
		return
	}

	points := make([]domain.Point, 0, len(request.Items))
	for _, item := range request.Items {
		points = append(points, domain.Point{
			Name:       item.Name,
			SymbolCode: item.SymbolCode,
			Lon:        item.Lon,
			Lat:        item.Lat,
		})
	}

	inserted, err := h.service.PopulateBulkPoints(r.Context(), points)
	if err != nil {
		handleError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"inserted": inserted,
	})
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	conn.EnableWriteCompression(true)
	if err := conn.SetCompressionLevel(flate.BestSpeed); err != nil {
		conn.EnableWriteCompression(false)
	}

	ctx := r.Context()
	updates, unsubscribe := h.service.SubscribeUpdates()
	defer unsubscribe()

	incoming := make(chan streamRequest, 1)
	readErr := make(chan error, 1)
	go func() {
		for {
			messageType, payload, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}

			message, err := decodeStreamRequest(messageType, payload)
			if err != nil {
				continue
			}
			incoming <- message
		}
	}()

	var (
		currentViewport domain.Viewport
		currentMode     domain.VisualizationMode
		hasViewport     bool
		lastFeatures    = map[string]domain.Feature{}
		lastSignatures  = map[string]string{}
		responseFormat  = streamFormatJSON
		coalesceTimer   *time.Timer
		coalesceTimerCh <-chan time.Time
		pendingEmit     bool
		pendingInitial  bool
	)

	defer func() {
		if coalesceTimer != nil {
			coalesceTimer.Stop()
		}
	}()

	sendViewport := func(initial bool, format streamFormat) error {
		features, err := h.service.ViewportFeatures(ctx, currentViewport, currentMode)
		if err != nil {
			return err
		}

		currentFeatures := make(map[string]domain.Feature, len(features))
		currentSignatures := make(map[string]string, len(features))
		for _, feature := range features {
			currentFeatures[feature.ID] = feature
			currentSignatures[feature.ID] = featureSignature(feature)
		}

		if initial {
			response := streamResponse{
				Type:  "s",
				Added: features,
			}
			if err := writeStreamResponse(conn, response, format); err != nil {
				return err
			}
			lastFeatures = currentFeatures
			lastSignatures = currentSignatures
			return nil
		}

		added := make([]domain.Feature, 0)
		updated := make([]domain.Feature, 0)
		removed := make([]string, 0)

		for id, feature := range currentFeatures {
			previousSignature, ok := lastSignatures[id]
			if !ok {
				added = append(added, feature)
				continue
			}
			if previousSignature != currentSignatures[id] {
				updated = append(updated, feature)
			}
		}

		for id := range lastFeatures {
			if _, ok := currentFeatures[id]; !ok {
				removed = append(removed, id)
			}
		}

		response := streamResponse{
			Type:    "d",
			Added:   added,
			Updated: updated,
			Removed: removed,
		}
		if err := writeStreamResponse(conn, response, format); err != nil {
			return err
		}
		lastFeatures = currentFeatures
		lastSignatures = currentSignatures
		return nil
	}

	scheduleEmit := func(initial bool) {
		pendingEmit = true
		if initial {
			pendingInitial = true
		}

		if coalesceTimer == nil {
			coalesceTimer = time.NewTimer(viewportCoalesceWindow)
			coalesceTimerCh = coalesceTimer.C
			return
		}

		if !coalesceTimer.Stop() {
			select {
			case <-coalesceTimer.C:
			default:
			}
		}
		coalesceTimer.Reset(viewportCoalesceWindow)
	}

	for {
		select {
		case message := <-incoming:
			viewport := message.Viewport
			if len(message.BBox) == 4 {
				copy(viewport.BBox[:], message.BBox)
			}
			if message.ShortZoom != nil {
				viewport.Zoom = *message.ShortZoom
			}
			mode := message.Mode
			if mode == "" {
				mode = message.ShortMode
			}
			if viewport.Zoom == 0 && message.Zoom != nil {
				viewport.Zoom = *message.Zoom
			}
			if mode == "" {
				mode = string(currentMode)
			}
			if mode == "h" {
				mode = string(domain.VisualizationModeHeatmap)
			}
			if mode == "c" {
				mode = string(domain.VisualizationModeCluster)
			}

			if message.ShortFormat == string(streamFormatMsgpack) || message.Format == "msgpack" {
				responseFormat = streamFormatMsgpack
			}

			nextMode := domain.NormalizeVisualizationMode(mode)

			initialSnapshot := !hasViewport || nextMode != currentMode
			hasViewport = true
			currentViewport = viewport
			currentMode = nextMode
			scheduleEmit(initialSnapshot)

		case <-updates:
			if !hasViewport {
				continue
			}
			scheduleEmit(false)

		case <-coalesceTimerCh:
			if !hasViewport || !pendingEmit {
				continue
			}

			if err := sendViewport(pendingInitial, responseFormat); err != nil {
				return
			}
			pendingEmit = false
			pendingInitial = false

		case err := <-readErr:
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

type streamRequest struct {
	Type        string          `json:"type" msgpack:"type"`
	Viewport    domain.Viewport `json:"viewport" msgpack:"viewport"`
	BBox        []float64       `json:"bbox" msgpack:"bbox"`
	Zoom        *int            `json:"zoom" msgpack:"zoom"`
	Mode        string          `json:"mode" msgpack:"mode"`
	ShortZoom   *int            `json:"z" msgpack:"z"`
	ShortMode   string          `json:"m" msgpack:"m"`
	ShortFormat string          `json:"f" msgpack:"f"`
	Format      string          `json:"format" msgpack:"format"`
}

type streamResponse struct {
	Type    string           `json:"t" msgpack:"t"`
	Added   []domain.Feature `json:"a,omitempty" msgpack:"a,omitempty"`
	Updated []domain.Feature `json:"u,omitempty" msgpack:"u,omitempty"`
	Removed []string         `json:"r,omitempty" msgpack:"r,omitempty"`
	Error   string           `json:"e,omitempty" msgpack:"e,omitempty"`
}

type streamFormat string

const (
	streamFormatJSON    streamFormat = "j"
	streamFormatMsgpack streamFormat = "m"
)

func decodeStreamRequest(messageType int, payload []byte) (streamRequest, error) {
	var message streamRequest
	if messageType == websocket.BinaryMessage {
		if err := msgpack.Unmarshal(payload, &message); err != nil {
			return streamRequest{}, err
		}
		return message, nil
	}

	if err := json.Unmarshal(payload, &message); err != nil {
		return streamRequest{}, err
	}

	return message, nil
}

func writeStreamResponse(conn *websocket.Conn, response streamResponse, format streamFormat) error {
	if format == streamFormatMsgpack {
		payload, err := msgpack.Marshal(response)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.BinaryMessage, payload)
	}

	return conn.WriteJSON(response)
}

func featureSignature(feature domain.Feature) string {
	coordinates := feature.Geometry.Coordinates
	if len(coordinates) < 2 {
		return feature.ID
	}

	cluster, _ := feature.Properties["cluster"].(bool)
	count, _ := feature.Properties["count"].(int)
	if count == 0 {
		if countFloat, ok := feature.Properties["count"].(float64); ok {
			count = int(countFloat)
		}
	}
	intensity, _ := feature.Properties["intensity"].(int)
	if intensity == 0 {
		if intensityFloat, ok := feature.Properties["intensity"].(float64); ok {
			intensity = int(intensityFloat)
		}
	}
	symbolCode, _ := feature.Properties["symbol_code"].(string)
	name, _ := feature.Properties["name"].(string)

	return strings.Join([]string{
		feature.ID,
		strconv.FormatFloat(coordinates[0], 'f', 6, 64),
		strconv.FormatFloat(coordinates[1], 'f', 6, 64),
		strconv.FormatBool(cluster),
		strconv.Itoa(count),
		strconv.Itoa(intensity),
		symbolCode,
		name,
	}, ":")
}

type bulkItemsRequest struct {
	Items []bulkItemRequest `json:"items"`
}

type populateRandomPointsRequest struct {
	Count *int      `json:"count"`
	BBox  []float64 `json:"bbox"`
	Zoom  *int      `json:"zoom"`
}

type bulkItemRequest struct {
	Name       string  `json:"name"`
	SymbolCode string  `json:"symbolCode"`
	Lon        float64 `json:"lon"`
	Lat        float64 `json:"lat"`
}

func (item *bulkItemRequest) UnmarshalJSON(data []byte) error {
	type alias bulkItemRequest
	var payload struct {
		alias
		SymbolCodeAlt string `json:"symbol_code"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	*item = bulkItemRequest(payload.alias)
	if item.SymbolCode == "" {
		item.SymbolCode = payload.SymbolCodeAlt
	}
	return nil
}

func viewportFromRequest(r *http.Request) (domain.Viewport, error) {
	viewport := domain.Viewport{}
	if zoomStr := r.URL.Query().Get("zoom"); zoomStr != "" {
		zoom, err := strconv.Atoi(zoomStr)
		if err != nil {
			return viewport, errors.New("invalid zoom")
		}
		viewport.Zoom = zoom
	}

	bboxStr := r.URL.Query().Get("bbox")
	if bboxStr == "" {
		return viewport, nil
	}

	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		return viewport, errors.New("bbox must contain 4 comma-separated values")
	}
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return viewport, errors.New("bbox must be numeric")
		}
		viewport.BBox[i] = value
	}
	return viewport, nil
}

func viewportModeFromRequest(r *http.Request) domain.VisualizationMode {
	return domain.NormalizeVisualizationMode(r.URL.Query().Get("mode"))
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}

	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func handleError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		defer gz.Close()

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")

		wrapped := &gzipResponseWriter{ResponseWriter: w, writer: gz}
		next.ServeHTTP(wrapped, r)
	})
}

func isWebSocketUpgrade(r *http.Request) bool {
	connectionHeader := strings.ToLower(r.Header.Get("Connection"))
	upgradeHeader := strings.ToLower(r.Header.Get("Upgrade"))
	return strings.Contains(connectionHeader, "upgrade") && upgradeHeader == "websocket"
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.writer.Write(b)
}
