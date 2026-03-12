package main

import (
	"bytes"
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

// -- dashboard dimensions (physical: 1872×1404 @ 227ppi) --
//
// All values derived from the original Typst design.
// 1pt = 227/72 ≈ 3.153px
const (
	ptPx = 227.0 / 72.0 // px per typographic point

	pageW   = 1872.0
	pageH   = 1404.0
	marginX = 102.0 // 0.45in × 227ppi
	marginY = 91.0  // 0.40in × 227ppi
	contentW = pageW - 2*marginX // 1668px

	iconCol  = 18 * ptPx // ~56.8px  — icon column
	iconGap  = 8 * ptPx  // ~25.2px  — gap between icon and labels
	labelW   = 22 * ptPx // ~69.4px  — y-axis label column
	chartW   = contentW - iconCol - iconGap - labelW // ~1516.6px
	chartH   = 28 * ptPx // ~88.3px  — chart height
	chartGap = 20 * ptPx // ~63.1px  — vertical gap between charts
	hExt     = labelW / 2 // h-line extension to the left
	vExt     = 14 * ptPx // ~44.1px  — v-line extension below last chart

	nDay    = 16 // 7am–10pm inclusive
	dayStep = chartW / (nDay - 1)

	labelFontSize = 8 * ptPx  // ~25.2px
	gridStrokeW   = 0.4 * ptPx // ~1.3px

	// x offset to chart data area
	chartX0 = iconCol + iconGap + labelW
)

// -- SVG chart generation --

type chartSeries struct {
	data           []float64
	unit           string
	yMin, yMax     float64
	autoMin, autoMax bool
	icon           string // "therm" | "wind" | "rain"
}

func sliceDay(arr []float64) []float64 {
	if len(arr) >= 23 {
		return arr[7:23] // 7am–10pm, 16 points
	}
	return arr
}

func buildChartSVG(hourlyTemp, hourlyWind, hourlyPrecip []float64) template.HTML {
	series := []chartSeries{
		{sliceDay(hourlyTemp),   "°C",  0, 0,   true,  true,  "therm"},
		{sliceDay(hourlyWind),   "mph", 0, 0,   true,  true,  "wind"},
		{sliceDay(hourlyPrecip), "%",   0, 100, false, false, "rain"},
	}

	chartsH := 3*chartH + 2*chartGap
	svgH := chartsH + vExt + labelFontSize + 4

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%.2f" height="%.2f" style="display:block;overflow:visible">`,
		contentW, svgH)

	gridStyle := `stroke="white" stroke-dasharray="8,6" style="mix-blend-mode:difference"`

	for ci, s := range series {
		data := s.data
		n := len(data)
		if n < 2 {
			continue
		}
		step := chartW / float64(n-1)
		cy := float64(ci) * (chartH + chartGap) // chart top y

		// Compute data range
		dMin, dMax := s.yMin, s.yMax
		if s.autoMin {
			dMin = data[0]
			for _, v := range data[1:] {
				if v < dMin {
					dMin = v
				}
			}
		}
		if s.autoMax {
			dMax = data[0]
			for _, v := range data[1:] {
				if v > dMax {
					dMax = v
				}
			}
		}
		dRng := dMax - dMin
		if dRng == 0 {
			dRng = 1
		}
		norm := func(v float64) float64 {
			n := (v - dMin) / dRng
			if n < 0 {
				return 0
			}
			if n > 1 {
				return 1
			}
			return n
		}

		// Compute data points
		type vec2 struct{ x, y float64 }
		pts := make([]vec2, n)
		for i, v := range data {
			pts[i] = vec2{chartX0 + float64(i)*step, cy + chartH*(1-norm(v))}
		}

		// 1. Fill polygon
		fillPts := make([]string, n+2)
		for i, p := range pts {
			fillPts[i] = fmt.Sprintf("%.1f,%.1f", p.x, p.y)
		}
		fillPts[n] = fmt.Sprintf("%.1f,%.1f", chartX0+chartW, cy+chartH)
		fillPts[n+1] = fmt.Sprintf("%.1f,%.1f", chartX0, cy+chartH)
		fmt.Fprintf(&b, `<polygon points="%s" fill="#e2e2e2" stroke="none"/>`,
			strings.Join(fillPts, " "))

		// 2. Data line (on top of fill, beneath grid)
		linePts := make([]string, n)
		for i, p := range pts {
			linePts[i] = fmt.Sprintf("%.1f,%.1f", p.x, p.y)
		}
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="black" stroke-width="%.1f" stroke-linejoin="round" stroke-linecap="round"/>`,
			strings.Join(linePts, " "), 1.0*ptPx)

		// 3. Horizontal grid lines (difference blend — visible over both fill and line)
		for _, frac := range []float64{0, 0.5, 1} {
			gy := cy + chartH*frac
			fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke-width="%.1f" %s/>`,
				chartX0-hExt, gy, chartX0+chartW, gy, gridStrokeW, gridStyle)
		}

		// 4. Y-axis labels (right of label area, centered on their h-line)
		lx := chartX0 - hExt - 5
		maxStr := fmt.Sprintf("%d%s", int(math.Round(dMax)), s.unit)
		minStr := fmt.Sprintf("%d%s", int(math.Round(dMin)), s.unit)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end" dominant-baseline="middle" font-size="%.1f" font-family="Georgia,serif" fill="black">%s</text>`,
			lx, cy, labelFontSize, maxStr)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" text-anchor="end" dominant-baseline="middle" font-size="%.1f" font-family="Georgia,serif" fill="black">%s</text>`,
			lx, cy+chartH, labelFontSize, minStr)

		// 5. Icon (centered in icon column, vertically centered on chart)
		icx := iconCol / 2
		icy := cy + chartH/2
		sw := 1.5 * ptPx // icon stroke width
		switch s.icon {
		case "therm":
			bulbR := 4 * ptPx
			stemH := 13 * ptPx
			stemBot := icy - bulbR
			stemTop := stemBot - stemH
			fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="black" stroke-width="%.1f" stroke-linecap="round"/>`,
				icx, stemTop, icx, stemBot, sw)
			fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" fill="black"/>`,
				icx, icy, bulbR)
		case "wind":
			gap := 4 * ptPx
			lengths := []float64{14 * ptPx, 10 * ptPx, 6 * ptPx}
			totalH := gap * float64(len(lengths)-1)
			top := icy - totalH/2
			for li, l := range lengths {
				ly := top + float64(li)*gap
				fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="black" stroke-width="%.1f" stroke-linecap="round"/>`,
					icx-l/2, ly, icx+l/2, ly, sw)
			}
		case "rain":
			tw := 12 * ptPx
			th := 14 * ptPx
			fmt.Fprintf(&b, `<polygon points="%.1f,%.1f %.1f,%.1f %.1f,%.1f" fill="black"/>`,
				icx-tw/2, icy-th/2, icx+tw/2, icy-th/2, icx, icy+th/2)
		}
	}

	// Shared vertical time lines spanning all three charts
	timeIdxs := []int{0, 5, 10, 15}
	timeLabels := []string{"7", "12", "17", "22"}
	lineBottom := chartsH + vExt

	for i, idx := range timeIdxs {
		vx := chartX0 + float64(idx)*dayStep
		// Line stops a few px above label to avoid last-dash overlap
		fmt.Fprintf(&b, `<line x1="%.1f" y1="0" x2="%.1f" y2="%.1f" stroke-width="%.1f" %s/>`,
			vx, vx, lineBottom-labelFontSize, gridStrokeW, gridStyle)
		// Label sits below the line, left-aligned from the line
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="%.1f" font-family="Georgia,serif" fill="black">%s</text>`,
			vx+4, lineBottom, labelFontSize, timeLabels[i])
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// -- dashboard template --

type dashboardData struct {
	Temp      int
	TempMin   int
	TempMax   int
	WindMPH   int
	Condition string
	ChartSVG  template.HTML
	Debug     bool
}

func buildDashboard(wd *weatherData, debug bool) dashboardData {
	d := dashboardData{
		Condition: wd.Condition,
		Temp:      wd.Temp,
		TempMin:   wd.TempMin,
		TempMax:   wd.TempMax,
		WindMPH:   wd.WindMPH,
		Debug:     debug,
	}
	d.ChartSVG = buildChartSVG(wd.HourlyTemp, wd.HourlyWind, wd.HourlyPrecip)
	return d
}

// -- Chrome rendering --

func findChrome() (string, error) {
	// Check CHROME_BIN env override first
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

func renderHTML(tmplPath, pngOut string, data dashboardData) error {
	renderMu.Lock()
	defer renderMu.Unlock()

	// Parse and execute template into a temp HTML file
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

	// Run Chrome headless
	chrome, err := findChrome()
	if err != nil {
		return err
	}
	absHTML, _ := filepath.Abs(tmpPath)
	absPNG, _ := filepath.Abs(pngOut)

	cmd := exec.Command(chrome,
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--hide-scrollbars",
		"--force-device-scale-factor=1",
		"--window-size=1872,1404",
		"--screenshot="+absPNG,
		"file://"+absHTML,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func handleDisplay(w http.ResponseWriter, r *http.Request, refreshRate int, tmplSrc, pngOut string, lat, lon float64, location string, debug bool) {
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

	var data dashboardData
	if !math.IsNaN(lat) && !math.IsNaN(lon) {
		wd, err := fetchWeather(lat, lon)
		if err != nil {
			log.Printf("[weather] unavailable: %v", err)
		} else {
			data = buildDashboard(wd, debug)
		}
	}
	if location != "" {
		// location not currently rendered but kept for future use
		_ = location
	}

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
	once        := flag.Bool("once", false, "Fetch weather, render PNG, and exit (for preview)")
	debug       := flag.Bool("debug", false, "Show margin guides in rendered output")
	flag.Parse()

	if *once {
		var data dashboardData
		if !math.IsNaN(*lat) && !math.IsNaN(*lon) {
			wd, err := fetchWeather(*lat, *lon)
			if err != nil {
				log.Fatalf("[weather] %v", err)
			}
			data = buildDashboard(wd, *debug)
		}
		if err := renderHTML(*tmplSrc, *pngOut, data); err != nil {
			log.Fatalf("render: %v", err)
		}
		return
	}

	devicesPath = *dp

	rand.Seed(time.Now().UnixNano()) //nolint:staticcheck
	loadRegistry(devicesPath)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup", handleSetup)
	mux.HandleFunc("GET /api/display", func(w http.ResponseWriter, r *http.Request) {
		handleDisplay(w, r, *refreshRate, *tmplSrc, *pngOut, *lat, *lon, *location, *debug)
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
