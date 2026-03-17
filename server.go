package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
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

var (
	devMode  = os.Getenv("DEV") != ""
	debugMode = os.Getenv("DEBUG") != ""
)

var (
	registryMu  sync.RWMutex
	byMAC       = make(map[string]*Device)
	byKey       = make(map[string]*Device)
	renderMu    sync.Mutex
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

// -- weather --

type weatherResponse struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Hourly struct {
		Temperature       []float64 `json:"temperature_2m"`
		WindSpeed         []float64 `json:"wind_speed_10m"`
		PrecipProbability []float64 `json:"precipitation_probability"`
	} `json:"hourly"`
	Daily struct {
		TempMax []float64 `json:"temperature_2m_max"`
		TempMin []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

type weatherData struct {
	Temp         int
	TempMin      int
	TempMax      int
	WindMPH      int
	Condition    string
	HourlyTemp   []float64
	HourlyWind   []float64
	HourlyPrecip []float64
	FetchedAt    time.Time
}

var (
	weatherMu    sync.Mutex
	weatherCache *weatherData
)

func fetchWeather(lat, lon float64) (*weatherData, error) {
	weatherMu.Lock()
	defer weatherMu.Unlock()

	if weatherCache != nil && time.Since(weatherCache.FetchedAt) < 15*time.Minute {
		return weatherCache, nil
	}

	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&current=temperature_2m,weather_code,wind_speed_10m"+
			"&hourly=temperature_2m,wind_speed_10m,precipitation_probability"+
			"&daily=temperature_2m_max,temperature_2m_min"+
			"&wind_speed_unit=mph"+
			"&forecast_days=1&timezone=auto",
		lat, lon,
	)

	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("weather fetch: %w", err)
	}
	defer resp.Body.Close()

	var wr weatherResponse
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("weather decode: %w", err)
	}

	cap24 := func(s []float64) []float64 {
		if len(s) > 24 {
			return s[:24]
		}
		return s
	}

	wd := &weatherData{
		Temp:         int(math.Round(wr.Current.Temperature)),
		WindMPH:      int(math.Round(wr.Current.WindSpeed)),
		Condition:    wmoCondition(wr.Current.WeatherCode),
		HourlyTemp:   cap24(wr.Hourly.Temperature),
		HourlyWind:   cap24(wr.Hourly.WindSpeed),
		HourlyPrecip: cap24(wr.Hourly.PrecipProbability),
		FetchedAt:    time.Now(),
	}
	if len(wr.Daily.TempMax) > 0 {
		wd.TempMax = int(math.Round(wr.Daily.TempMax[0]))
	}
	if len(wr.Daily.TempMin) > 0 {
		wd.TempMin = int(math.Round(wr.Daily.TempMin[0]))
	}

	log.Printf("[weather] %d°C, %s, wind %d mph (high %d° low %d°)",
		wd.Temp, wd.Condition, wd.WindMPH, wd.TempMax, wd.TempMin)
	weatherCache = wd
	return wd, nil
}

func wmoCondition(code int) string {
	switch {
	case code == 0:
		return "Clear sky"
	case code == 1:
		return "Mainly clear"
	case code == 2:
		return "Partly cloudy"
	case code == 3:
		return "Overcast"
	case code <= 48:
		return "Fog"
	case code <= 55:
		return "Drizzle"
	case code <= 57:
		return "Freezing drizzle"
	case code <= 65:
		return "Rain"
	case code <= 67:
		return "Freezing rain"
	case code <= 75:
		return "Snow"
	case code == 77:
		return "Snow grains"
	case code <= 82:
		return "Showers"
	case code <= 86:
		return "Snow showers"
	default:
		return "Thunderstorm"
	}
}

// -- dashboard template data --
//
// Go's job: fetch data, serialise to JSON, hand off to the template.
// All chart rendering logic lives in dashboard.html / JS.

// dashJSON is the complete data contract passed to the JS layer.
// Go's only job: populate this struct and serialise it.
type dashJSON struct {
	Temp         int       `json:"temp"`
	TempMin      int       `json:"tempMin"`
	TempMax      int       `json:"tempMax"`
	WindMPH      int       `json:"windMPH"`
	Condition    string    `json:"condition"`
	Debug        bool      `json:"debug"`
	Headless     bool      `json:"headless"`
	HourlyTemp   []float64 `json:"hourlyTemp"`
	HourlyWind   []float64 `json:"hourlyWind"`
	HourlyPrecip []float64 `json:"hourlyPrecip"`
}

// dashboardData is the Go template context — a single JSON blob, nothing else.
type dashboardData struct {
	DataJSON template.JS
}

func buildDashboard(wd *weatherData) dashboardData {
	d := dashJSON{
		Temp:         wd.Temp,
		TempMin:      wd.TempMin,
		TempMax:      wd.TempMax,
		WindMPH:      wd.WindMPH,
		Condition:    wd.Condition,
		Debug:        debugMode,
		Headless:     true,
		HourlyTemp:   wd.HourlyTemp,
		HourlyWind:   wd.HourlyWind,
		HourlyPrecip: wd.HourlyPrecip,
	}
	jsonBytes, _ := json.Marshal(d)
	return dashboardData{DataJSON: template.JS(jsonBytes)}
}

// -- Chrome rendering --

func findChrome() (string, error) {
	if v := os.Getenv("CHROME_BIN"); v != "" {
		return v, nil
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no Chrome/Chromium found; install Chrome or set CHROME_BIN")
}

// chromeCtx is a persistent browser context kept alive for the life of the process.
// Chrome starts on first render and is reused for all subsequent renders, avoiding
// the ~1-2s cold-start penalty on every re-render.
var (
	chromeOnce   sync.Once
	chromeCtx    context.Context
	chromeCancel context.CancelFunc
	chromeErr    error
)

func initChrome() error {
	chromeOnce.Do(func() {
		chrome, err := findChrome()
		if err != nil {
			chromeErr = err
			return
		}
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chrome),
			chromedp.Flag("hide-scrollbars", true),
			chromedp.Flag("force-device-scale-factor", "1"),
			chromedp.WindowSize(1872, 1404),
		)
		allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
		ctx, cancel := chromedp.NewContext(allocCtx)
		chromeCtx = ctx
		chromeCancel = func() { cancel(); allocCancel() }
	})
	return chromeErr
}

func renderHTML(tmplPath, pngOut string, data dashboardData) error {
	renderMu.Lock()
	defer renderMu.Unlock()

	if err := initChrome(); err != nil {
		return err
	}

	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	tmp, err := os.CreateTemp("", "trmnl-*.html")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmpl.Execute(tmp, data); err != nil {
		tmp.Close()
		return fmt.Errorf("execute template: %w", err)
	}
	tmp.Close()

	absHTML, _ := filepath.Abs(tmpPath)
	absPNG, _ := filepath.Abs(pngOut)

	var buf []byte
	if err := chromedp.Run(chromeCtx,
		chromedp.EmulateViewport(1872, 1404),
		chromedp.Navigate("file://"+absHTML),
		chromedp.CaptureScreenshot(&buf),
	); err != nil {
		return fmt.Errorf("chromedp: %w", err)
	}
	return os.WriteFile(absPNG, buf, 0644)
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

func envFloat(key string) float64 {
	v := os.Getenv(key)
	if v == "" {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Printf("warn: invalid %s=%q: %v", key, v, err)
		return math.NaN()
	}
	return f
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// -- handlers --

func handleSetup(w http.ResponseWriter, r *http.Request) {
	mac := strings.ToLower(r.Header.Get("ID"))
	if mac == "" {
		http.Error(w, "missing ID header", http.StatusBadRequest)
		return
	}

	base := baseURL(r)

	registryMu.Lock()
	defer registryMu.Unlock()

	if d, ok := byMAC[mac]; ok {
		log.Printf("[setup] known device mac=%s friendly_id=%s", mac, d.FriendlyID)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      200,
			"api_key":     d.APIKey,
			"friendly_id": d.FriendlyID,
			"image_url":   base + "/dashboard.png",
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
		"image_url":   base + "/dashboard.png",
		"filename":    "welcome",
	})
}

func handleDisplay(w http.ResponseWriter, r *http.Request, refreshRate int, tmplSrc, pngOut string, lat, lon float64, location string) {
	if !devMode {
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
	}

	var data dashboardData
	if !math.IsNaN(lat) && !math.IsNaN(lon) {
		wd, err := fetchWeather(lat, lon)
		if err != nil {
			log.Printf("[weather] unavailable: %v", err)
		} else {
			data = buildDashboard(wd)
		}
	}
	_ = location // retained for future footer use

	if err := renderHTML(tmplSrc, pngOut, data); err != nil {
		log.Printf("[display] render error: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": 500})
		return
	}

	imageURL := fmt.Sprintf("%s/dashboard.png?t=%d", baseURL(r), time.Now().Unix())
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
	refreshRate := flag.Int("refresh-rate", 1800, "Seconds between device polls")
	tmplSrc     := flag.String("tmpl", "dashboard.html", "HTML template source")
	pngOut      := flag.String("out", "dashboard.png", "PNG output path")
	dp          := flag.String("devices", "devices.json", "Device registry file")
	lat         := flag.Float64("lat", envFloat("TRMNL_LAT"), "Weather latitude (or set TRMNL_LAT)")
	lon         := flag.Float64("lon", envFloat("TRMNL_LON"), "Weather longitude (or set TRMNL_LON)")
	location    := flag.String("location", os.Getenv("TRMNL_LOCATION"), "Location name (or set TRMNL_LOCATION)")
	flag.Parse()

	devicesPath = *dp
	rand.Seed(time.Now().UnixNano()) //nolint:staticcheck
	loadRegistry(devicesPath)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/setup", handleSetup)
	mux.HandleFunc("GET /api/display", func(w http.ResponseWriter, r *http.Request) {
		handleDisplay(w, r, *refreshRate, *tmplSrc, *pngOut, *lat, *lon, *location)
	})
	mux.HandleFunc("POST /api/log", handleLog)
	mux.HandleFunc("GET /dashboard.png", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, *pngOut)
	})

	log.Printf("TRMNL BYOS server (html-stack)")
	log.Printf("  addr:           %s", *addr)
	log.Printf("  refresh-rate:   %ds", *refreshRate)
	if !math.IsNaN(*lat) {
		log.Printf("  location:       %.4f, %.4f (%s)", *lat, *lon, *location)
	} else {
		log.Printf("  location:       not configured (weather disabled)")
	}
	log.Printf("  template:       %s", *tmplSrc)
	log.Printf("  devices file:   %s", devicesPath)
	log.Printf("  devices loaded: %d", len(byMAC))

	log.Fatal(http.ListenAndServe(*addr, mux))
}
