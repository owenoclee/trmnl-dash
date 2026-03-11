package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strconv"
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

// -- weather --

type weatherResponse struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Hourly struct {
		Temperature      []float64 `json:"temperature_2m"`
		WindSpeed        []float64 `json:"wind_speed_10m"`
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

// envFloat reads a float64 from an environment variable.
// Returns NaN if the variable is unset or invalid.
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

// -- helpers --

const charset = "abcdefghijklmnopqrstuvwxyz0123456789"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func compile(src, out string, inputs map[string]string) error {
	compileMu.Lock()
	defer compileMu.Unlock()
	args := []string{"compile", "--format", "png", "--ppi", "227", "--pages", "1"}
	for k, v := range inputs {
		args = append(args, "--input", k+"="+v)
	}
	args = append(args, src, out)
	cmd := exec.Command("typst", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// weatherInputs fetches weather and returns a map of --input key=value pairs
// ready to pass to compile. Returns an empty map if location is not configured
// or the fetch fails.
func weatherInputs(lat, lon float64, location string) map[string]string {
	inputs := map[string]string{}
	if location != "" {
		inputs["location"] = location
	}
	if math.IsNaN(lat) || math.IsNaN(lon) {
		return inputs
	}
	wd, err := fetchWeather(lat, lon)
	if err != nil {
		log.Printf("[weather] unavailable: %v", err)
		return inputs
	}
	joinFloats := func(vals []float64) string {
		s := make([]string, len(vals))
		for i, v := range vals {
			s[i] = fmt.Sprintf("%.1f", v)
		}
		return strings.Join(s, ",")
	}
	inputs["temp"]          = fmt.Sprintf("%d", wd.Temp)
	inputs["temp-min"]      = fmt.Sprintf("%d", wd.TempMin)
	inputs["temp-max"]      = fmt.Sprintf("%d", wd.TempMax)
	inputs["wind"]          = fmt.Sprintf("%d", wd.WindMPH)
	inputs["condition"]     = wd.Condition
	inputs["hourly-temp"]   = joinFloats(wd.HourlyTemp)
	inputs["hourly-wind"]   = joinFloats(wd.HourlyWind)
	inputs["hourly-precip"] = joinFloats(wd.HourlyPrecip)
	return inputs
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// baseURL constructs the server's base URL from the incoming request so we
// never need to hard-code or configure it.
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

func handleDisplay(w http.ResponseWriter, r *http.Request, refreshRate int, typstSrc, pngOut string, lat, lon float64, location string) {
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

	if err := compile(typstSrc, pngOut, weatherInputs(lat, lon, location)); err != nil {
		log.Printf("[display] compile error: %v", err)
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
	typstSrc    := flag.String("src", "dashboard.typ", "Typst source file")
	pngOut      := flag.String("out", "dashboard.png", "PNG output path")
	dp          := flag.String("devices", "devices.json", "Device registry file")
	lat         := flag.Float64("lat", envFloat("TRMNL_LAT"), "Weather latitude (or set TRMNL_LAT)")
	lon         := flag.Float64("lon", envFloat("TRMNL_LON"), "Weather longitude (or set TRMNL_LON)")
	location    := flag.String("location", os.Getenv("TRMNL_LOCATION"), "Location name shown in footer (or set TRMNL_LOCATION)")
	once        := flag.Bool("once", false, "Fetch weather, compile, and open the PNG — then exit (useful for previewing)")
	flag.Parse()

	if *once {
		if err := compile(*typstSrc, *pngOut, weatherInputs(*lat, *lon, *location)); err != nil {
			log.Fatalf("compile: %v", err)
		}
		return
	}

	devicesPath = *dp

	rand.Seed(time.Now().UnixNano()) //nolint:staticcheck
	loadRegistry(devicesPath)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup", handleSetup)
	mux.HandleFunc("GET /api/display", func(w http.ResponseWriter, r *http.Request) {
		handleDisplay(w, r, *refreshRate, *typstSrc, *pngOut, *lat, *lon, *location)
	})
	mux.HandleFunc("POST /api/log", handleLog)
	mux.HandleFunc("GET /dashboard.png", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, *pngOut)
	})

	log.Printf("TRMNL BYOS server")
	log.Printf("  addr:           %s", *addr)
	log.Printf("  refresh-rate:   %ds", *refreshRate)
	if !math.IsNaN(*lat) {
		log.Printf("  location:       %.4f, %.4f (%s)", *lat, *lon, *location)
	} else {
		log.Printf("  location:       not configured (weather disabled)")
	}
	log.Printf("  devices file:   %s", devicesPath)
	log.Printf("  devices loaded: %d", len(byMAC))

	log.Fatal(http.ListenAndServe(*addr, mux))
}
