package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

type viewportRequest struct {
	BBox [4]float64 `json:"bbox"`
	Zoom int        `json:"z"`
	Mode string     `json:"m"`
	Fmt  string     `json:"f,omitempty"`
}

func main() {
	wsURL := flag.String("url", "ws://localhost:8080/ws/stream", "websocket stream URL")
	duration := flag.Duration("duration", 10*time.Second, "benchmark duration")
	interval := flag.Duration("interval", 120*time.Millisecond, "viewport send interval")
	zoom := flag.Int("zoom", 4, "zoom level")
	mode := flag.String("mode", "c", "visualization mode: c|h")
	format := flag.String("format", "msgpack", "wire format: msgpack|json")
	bboxStr := flag.String("bbox", "-125,25,-66,49", "bbox as minLon,minLat,maxLon,maxLat")
	flag.Parse()

	bbox, err := parseBBox(*bboxStr)
	if err != nil {
		log.Fatalf("invalid bbox: %v", err)
	}

	dialer := websocket.Dialer{
		Proxy:             http.ProxyFromEnvironment,
		HandshakeTimeout:  5 * time.Second,
		EnableCompression: true,
		ReadBufferSize:    1024,
		WriteBufferSize:   1024,
	}

	conn, _, err := dialer.Dial(*wsURL, nil)
	if err != nil {
		log.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	var recvMessages int64
	var recvBytes int64
	readErr := make(chan error, 1)
	go func() {
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			atomic.AddInt64(&recvMessages, 1)
			atomic.AddInt64(&recvBytes, int64(len(payload)))
		}
	}()

	start := time.Now()
	end := start.Add(*duration)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sendCount := int64(0)
	sendPayload := viewportRequest{
		BBox: bbox,
		Zoom: *zoom,
		Mode: *mode,
	}
	if strings.EqualFold(*format, "msgpack") {
		sendPayload.Fmt = "m"
	}

	if err := writeViewport(conn, sendPayload, *format); err != nil {
		log.Fatalf("send initial viewport: %v", err)
	}
	sendCount++

	wiggle := 0.0
	for time.Now().Before(end) {
		select {
		case <-ticker.C:
			wiggle += 0.015
			sendPayload.BBox = [4]float64{
				bbox[0] + wiggle,
				bbox[1],
				bbox[2] + wiggle,
				bbox[3],
			}
			if err := writeViewport(conn, sendPayload, *format); err != nil {
				log.Fatalf("send viewport: %v", err)
			}
			sendCount++
		case err := <-readErr:
			log.Fatalf("stream closed during benchmark: %v", err)
		}
	}

	_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "benchmark complete"))
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	for {
		select {
		case <-time.After(150 * time.Millisecond):
			goto report
		case <-readErr:
			goto report
		}
	}

report:
	runtime := time.Since(start)
	receivedCount := atomic.LoadInt64(&recvMessages)
	receivedBytes := atomic.LoadInt64(&recvBytes)

	avgBytes := 0.0
	if receivedCount > 0 {
		avgBytes = float64(receivedBytes) / float64(receivedCount)
	}

	fmt.Printf("ws benchmark\n")
	fmt.Printf("url: %s\n", *wsURL)
	fmt.Printf("format: %s\n", *format)
	fmt.Printf("duration: %s\n", runtime.Round(time.Millisecond))
	fmt.Printf("sent viewport updates: %d\n", sendCount)
	fmt.Printf("received messages: %d\n", receivedCount)
	fmt.Printf("received bytes: %d\n", receivedBytes)
	fmt.Printf("avg bytes per message: %.1f\n", avgBytes)
	fmt.Printf("recv msgs/sec: %.2f\n", float64(receivedCount)/runtime.Seconds())
	fmt.Printf("recv kb/sec: %.2f\n", (float64(receivedBytes)/1024.0)/runtime.Seconds())
}

func writeViewport(conn *websocket.Conn, payload viewportRequest, format string) error {
	if strings.EqualFold(format, "msgpack") {
		message, err := msgpack.Marshal(payload)
		if err != nil {
			return err
		}
		return conn.WriteMessage(websocket.BinaryMessage, message)
	}

	message, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, message)
}

func parseBBox(raw string) ([4]float64, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return [4]float64{}, fmt.Errorf("expected 4 comma-separated values")
	}

	var bbox [4]float64
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return [4]float64{}, err
		}
		bbox[i] = value
	}
	return bbox, nil
}

func init() {
	log.SetOutput(os.Stderr)
}
