package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	configPath  string

	// photosDir holds the images cycled into the dashboard's bottom-left slot.
	// renderBaseURL is the loopback URL the headless render uses to fetch them
	// (the page is loaded from file://, so photo <img> src must be absolute, and
	// must be CORS-clean so the canvas dithering pass can read pixels back).
	photosDir     string
	renderBaseURL string

	// lastRenderUnix is the time of the most recent successful render, used as the
	// image cache-buster. Read/written atomically (the render loop and request
	// handlers touch it from different goroutines).
	lastRenderUnix int64
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
		Temperature  float64 `json:"temperature_2m"`
		WeatherCode  int     `json:"weather_code"`
		WindSpeed    float64 `json:"wind_speed_10m"`
		ApparentTemp float64 `json:"apparent_temperature"`
	} `json:"current"`
	Hourly struct {
		Temperature       []float64 `json:"temperature_2m"`
		WindSpeed         []float64 `json:"wind_speed_10m"`
		PrecipProbability []float64 `json:"precipitation_probability"`
		UVIndex           []float64 `json:"uv_index"`
	} `json:"hourly"`
	Daily struct {
		TempMax []float64 `json:"temperature_2m_max"`
		TempMin []float64 `json:"temperature_2m_min"`
	} `json:"daily"`
}

type weatherData struct {
	Temp         int
	FeelsLike    int
	TempMin      int
	TempMax      int
	WindMPH      int
	Condition    string
	HourlyTemp   []float64
	HourlyWind   []float64
	HourlyPrecip []float64
	HourlyUV     []float64
	FetchedAt    time.Time
}

var (
	weatherMu    sync.Mutex
	weatherCache *weatherData
)

func fetchWeather(lat, lon float64) (*weatherData, error) {
	weatherMu.Lock()
	defer weatherMu.Unlock()

	// Fresh cache hit: serve it without touching the network.
	if weatherCache != nil && time.Since(weatherCache.FetchedAt) < 15*time.Minute {
		return weatherCache, nil
	}

	// Cache is cold or past its TTL → try to refresh.
	wd, err := fetchWeatherUncached(lat, lon)
	if err != nil {
		// Stale-while-revalidate: a refresh failure must never blank the display,
		// so fall back to the last-successful copy however old it is. weatherCache
		// only ever holds real, successfully-fetched data (the placeholder is never
		// stored), so this is always genuine weather — just stale. Only when we've
		// never had a single successful fetch do we surface the error, so the caller
		// can render obviously-fake placeholder data instead.
		if weatherCache != nil {
			log.Printf("[weather] refresh failed (%v); serving cached data from %s ago",
				err, time.Since(weatherCache.FetchedAt).Round(time.Minute))
			return weatherCache, nil
		}
		return nil, err
	}

	weatherCache = wd
	return wd, nil
}

// fetchWeatherUncached performs the Open-Meteo request and parses the response
// into a weatherData. It never reads or writes weatherCache — caching and the
// stale-fallback policy are fetchWeather's responsibility.
func fetchWeatherUncached(lat, lon float64) (*weatherData, error) {
	url := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast"+
			"?latitude=%.4f&longitude=%.4f"+
			"&current=temperature_2m,weather_code,wind_speed_10m,apparent_temperature"+
			"&hourly=temperature_2m,wind_speed_10m,precipitation_probability,uv_index"+
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
		FeelsLike:    int(math.Round(wr.Current.ApparentTemp)),
		WindMPH:      int(math.Round(wr.Current.WindSpeed)),
		Condition:    wmoCondition(wr.Current.WeatherCode),
		HourlyTemp:   cap24(wr.Hourly.Temperature),
		HourlyWind:   cap24(wr.Hourly.WindSpeed),
		HourlyPrecip: cap24(wr.Hourly.PrecipProbability),
		HourlyUV:     cap24(wr.Hourly.UVIndex),
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
	return wd, nil
}

// synthWeather returns obviously-fake placeholder weather, used only when a
// fetch fails AND there's no cached copy to fall back on (e.g. the first render
// after a cold start while Open-Meteo is unreachable). Every hourly series is a
// hard 0↔100 sawtooth so the charts render as unmistakable jagged spikes, and
// the KPI scalars are nonsensical — the display must read as "broken" and never
// be confused for real conditions.
func synthWeather() *weatherData {
	sawtooth := func() []float64 {
		s := make([]float64, 24)
		for i := range s {
			if i%2 == 1 {
				s[i] = 100
			}
		}
		return s
	}
	return &weatherData{
		Temp:         0,
		FeelsLike:    100,
		TempMin:      0,
		TempMax:      100,
		WindMPH:      100,
		Condition:    "No weather data",
		HourlyTemp:   sawtooth(),
		HourlyWind:   sawtooth(),
		HourlyPrecip: sawtooth(),
		HourlyUV:     sawtooth(),
		FetchedAt:    time.Now(),
	}
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

// -- personal stats (Prometheus) --
//
// The left column charts the last seven days of personal metrics that already
// live in the cluster's Prometheus: iPhone screen time, indoor CO₂ and the
// mobile data allowance. Go runs the queries and hands the raw points over;
// all bucketing into day slots, scaling and drawing happen in dashboard.html,
// mirroring the weather split. Unconfigured or unreachable → "null", and the
// page draws empty frames rather than sample data.

const (
	statsDays = 7                // day slots on the left column's x-axis, ending today
	statsStep = 30 * time.Minute // resolution of the CO₂ and data-remaining curves
)

// statsJSON is the data contract for the left column. Point series are
// [unix seconds, value] pairs; a missing point is a gap in the data. Instants
// are pointers so "no data" serialises as null rather than a fabricated zero.
type statsJSON struct {
	WindowStart int64        `json:"windowStart"` // local midnight, statsDays-1 days ago
	Step        int          `json:"step"`        // seconds between CO₂ / data points
	ScreenHours []*float64   `json:"screenHours"` // per local day, oldest first; today in progress
	CO2         [][2]float64 `json:"co2"`
	CO2Now      *float64     `json:"co2Now"`
	DataGB      [][2]float64 `json:"dataGB"`
	DataNowGB   *float64     `json:"dataNowGB"`
	AllowanceGB *float64     `json:"allowanceGB"`
	BoostStart  *float64     `json:"boostStart"` // unix seconds
	BoostEnd    *float64     `json:"boostEnd"`   // unix seconds; the next refresh
	FetchedAt   time.Time    `json:"-"`
}

var (
	promURL    string
	promClient = &http.Client{Timeout: 10 * time.Second}
	statsMu    sync.Mutex
	statsCache *statsJSON
)

// promQuery runs a PromQL query against the Prometheus HTTP API. With a zero
// step it's an instant query; otherwise a range query over [start, end]. Each
// query is wrapped by the caller in an aggregation, so only the first series of
// the result is read.
func promQuery(q string, start, end time.Time, step time.Duration) ([][2]float64, error) {
	v := url.Values{"query": {q}}
	endpoint := "/api/v1/query"
	if step > 0 {
		endpoint = "/api/v1/query_range"
		v.Set("start", strconv.FormatInt(start.Unix(), 10))
		v.Set("end", strconv.FormatInt(end.Unix(), 10))
		v.Set("step", strconv.Itoa(int(step.Seconds())))
	}
	resp, err := promClient.Get(promURL + endpoint + "?" + v.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value  [2]any   `json:"value"`
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("%s: %s", q, body.Error)
	}
	if len(body.Data.Result) == 0 {
		return nil, nil // no data is not an error: the exporter may just be down
	}
	r := body.Data.Result[0]
	raw := r.Values
	if step == 0 {
		raw = [][2]any{r.Value}
	}
	out := make([][2]float64, 0, len(raw))
	for _, p := range raw {
		ts, _ := p[0].(float64)
		s, _ := p[1].(string)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		out = append(out, [2]float64{ts, f})
	}
	return out, nil
}

// promInstant returns the value of an instant query, or nil when it has no data.
func promInstant(q string) (*float64, error) {
	pts, err := promQuery(q, time.Time{}, time.Time{}, 0)
	if err != nil || len(pts) == 0 {
		return nil, err
	}
	return &pts[0][1], nil
}

// fetchStats returns the left column's data, refreshing it on every call (the
// render loop already paces calls to once per render interval). Like the
// weather, a failed refresh falls back to the last good copy however old it is;
// only with no copy at all does it return nil.
func fetchStats() *statsJSON {
	if promURL == "" {
		return nil
	}
	statsMu.Lock()
	defer statsMu.Unlock()
	st, err := fetchStatsUncached()
	if err != nil {
		if statsCache != nil {
			log.Printf("[stats] refresh failed (%v); serving cached data from %s ago",
				err, time.Since(statsCache.FetchedAt).Round(time.Minute))
			return statsCache
		}
		log.Printf("[stats] no data available: %v", err)
		return nil
	}
	statsCache = st
	return st
}

func fetchStatsUncached() (*statsJSON, error) {
	now := time.Now()
	y, m, d := now.Date()
	winStart := time.Date(y, m, d-(statsDays-1), 0, 0, 0, 0, time.Local)
	st := &statsJSON{WindowStart: winStart.Unix(), Step: int(statsStep.Seconds()), FetchedAt: now}

	// Screen time: hourly sums, bucketed into local days here rather than with a
	// [1d] range, so today's bar runs from midnight and DST days stay whole. A
	// sample at T covers (T-1h, T], so it belongs to the day containing T-1h.
	hourly, err := promQuery(
		`sum(sum_over_time(screentime_device_foreground_seconds{device="iphone"}[1h])) / 3600`,
		winStart.Add(time.Hour), now.Truncate(time.Hour).Add(time.Hour), time.Hour)
	if err != nil {
		return nil, fmt.Errorf("screen time: %w", err)
	}
	st.ScreenHours = make([]*float64, statsDays)
	for _, p := range hourly {
		t := time.Unix(int64(p[0]), 0).Add(-time.Hour)
		ty, tm, td := t.Date()
		day := int(time.Date(ty, tm, td, 12, 0, 0, 0, time.Local).Sub(winStart).Hours() / 24)
		if day < 0 || day >= statsDays {
			continue
		}
		if st.ScreenHours[day] == nil {
			st.ScreenHours[day] = new(float64)
		}
		*st.ScreenHours[day] += p[1]
	}

	ranges := []struct {
		q   string
		dst *[][2]float64
	}{
		{`avg(avg_over_time(inkbird_iam_t1_co2_ppm[30m]))`, &st.CO2},
		{`max(last_over_time(onepmobile_data_remaining_bytes[30m])) / 2^30`, &st.DataGB},
	}
	for _, r := range ranges {
		pts, err := promQuery(r.q, winStart.Add(statsStep), now, statsStep)
		if err != nil {
			return nil, err
		}
		*r.dst = pts
	}

	instants := []struct {
		q   string
		dst **float64
	}{
		{`max(last_over_time(inkbird_iam_t1_co2_ppm[15m]))`, &st.CO2Now},
		{`max(last_over_time(onepmobile_data_remaining_bytes[1h])) / 2^30`, &st.DataNowGB},
		{`max(last_over_time(onepmobile_data_allowance_bytes[1h])) / 2^30`, &st.AllowanceGB},
		{`max(last_over_time(onepmobile_boost_period_start_timestamp_seconds[1h]))`, &st.BoostStart},
		{`max(last_over_time(onepmobile_boost_next_refresh_timestamp_seconds[1h]))`, &st.BoostEnd},
	}
	for _, in := range instants {
		v, err := promInstant(in.q)
		if err != nil {
			return nil, err
		}
		*in.dst = v
	}

	var today float64
	if h := st.ScreenHours[statsDays-1]; h != nil {
		today = *h
	}
	log.Printf("[stats] screen today %.1fh, %d CO₂ points, %d data points", today, len(st.CO2), len(st.DataGB))
	return st, nil
}

// currentStatsJSON serialises the left column's data for injection. Always
// valid JSON ("null" when there's none), so the page can tell "server has no
// data" apart from the raw placeholder it sees when opened from disk.
func currentStatsJSON() template.JS {
	st := fetchStats()
	if st == nil {
		return "null"
	}
	b, _ := json.Marshal(st)
	return template.JS(b)
}

// -- dashboard template data --
//
// Go's job: fetch data, serialise to JSON, hand off to the template.
// All chart rendering logic lives in dashboard.html / JS.

// dashJSON is the complete data contract passed to the JS layer.
// Go's only job: populate this struct and serialise it.
type dashJSON struct {
	Temp         int       `json:"temp"`
	FeelsLike    int       `json:"feelsLike"`
	TempMin      int       `json:"tempMin"`
	TempMax      int       `json:"tempMax"`
	WindMPH      int       `json:"windMPH"`
	Condition    string    `json:"condition"`
	Debug        bool      `json:"debug"`
	Headless     bool      `json:"headless"`
	HourlyTemp   []float64 `json:"hourlyTemp"`
	HourlyWind   []float64 `json:"hourlyWind"`
	HourlyPrecip []float64 `json:"hourlyPrecip"`
	HourlyUV     []float64 `json:"hourlyUV"`
}

// dashboardData is the Go template context. DataJSON carries the weather blob
// (empty when weather is unconfigured, which makes the page fall back to its
// built-in sample data); PhotosJSON is injected independently so the photo
// cycling works regardless of whether weather is available. ConfigJSON carries
// the tunables from config.json so a render uses the values dialled in via the
// preview panel (empty → the page keeps its built-in cfg defaults). StatsJSON
// carries the left column's Prometheus data ("null" → empty charts), also
// independent of weather.
type dashboardData struct {
	DataJSON   template.JS
	PhotosJSON template.JS
	ConfigJSON template.JS
	StatsJSON  template.JS
}

func buildDashboard(wd *weatherData) dashboardData {
	d := dashJSON{
		Temp:         wd.Temp,
		FeelsLike:    wd.FeelsLike,
		TempMin:      wd.TempMin,
		TempMax:      wd.TempMax,
		WindMPH:      wd.WindMPH,
		Condition:    wd.Condition,
		Debug:        debugMode,
		Headless:     true,
		HourlyTemp:   wd.HourlyTemp,
		HourlyWind:   wd.HourlyWind,
		HourlyPrecip: wd.HourlyPrecip,
		HourlyUV:     wd.HourlyUV,
	}
	jsonBytes, _ := json.Marshal(d)
	return dashboardData{DataJSON: template.JS(jsonBytes)}
}

// loadConfigJSON reads the tunables file (written by the preview panel's Apply
// button) and returns it for injection into the template. It's re-read on every
// render, so replacing config.json takes effect on the next render with no
// restart. Missing file → empty (page keeps its built-in defaults); invalid
// JSON → empty + a warning, so a botched edit degrades to defaults rather than
// breaking the render.
func loadConfigJSON() template.JS {
	if configPath == "" {
		return ""
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "" // no config → built-in defaults
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		log.Printf("[config] ignoring invalid %s: %v", configPath, err)
		return ""
	}
	b, _ := json.Marshal(obj)
	return template.JS(b)
}

// photoExts is the set of image types served from the photos directory.
var photoExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// photoRef is one photo as handed to the page: an absolute loopback URL, and
// for uploads the focus rect (x, y, w, h in the image's pixels) the page zooms
// to fit. Photos from photosDir have no focus and are centre-cropped.
type photoRef struct {
	URL   string  `json:"url"`
	Focus *[4]int `json:"focus,omitempty"`
}

// listPhotos returns every photo in rotation — photosDir's files, then the
// uploads mirrored from the phone — each list sorted by name. Which one is
// shown is decided by advancePhoto.
func listPhotos() []photoRef {
	var refs []photoRef
	for _, n := range listDir(photosDir, func(name string) bool {
		return photoExts[strings.ToLower(filepath.Ext(name))]
	}) {
		refs = append(refs, photoRef{URL: renderBaseURL + "/photos/" + url.PathEscape(n)})
	}
	for _, n := range listDir(uploadsDir, func(name string) bool {
		return strings.HasSuffix(name, ".tgry") && uploadID.MatchString(strings.TrimSuffix(name, ".tgry"))
	}) {
		id := strings.TrimSuffix(n, ".tgry")
		focus, err := readTGRYFocus(filepath.Join(uploadsDir, n))
		if err != nil {
			log.Printf("[uploads] skipping %s: %v", n, err)
			continue
		}
		refs = append(refs, photoRef{URL: renderBaseURL + "/uploads/" + id + ".png", Focus: &focus})
	}
	return refs
}

// listDir returns the sorted names of the regular files in dir that keep()
// accepts. Dotfiles are skipped: macOS junk such as .DS_Store and especially
// "._name.jpg" AppleDouble files carry an image extension but aren't images,
// and otherwise enter the rotation and render as a blank photo.
func listDir(dir string, keep func(string) bool) []string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !keep(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// Photo selection state. The chosen photo advances only on /api/display, so
// every background pre-render between two device polls uses the same image —
// the selection stays put until the device actually fetches again.
var (
	photoMu        sync.Mutex
	photoStrategy  string     // "random" | "shuffle" | "alphabetical" (set at startup)
	photoSelection *photoRef  // currently-chosen photo
	photoOrder     []photoRef // shuffle: the shuffled order being walked
	photoOrderSig  string     // file-set signature photoOrder was built from
	photoIdx       int        // position within photoOrder / the alphabetical cycle
)

// advancePhoto moves the selection to the next photo per the configured strategy.
// Call once per /api/display so the image changes only when the device fetches.
func advancePhoto() {
	refs := listPhotos()
	photoMu.Lock()
	defer photoMu.Unlock()
	if len(refs) == 0 {
		photoSelection = nil
		return
	}
	var next photoRef
	switch photoStrategy {
	case "alphabetical":
		next = refs[photoIdx%len(refs)] // listPhotos is sorted by name
		photoIdx++
	case "shuffle":
		urls := make([]string, len(refs))
		for i, r := range refs {
			urls[i] = r.URL
		}
		sig := strings.Join(urls, "\x00")
		if photoIdx >= len(photoOrder) || sig != photoOrderSig {
			photoOrder = append([]photoRef(nil), refs...)
			rand.Shuffle(len(photoOrder), func(i, j int) {
				photoOrder[i], photoOrder[j] = photoOrder[j], photoOrder[i]
			})
			photoOrderSig = sig
			photoIdx = 0
		}
		next = photoOrder[photoIdx]
		photoIdx++
	default: // random
		next = refs[rand.Intn(len(refs))]
	}
	photoSelection = &next
}

// currentPhotoJSON injects the currently-selected photo as a 0- or 1-element
// list (matching the page's expectation), without advancing the selection.
func currentPhotoJSON() template.JS {
	photoMu.Lock()
	sel := photoSelection
	photoMu.Unlock()
	arr := []photoRef{}
	if sel != nil {
		arr = append(arr, *sel)
	}
	b, _ := json.Marshal(arr)
	return template.JS(b)
}

// -- uploads (photos.pog.so) --
//
// Photos added from the phone live in an R2 bucket behind the uploader Worker
// (uploader/ in this repo). Every few minutes the server mirrors them into
// uploadsDir with the Worker's read-only display token, so a render never
// waits on the network and an outage just freezes the set. They arrive as TGRY
// — raw greyscale behind a fixed header (uploader/public/format.js) — so no
// image decoder ever runs on them here: parseTGRY checks the header and length
// exactly as the Worker did, and they're served to the page as PNGs encoded
// from the validated pixels.

const (
	tgryHeader   = 20
	tgryMinDim   = 16
	tgryMaxDim   = 4096
	tgryMaxPix   = 4_000_000
	uploadsEvery = 5 * time.Minute
)

var (
	uploadsURL   string
	uploadsToken string
	uploadsDir   string
	uploadID     = regexp.MustCompile(`^[0-9a-z]{8,12}-[0-9a-f]{8}$`)
	uploadClient = &http.Client{Timeout: 60 * time.Second}
)

type tgry struct {
	W, H  int
	Focus [4]int
	Pix   []byte
}

// parseTGRY validates a whole TGRY file. Keep in step with format.js's parse.
func parseTGRY(b []byte) (*tgry, error) {
	if len(b) < tgryHeader {
		return nil, fmt.Errorf("too short for a header")
	}
	if string(b[:4]) != "TGRY" || b[4] != 1 || b[5]|b[6]|b[7] != 0 {
		return nil, fmt.Errorf("not a TGRY v1 header")
	}
	u16 := func(off int) int { return int(b[off]) | int(b[off+1])<<8 }
	t := &tgry{W: u16(8), H: u16(10), Focus: [4]int{u16(12), u16(14), u16(16), u16(18)}}
	if t.W < tgryMinDim || t.H < tgryMinDim || t.W > tgryMaxDim || t.H > tgryMaxDim || t.W*t.H > tgryMaxPix {
		return nil, fmt.Errorf("bad dimensions %dx%d", t.W, t.H)
	}
	f := t.Focus
	if f[2] < tgryMinDim || f[3] < tgryMinDim || f[0]+f[2] > t.W || f[1]+f[3] > t.H {
		return nil, fmt.Errorf("focus outside image")
	}
	if len(b) != tgryHeader+t.W*t.H {
		return nil, fmt.Errorf("length %d, want %d", len(b), tgryHeader+t.W*t.H)
	}
	t.Pix = b[tgryHeader:]
	return t, nil
}

// readTGRYFocus reads just the focus rect of a mirrored upload (already
// validated when it was written).
func readTGRYFocus(path string) ([4]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return [4]int{}, err
	}
	defer f.Close()
	h := make([]byte, tgryHeader)
	if _, err := io.ReadFull(f, h); err != nil {
		return [4]int{}, err
	}
	u16 := func(off int) int { return int(h[off]) | int(h[off+1])<<8 }
	return [4]int{u16(12), u16(14), u16(16), u16(18)}, nil
}

func uploadsGet(path string) (*http.Response, error) {
	req, err := http.NewRequest("GET", uploadsURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+uploadsToken)
	resp, err := uploadClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return resp, nil
}

// syncUploads makes uploadsDir match the bucket: fetch what's new, drop what
// was removed on the phone. A failed listing changes nothing locally.
func syncUploads() {
	resp, err := uploadsGet("/api/photos")
	if err != nil {
		log.Printf("[uploads] list: %v", err)
		return
	}
	var listing struct {
		Photos []struct {
			ID string `json:"id"`
		} `json:"photos"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&listing)
	resp.Body.Close()
	if err != nil {
		log.Printf("[uploads] list decode: %v", err)
		return
	}
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		log.Printf("[uploads] %v", err)
		return
	}

	want := map[string]bool{}
	added := 0
	for _, p := range listing.Photos {
		if !uploadID.MatchString(p.ID) {
			continue
		}
		want[p.ID+".tgry"] = true
		dst := filepath.Join(uploadsDir, p.ID+".tgry")
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := fetchUpload(p.ID, dst); err != nil {
			log.Printf("[uploads] %s: %v", p.ID, err)
			continue
		}
		added++
	}
	removed := 0
	for _, n := range listDir(uploadsDir, func(string) bool { return true }) {
		if !want[n] {
			if os.Remove(filepath.Join(uploadsDir, n)) == nil {
				removed++
			}
		}
	}
	if added+removed > 0 {
		log.Printf("[uploads] %d in the bucket: +%d -%d", len(want), added, removed)
	}
}

func fetchUpload(id, dst string) error {
	resp, err := uploadsGet("/api/photos/" + id)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, tgryHeader+tgryMaxPix+1))
	if err != nil {
		return err
	}
	if _, err := parseTGRY(b); err != nil {
		return fmt.Errorf("rejected: %w", err)
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// serveUpload serves a mirrored upload as a greyscale PNG for the page.
func serveUpload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("file"), ".png")
	if !uploadID.MatchString(id) || !strings.HasSuffix(r.PathValue("file"), ".png") {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(uploadsDir, id+".tgry"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t, err := parseTGRY(b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	img := &image.Gray{Pix: t.Pix, Stride: t.W, Rect: image.Rect(0, 0, t.W, t.H)}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Access-Control-Allow-Origin", "*") // read back into a canvas from file://
	(&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(w, img)
}

// loopbackURL turns a listen address (":8080", "0.0.0.0:8080") into the
// loopback URL the local headless browser uses to fetch photos during a render.
func loopbackURL(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		port = "8080"
	}
	return "http://127.0.0.1:" + port
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
	chromeMu     sync.Mutex // guards the chrome context against re-init races and the shutdown handler
	chromeCtx    context.Context
	chromeCancel context.CancelFunc
)

// initChrome lazily starts headless Chrome and reuses it across renders. If the
// previous browser died (e.g. it crashed or was OOM-killed, leaving the context
// canceled), it reaps the dead context and starts a fresh one — so the render
// loop self-heals on the next tick instead of wedging on "context canceled"
// until the process is restarted.
func initChrome() error {
	chromeMu.Lock()
	defer chromeMu.Unlock()

	// Reuse the running browser while its context is still healthy.
	if chromeCtx != nil && chromeCtx.Err() == nil {
		return nil
	}
	// A non-nil context here is a dead one (Chrome exited); reap its cancel
	// funcs before replacing them so we don't leak the old process tree.
	if chromeCancel != nil {
		log.Printf("[render] headless Chrome context is dead; restarting browser")
		chromeCancel()
		chromeCtx, chromeCancel = nil, nil
	}

	chrome, err := findChrome()
	if err != nil {
		return err
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("force-device-scale-factor", "1"),
		chromedp.WindowSize(1872, 1404),
	)
	// In a container Chromium has no usable sandbox (no setuid helper / user
	// namespaces) and the default 64 MB /dev/shm is too small for a render this
	// large, which crashes the tab. CHROME_NO_SANDBOX (set by the Docker image)
	// opts into both fixes; it stays off on a normal desktop install.
	if os.Getenv("CHROME_NO_SANDBOX") != "" {
		opts = append(opts,
			chromedp.NoSandbox,
			chromedp.Flag("disable-dev-shm-usage", true),
		)
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	chromeCtx = ctx
	chromeCancel = func() { cancel(); allocCancel() }
	return nil
}

// shutdownChrome tears down the headless Chrome child (process + temp profile)
// that chromedp launched. Without it, every SIGINT/SIGTERM — dev restarts,
// Ctrl-C — orphans the browser, leaking a process tree each time. No-op if
// Chrome never started.
func shutdownChrome() {
	chromeMu.Lock()
	cancel := chromeCancel
	chromeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// optimizePNG converts the RGB screenshot into a 16-level grayscale PNG — exactly
// what the e-ink panel can display — which roughly halves the device's download
// (8-bit RGB → 4-bit indexed) while looking identical on the panel. Falls back to
// the original bytes if anything goes wrong.
func optimizePNG(raw []byte) []byte {
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		log.Printf("[render] optimize decode: %v", err)
		return raw
	}
	b := src.Bounds()
	pal := make(color.Palette, 16)
	for i := range pal {
		pal[i] = color.Gray{Y: uint8(i * 255 / 15)}
	}
	dst := image.NewPaletted(b, pal)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := src.At(x, y).RGBA()
			dst.SetColorIndex(x, y, uint8((int(r>>8)*15+127)/255)) // nearest of 16 levels
		}
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, dst); err != nil {
		log.Printf("[render] optimize encode: %v", err)
		return raw
	}
	return out.Bytes()
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
		// Wait for the page to finish charts + async photo load/dither. The page
		// always sets this flag (incl. a self-timeout backstop), so this resolves
		// promptly even when there's no photo or the photo fails to load.
		chromedp.Poll("window.__renderDone === true", nil, chromedp.WithPollingTimeout(8*time.Second)),
		chromedp.CaptureScreenshot(&buf),
	); err != nil {
		return fmt.Errorf("chromedp: %w", err)
	}
	buf = optimizePNG(buf)
	if err := os.WriteFile(absPNG, buf, 0644); err != nil {
		return err
	}
	atomic.StoreInt64(&lastRenderUnix, time.Now().Unix())
	return nil
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

// currentDashboardData gathers everything the template needs for a render:
// (cached) weather plus the photo list. Used by both the background render loop
// and the on-demand dev/preview path.
func currentDashboardData(lat, lon float64, location string) dashboardData {
	var data dashboardData
	if !math.IsNaN(lat) && !math.IsNaN(lon) {
		wd, err := fetchWeather(lat, lon)
		if err != nil {
			// Fetch failed and there's no cached copy to fall back on (cold start +
			// upstream down). Render obviously-fake placeholder data rather than an
			// empty blob: an empty DataJSON makes the page fall back to its built-in
			// sample data, which carries debug:true and trips the margin/config-box
			// debug overlay.
			log.Printf("[weather] no data available (%v); rendering placeholder", err)
			wd = synthWeather()
		}
		data = buildDashboard(wd)
	}
	data.PhotosJSON = currentPhotoJSON() // server-selected photo, independent of weather
	data.ConfigJSON = loadConfigJSON()   // tunables applied via the preview panel
	data.StatsJSON = currentStatsJSON()  // Prometheus data for the left column, independent of weather
	_ = location                         // retained for future footer use
	return data
}

func handleDisplay(w http.ResponseWriter, r *http.Request, refreshRate int, tmplSrc, pngOut string, lat, lon float64, location string) {
	if !devMode {
		token := r.Header.Get("Access-Token")

		registryMu.Lock()
		d, ok := byKey[token]
		if !ok && token != "" {
			// Newer firmware (TRMNL X / FW 1.8.x) ships pre-provisioned with an
			// API token and never calls /api/setup — it polls /api/display
			// directly with that token. The original handshake assumed the
			// device would mint its key via /api/setup, so an unknown token got
			// a 202 ("not set up"), which is what drives the device to show its
			// built-in "visit trmnl.com/start" screen. On a personal LAN server
			// we trust the device on first contact: adopt the token it presents.
			mac := strings.ToLower(r.Header.Get("ID"))
			d = &Device{
				MAC:        mac,
				APIKey:     token,
				FriendlyID: randString(6),
				CreatedAt:  time.Now(),
			}
			byKey[token] = d
			if mac != "" {
				byMAC[mac] = d
			}
			ok = true
			log.Printf("[display] adopted pre-provisioned device mac=%s friendly_id=%s", mac, d.FriendlyID)
		}
		if ok {
			d.LastSeen = time.Now()
			d.BatteryVoltage = r.Header.Get("Battery-Voltage")
			d.RSSI = r.Header.Get("RSSI")
			d.FWVersion = r.Header.Get("FW-Version")
			saveRegistry()
		}
		registryMu.Unlock()

		if !ok {
			log.Printf("[display] empty token, returning 202")
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

	advancePhoto() // choose the photo for this serve (and the pre-renders until the next)

	// In dev/preview the render is driven on demand (the fswatch loop curls this
	// endpoint after each edit), so render synchronously here. In production the
	// background loop keeps dashboard.png fresh, so the device never waits on a
	// render — we just hand back the latest pre-rendered image instantly.
	if devMode {
		if err := renderHTML(tmplSrc, pngOut, currentDashboardData(lat, lon, location)); err != nil {
			log.Printf("[display] render error: %v", err)
			writeJSON(w, http.StatusOK, map[string]any{"status": 500})
			return
		}
	}

	ts := atomic.LoadInt64(&lastRenderUnix)
	imageURL := fmt.Sprintf("%s/dashboard.png?t=%d", baseURL(r), ts)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          0,
		"image_url":       imageURL,
		"filename":        fmt.Sprintf("dashboard-%d", ts),
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
	cfgFile     := flag.String("config", "config.json", "Chart tunables file (written by the preview panel's Apply button)")
	prom        := flag.String("prometheus", os.Getenv("TRMNL_PROMETHEUS_URL"), "Prometheus base URL for the left column's charts (or set TRMNL_PROMETHEUS_URL); empty → empty charts")
	photos      := flag.String("photos", "photos", "Directory of images to cycle through")
	uploads     := flag.String("uploads-url", os.Getenv("TRMNL_UPLOADS_URL"), "Photo uploader base URL, e.g. https://photos.pog.so (or set TRMNL_UPLOADS_URL; token in TRMNL_UPLOADS_TOKEN); empty → no uploads")
	uploadsD    := flag.String("uploads-dir", "uploads", "Where photos from the uploader are mirrored")
	renderEvery := flag.Int("render-interval", 300, "Seconds between background re-renders (production)")
	photoStrat  := flag.String("photo-strategy", "shuffle", "Photo cycling: random | shuffle | alphabetical")
	lat         := flag.Float64("lat", envFloat("TRMNL_LAT"), "Weather latitude (or set TRMNL_LAT)")
	lon         := flag.Float64("lon", envFloat("TRMNL_LON"), "Weather longitude (or set TRMNL_LON)")
	location    := flag.String("location", os.Getenv("TRMNL_LOCATION"), "Location name (or set TRMNL_LOCATION)")
	flag.Parse()

	devicesPath = *dp
	configPath = *cfgFile
	promURL = strings.TrimRight(*prom, "/")
	photosDir = *photos
	uploadsURL = strings.TrimRight(*uploads, "/")
	uploadsToken = os.Getenv("TRMNL_UPLOADS_TOKEN")
	uploadsDir = *uploadsD
	renderBaseURL = loopbackURL(*addr)
	photoStrategy = *photoStrat
	switch photoStrategy {
	case "random", "shuffle", "alphabetical":
	default:
		log.Printf("[photos] unknown strategy %q; using \"shuffle\"", photoStrategy)
		photoStrategy = "shuffle"
	}
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
	// Photos are read back into a <canvas> for dithering, so they must be
	// CORS-clean (the page renders from a file:// origin).
	photoFS := http.StripPrefix("/photos/", http.FileServer(http.Dir(photosDir)))
	mux.HandleFunc("GET /photos/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		photoFS.ServeHTTP(w, r)
	})
	mux.HandleFunc("GET /uploads/{file}", serveUpload)

	log.Printf("TRMNL BYOS server (html-stack)")
	log.Printf("  addr:           %s", *addr)
	log.Printf("  refresh-rate:   %ds", *refreshRate)
	if !math.IsNaN(*lat) {
		log.Printf("  location:       %.4f, %.4f (%s)", *lat, *lon, *location)
	} else {
		log.Printf("  location:       not configured (weather disabled)")
	}
	log.Printf("  template:       %s", *tmplSrc)
	if _, err := os.Stat(configPath); err == nil {
		log.Printf("  config file:    %s (applied)", configPath)
	} else {
		log.Printf("  config file:    %s (absent; using built-in defaults)", configPath)
	}
	if promURL != "" {
		log.Printf("  prometheus:     %s", promURL)
	} else {
		log.Printf("  prometheus:     not configured (left column charts empty)")
	}
	log.Printf("  devices file:   %s", devicesPath)
	log.Printf("  devices loaded: %d", len(byMAC))
	log.Printf("  photos:         %s", photosDir)
	if uploadsURL != "" && uploadsToken != "" {
		log.Printf("  uploads:        %s → %s (every %s)", uploadsURL, uploadsDir, uploadsEvery)
	} else {
		log.Printf("  uploads:        not configured")
	}
	log.Printf("  photo-strategy: %s", photoStrategy)
	if !devMode {
		log.Printf("  render-every:   %ds (pre-rendered; device polls never wait on a render)", *renderEvery)
	}

	// Bind the listener before the first render so the headless Chrome can fetch
	// /photos over HTTP during that initial render.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	go func() { log.Fatalf("serve: %v", http.Serve(ln, mux)) }()

	// Mirror the phone's uploads before the first render, then keep them fresh.
	if uploadsURL != "" && uploadsToken != "" {
		syncUploads()
		go func() {
			for range time.Tick(uploadsEvery) {
				syncUploads()
			}
		}()
	}
	log.Printf("  photos found:   %d", len(listPhotos()))

	if !devMode {
		renderOnce := func() {
			if err := renderHTML(*tmplSrc, *pngOut, currentDashboardData(*lat, *lon, *location)); err != nil {
				log.Printf("[render] %v", err)
			}
		}
		advancePhoto() // choose the first photo so the initial render isn't blank
		renderOnce()   // initial render so the very first device poll already has an image
		go func() {
			t := time.NewTicker(time.Duration(*renderEvery) * time.Second)
			defer t.Stop()
			for range t.C {
				renderOnce()
			}
		}()
	}

	// Block until a shutdown signal, then reap the headless Chrome child so it
	// doesn't leak across restarts. (serve + render run in goroutines.)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("shutting down (%s); tearing down headless Chrome", sig)
	done := make(chan struct{})
	go func() { shutdownChrome(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("chrome teardown timed out; exiting anyway")
	}
}
