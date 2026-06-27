// Command gentides fetches high/low tide predictions from the WorldTides API
// and bakes them into a tides.json the TRMNL server reads at render time.
//
// Why bake a file instead of fetching live? Tides aren't computable from a
// latitude/longitude the way sunrise is — they depend on local harmonic
// constants — so the practical approach is to download predictions once and
// store them. WorldTides gives 100 free credits on signup and charges 1 credit
// per 7 days of extremes, so a single run can cover a year or two for free.
//
// The output is gitignored and per-location: drop it next to the server (or
// mount it into the container at /data/tides.json) and the tide panel appears;
// delete it and the panel hides.
//
// Usage:
//
//	export WORLDTIDES_KEY=xxxxxxxx   # free key: https://www.worldtides.info/register
//	go run ./tools/gentides -lat 51.0795 -lon 1.1830 -location Folkestone -days 365
//
// This package is intentionally separate from the server (its own main), so it
// never enters the server binary or the Docker image — it's a dev-time tool.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"time"
)

// worldTidesResp is the subset of the WorldTides v3 "extremes" response we use.
type worldTidesResp struct {
	Status        int    `json:"status"`
	Error         string `json:"error"`
	ResponseDatum string `json:"responseDatum"`
	Extremes      []struct {
		Dt     int64   `json:"dt"`     // unix seconds, UTC
		Height float64 `json:"height"` // metres above responseDatum
		Type   string  `json:"type"`   // "High" | "Low"
	} `json:"extremes"`
}

// tideEvent is one row of the on-disk schema the server reads (see server.go's
// tidesFile): RFC3339 time in the device's local zone, "high"/"low", metres.
type tideEvent struct {
	Time   string  `json:"time"`
	Type   string  `json:"type"`
	Height float64 `json:"height"`
}

type tidesFile struct {
	Location  string      `json:"location"`
	Generated string      `json:"generated"`
	Source    string      `json:"source"`
	Datum     string      `json:"datum"`
	Tides     []tideEvent `json:"tides"`
}

func main() {
	key := flag.String("key", os.Getenv("WORLDTIDES_KEY"), "WorldTides API key (or set WORLDTIDES_KEY). Free key: https://www.worldtides.info/register")
	lat := flag.Float64("lat", 51.0795, "Latitude (default: Folkestone)")
	lon := flag.Float64("lon", 1.1830, "Longitude (default: Folkestone)")
	location := flag.String("location", "Folkestone", "Location label stored in the file")
	tz := flag.String("tz", "Europe/London", "IANA timezone the tide times are written in (use the device's local zone)")
	days := flag.Int("days", 365, "Days of tides to fetch (1 WorldTides credit per 7 days)")
	startStr := flag.String("start", "", "Start date YYYY-MM-DD (default: today in -tz)")
	datum := flag.String("datum", "LAT", "Tidal datum: LAT → positive heights (UK chart datum); MSL → low tides go negative; empty omits the parameter")
	out := flag.String("out", "tides.json", "Output file")
	flag.Parse()

	if *key == "" {
		log.Fatal("no API key: pass -key or set WORLDTIDES_KEY (free key at https://www.worldtides.info/register)")
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		log.Fatalf("invalid -tz %q: %v", *tz, err)
	}

	var start time.Time
	if *startStr == "" {
		now := time.Now().In(loc)
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	} else if start, err = time.ParseInLocation("2006-01-02", *startStr, loc); err != nil {
		log.Fatalf("invalid -start %q (want YYYY-MM-DD): %v", *startStr, err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	seen := make(map[int64]bool)
	var events []tideEvent

	// WorldTides bills per 7-day block, so fetch in 7-day chunks: predictable
	// cost (1 credit each) and no risk of tripping a per-request day cap.
	for d := 0; d < *days; d += 7 {
		chunk := min(*days-d, 7)
		date := start.AddDate(0, 0, d).Format("2006-01-02")
		resp, err := fetchExtremes(client, *key, *lat, *lon, date, chunk, *datum)
		if err != nil {
			log.Fatalf("fetch %s (+%dd): %v", date, chunk, err)
		}
		added := 0
		for _, e := range resp.Extremes {
			if seen[e.Dt] {
				continue // de-dup the seam between contiguous chunks
			}
			seen[e.Dt] = true
			typ := "high"
			if e.Type == "Low" || e.Type == "low" {
				typ = "low"
			}
			events = append(events, tideEvent{
				Time:   time.Unix(e.Dt, 0).In(loc).Format(time.RFC3339),
				Type:   typ,
				Height: e.Height,
			})
			added++
		}
		log.Printf("  %s +%dd → %d extremes (datum %s)", date, chunk, added, resp.ResponseDatum)
		time.Sleep(250 * time.Millisecond) // be polite to the API
	}

	if len(events) == 0 {
		log.Fatal("no tide events returned — check the coordinates are over water and the datum is valid for this point")
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Time < events[j].Time })

	file := tidesFile{
		Location:  *location,
		Generated: time.Now().In(loc).Format(time.RFC3339),
		Source:    "worldtides.info",
		Datum:     *datum,
		Tides:     events,
	}
	buf, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(*out, append(buf, '\n'), 0644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %d tide events to %s (%s, %d days from %s)",
		len(events), *out, *location, *days, start.Format("2006-01-02"))
}

// fetchExtremes calls the WorldTides v3 extremes endpoint for a [date, date+days)
// window and returns the parsed response, surfacing API-level errors (bad key,
// out of credits, invalid datum) as Go errors.
func fetchExtremes(c *http.Client, key string, lat, lon float64, date string, days int, datum string) (*worldTidesResp, error) {
	q := url.Values{}
	q.Set("lat", fmt.Sprintf("%.4f", lat))
	q.Set("lon", fmt.Sprintf("%.4f", lon))
	q.Set("date", date)
	q.Set("days", fmt.Sprintf("%d", days))
	if datum != "" {
		q.Set("datum", datum)
	}
	q.Set("key", key)
	// "extremes" is a presence flag, emitted bare (no "=") ahead of the rest.
	u := "https://www.worldtides.info/api/v3?extremes&" + q.Encode()

	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var wr worldTidesResp
	if err := json.NewDecoder(resp.Body).Decode(&wr); err != nil {
		return nil, fmt.Errorf("decode (HTTP %s): %w", resp.Status, err)
	}
	if wr.Status != 200 {
		if wr.Error != "" {
			return nil, fmt.Errorf("API status %d: %s", wr.Status, wr.Error)
		}
		return nil, fmt.Errorf("API status %d (HTTP %s)", wr.Status, resp.Status)
	}
	return &wr, nil
}
