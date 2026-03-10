package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Device represents a registered TRMNL device.
type Device struct {
	MAC            string    `json:"mac"`
	APIKey         string    `json:"api_key"`
	FriendlyID     string    `json:"friendly_id"`
	CreatedAt      time.Time `json:"created_at"`
	LastSeen       time.Time `json:"last_seen,omitempty"`
	BatteryVoltage string    `json:"battery_voltage,omitempty"`
	RSSI           string    `json:"rssi,omitempty"`
	FWVersion      string    `json:"fw_version,omitempty"`
}

// registry holds all registered devices keyed by MAC address.
// A separate reverse map (api_key → Device) is rebuilt on load and kept in sync.
var (
	registryMu  sync.RWMutex
	byMAC       = make(map[string]*Device)
	byKey       = make(map[string]*Device)
	compileMu   sync.Mutex
	devicesPath string
)

// -- registry persistence --

func loadRegistry(path string) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		log.Fatalf("[registry] read %s: %v", path, err)
	}
	var devices []*Device
	if err := json.Unmarshal(data, &devices); err != nil {
		log.Fatalf("[registry] parse %s: %v", path, err)
	}
	for _, d := range devices {
		byMAC[d.MAC] = d
		byKey[d.APIKey] = d
	}
	log.Printf("[registry] loaded %d device(s) from %s", len(devices), path)
}

func saveRegistry() {
	devices := make([]*Device, 0, len(byMAC))
	for _, d := range byMAC {
		devices = append(devices, d)
	}
	data, _ := json.MarshalIndent(devices, "", "  ")
	if err := os.WriteFile(devicesPath, data, 0644); err != nil {
		log.Printf("[registry] save error: %v", err)
	}
}

// -- helpers --

const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func compile(src, out string) error {
	compileMu.Lock()
	defer compileMu.Unlock()
	cmd := exec.Command("typst", "compile", "--format", "png", "--ppi", "227", src, out)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// -- handlers --

func handleSetup(w http.ResponseWriter, r *http.Request, baseURL string) {
	mac := strings.ToLower(r.Header.Get("ID"))
	if mac == "" {
		http.Error(w, "missing ID header", http.StatusBadRequest)
		return
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if d, ok := byMAC[mac]; ok {
		log.Printf("[setup] known device mac=%s friendly_id=%s", mac, d.FriendlyID)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      200,
			"api_key":     d.APIKey,
			"friendly_id": d.FriendlyID,
			"image_url":   baseURL + "/dashboard.png",
			"filename":    "welcome",
		})
		return
	}

	d := &Device{
		MAC:        mac,
		APIKey:     randString(32),
		FriendlyID: randString(6),
		CreatedAt:  time.Now(),
	}
	byMAC[mac] = d
	byKey[d.APIKey] = d
	saveRegistry()
	log.Printf("[setup] registered device mac=%s friendly_id=%s", mac, d.FriendlyID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      200,
		"api_key":     d.APIKey,
		"friendly_id": d.FriendlyID,
		"image_url":   baseURL + "/dashboard.png",
		"filename":    "welcome",
	})
}

func handleDisplay(w http.ResponseWriter, r *http.Request, baseURL string, refreshRate int, typstSrc, pngOut string) {
	token := r.Header.Get("Access-Token")

	registryMu.Lock()
	d, ok := byKey[token]
	if ok {
		d.LastSeen = time.Now()
		d.BatteryVoltage = r.Header.Get("Battery-Voltage")
		d.RSSI = r.Header.Get("RSSI")
		d.FWVersion = r.Header.Get("FW-Version")
		saveRegistry()
	}
	registryMu.Unlock()

	if !ok {
		log.Printf("[display] unknown token, returning 202")
		writeJSON(w, http.StatusOK, map[string]any{"status": 202})
		return
	}

	log.Printf("[display] device=%s battery=%s rssi=%s fw=%s",
		d.FriendlyID,
		r.Header.Get("Battery-Voltage"),
		r.Header.Get("RSSI"),
		r.Header.Get("FW-Version"),
	)

	if err := compile(typstSrc, pngOut); err != nil {
		log.Printf("[display] compile error: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": 500})
		return
	}

	imageURL := fmt.Sprintf("%s/dashboard.png?t=%d", baseURL, time.Now().Unix())
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          0,
		"image_url":       imageURL,
		"filename":        fmt.Sprintf("dashboard-%s", time.Now().Format("2006-01-02T15:04:05")),
		"refresh_rate":    refreshRate,
		"update_firmware": false,
		"reset_firmware":  false,
	})
}

func handleLog(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("ID")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err == nil && len(body) > 0 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") == nil {
			log.Printf("[log] device=%s\n%s", deviceID, pretty.String())
		} else {
			log.Printf("[log] device=%s %s", deviceID, body)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// -- main --

func main() {
	addr        := flag.String("addr", ":8080", "Listen address")
	baseURL     := flag.String("base-url", "", "Public base URL the device will use to fetch the image (e.g. http://192.168.1.100:8080)")
	refreshRate := flag.Int("refresh-rate", 1800, "Seconds between device polls")
	typstSrc    := flag.String("src", "dashboard.typ", "Typst source file")
	pngOut      := flag.String("out", "dashboard.png", "PNG output path")
	dp          := flag.String("devices", "devices.json", "Device registry file")
	flag.Parse()

	if *baseURL == "" {
		log.Fatal("--base-url is required (e.g. http://192.168.1.100:8080)")
	}
	*baseURL = strings.TrimRight(*baseURL, "/")
	devicesPath = *dp

	rand.Seed(time.Now().UnixNano()) //nolint:staticcheck
	loadRegistry(devicesPath)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup", func(w http.ResponseWriter, r *http.Request) {
		handleSetup(w, r, *baseURL)
	})
	mux.HandleFunc("GET /api/display", func(w http.ResponseWriter, r *http.Request) {
		handleDisplay(w, r, *baseURL, *refreshRate, *typstSrc, *pngOut)
	})
	mux.HandleFunc("POST /api/log", handleLog)
	mux.HandleFunc("GET /dashboard.png", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, *pngOut)
	})

	log.Printf("TRMNL BYOS server")
	log.Printf("  addr:          %s", *addr)
	log.Printf("  base-url:      %s", *baseURL)
	log.Printf("  refresh-rate:  %ds", *refreshRate)
	log.Printf("  devices file:  %s", devicesPath)
	log.Printf("  devices loaded: %d", len(byMAC))

	log.Fatal(http.ListenAndServe(*addr, mux))
}
