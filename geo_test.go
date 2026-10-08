package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeGeo serves Nominatim /search and OSRM /route responses and counts calls.
// Any place resolves to a point derived from its name, except "Nowhere"
// (unknown), "Down" (503) and "Island" (OSRM answers NoRoute with 400, like the real server).
func fakeGeo(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.UserAgent() == "" {
			t.Error("missing User-Agent")
		}
		switch {
		case r.URL.Path == "/search":
			q := r.URL.Query().Get("q")
			switch q {
			case "Down":
				w.WriteHeader(http.StatusServiceUnavailable)
			case "Nowhere":
				w.Write([]byte("[]"))
			case "Island":
				json.NewEncoder(w).Encode([]map[string]string{{"lat": "10", "lon": "10", "display_name": "Island"}})
			default:
				h := fnv.New32a()
				h.Write([]byte(strings.ToLower(q)))
				off := float64(h.Sum32()%1000) / 1000
				json.NewEncoder(w).Encode([]map[string]string{{"lat": fmt.Sprintf("%.3f", 48+off), "lon": fmt.Sprintf("%.3f", 20+off), "display_name": q + ", 1, Town, District, Region"}})
			}
		case strings.HasPrefix(r.URL.Path, "/route/"):
			if strings.Contains(r.URL.Path, "10.000000,10.000000") {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"code": "NoRoute", "message": "Impossible route"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "routes": []map[string]float64{{"distance": 12345.6, "duration": 1080.4}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newFakeGeo(t *testing.T, upstream, path string) *Geo {
	t.Helper()
	g := NewGeo(path, "test-agent")
	g.geocodeURL, g.routeURL, g.interval = upstream+"/search", upstream+"/route/v1/driving", 0
	return g
}

func readCache(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGeoRouteCachesStopsOnDisk(t *testing.T) {
	upstream, calls := fakeGeo(t)
	path := filepath.Join(t.TempDir(), "geo-cache.json")
	g := newFakeGeo(t, upstream.URL, path)

	r := g.Route(context.Background(), "", []string{"Lake", "Nowhere", "Castle", "Castle"})
	if !r.Complete || len(r.Legs) != 3 || len(r.Places) != 4 {
		t.Fatalf("route = %+v", r)
	}
	if r.Places[0] != "Lake, 1, Town" || r.Places[1] != "" {
		t.Fatalf("places = %q", r.Places)
	}
	if r.Legs[0] != nil || r.Legs[1] != nil {
		t.Fatalf("legs touching an unknown place must be nil: %+v %+v", r.Legs[0], r.Legs[1])
	}
	if r.Legs[2] == nil || *r.Legs[2] != (Leg{}) {
		t.Fatalf("same place twice must be a zero leg: %+v", r.Legs[2])
	}
	if n := calls.Load(); n != 3 { // Lake, Nowhere, Castle; the zero leg costs nothing
		t.Fatalf("upstream calls = %d, want 3", n)
	}

	reloaded := newFakeGeo(t, upstream.URL, path)
	again := reloaded.Route(context.Background(), "", []string{" lake ", "Castle"})
	if again.Legs[0] == nil || *again.Legs[0] != (Leg{12346, 1080}) {
		t.Fatalf("lake→castle = %+v", again.Legs[0])
	}
	reloaded.Route(context.Background(), "", []string{"Lake", "Castle"})
	if n := calls.Load(); n != 4 { // only the new Lake→Castle route
		t.Fatalf("upstream calls = %d, want 4", n)
	}
}

func TestGeoStartIsCachedInMemoryOnly(t *testing.T) {
	upstream, calls := fakeGeo(t)
	path := filepath.Join(t.TempDir(), "geo-cache.json")
	g := newFakeGeo(t, upstream.URL, path)

	r := g.Route(context.Background(), "My Hotel", []string{"Lake", "Castle"})
	if !r.Complete || len(r.Legs) != 2 || r.Legs[0] == nil || r.Places[0] != "My Hotel, 1, Town" {
		t.Fatalf("route = %+v", r)
	}
	if got := g.StartCost("my  hotel", "Lake"); got != 0 {
		t.Fatalf("StartCost(cached start and leg) = %d", got)
	}
	if got := g.StartCost("Other", "Lake"); got != 2 {
		t.Fatalf("StartCost(new start) = %d", got)
	}
	if got := g.StartCost("my hotel", "Mill"); got != 1 {
		t.Fatalf("StartCost(cached start, new first stop) = %d", got)
	}
	if strings.Contains(strings.ToLower(readCache(t, path)), "hotel") {
		t.Fatalf("start leaked into the disk cache: %s", readCache(t, path))
	}
	before := calls.Load()
	g.Route(context.Background(), "My Hotel", []string{"Lake", "Castle"})
	if calls.Load() != before {
		t.Fatal("cached start hit upstream")
	}
}

func TestGeoRetriesFailuresAndCachesNoRoute(t *testing.T) {
	upstream, calls := fakeGeo(t)
	g := newFakeGeo(t, upstream.URL, filepath.Join(t.TempDir(), "geo-cache.json"))

	if r := g.Route(context.Background(), "Down", []string{"Lake"}); r.Complete || r.Legs[0] != nil {
		t.Fatalf("a failed lookup must make the route incomplete: %+v", r)
	}
	if g.StartCost("Down", "Lake") != 2 {
		t.Fatal("a failed lookup was cached")
	}
	for range 2 {
		r := g.Route(context.Background(), "", []string{"Lake", "Island"})
		if !r.Complete || r.Legs[0] != nil {
			t.Fatalf("NoRoute is a final answer: %+v", r)
		}
	}
	// Down, Lake, then Island + NoRoute once; the second run is fully cached.
	if n := calls.Load(); n != 4 {
		t.Fatalf("upstream calls = %d, want 4", n)
	}
}

func TestGeoStopsWhenContextEnds(t *testing.T) {
	upstream, calls := fakeGeo(t)
	g := newFakeGeo(t, upstream.URL, filepath.Join(t.TempDir(), "geo-cache.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := g.Route(ctx, "", []string{"Lake", "Castle"})
	if r.Complete || r.Legs[0] != nil || calls.Load() != 0 {
		t.Fatalf("route=%+v calls=%d", r, calls.Load())
	}
}

func TestGeoRouteNeedsTwoPoints(t *testing.T) {
	g := NewGeo(filepath.Join(t.TempDir(), "geo-cache.json"), "test-agent")
	for _, stops := range [][]string{nil, {"Lake"}} {
		if r := g.Route(context.Background(), "", stops); !r.Complete || len(r.Legs) != 0 {
			t.Fatalf("%v: %+v", stops, r)
		}
	}
}

func TestGeoConcurrentRoutes(t *testing.T) {
	upstream, _ := fakeGeo(t)
	g := newFakeGeo(t, upstream.URL, filepath.Join(t.TempDir(), "geo-cache.json"))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := []string{"", "Hotel A", "Hotel B"}[i%3]
			if r := g.Route(context.Background(), start, []string{"Lake", "Castle", "Mill"}); !r.Complete {
				t.Errorf("route %d incomplete", i)
			}
		}()
	}
	wg.Wait()
}

func TestGeoWarm(t *testing.T) {
	upstream, calls := fakeGeo(t)
	g := newFakeGeo(t, upstream.URL, filepath.Join(t.TempDir(), "geo-cache.json"))
	it := &Itinerary{Days: []Day{{Activities: []Activity{{Place: "Lake"}, {Place: "Castle"}}}, {Activities: []Activity{{Place: "Mill"}}}}}
	g.warm(context.Background(), it)
	warmed := calls.Load()
	if r := g.Route(context.Background(), "", it.Days[0].RouteStops()); !r.Complete || r.Legs[0] == nil || calls.Load() != warmed {
		t.Fatalf("warmed route not cached: %+v, calls %d→%d", r, warmed, calls.Load())
	}
}

func TestRouteStops(t *testing.T) {
	d := Day{Activities: []Activity{{Place: "A"}, {}, {Place: "A"}, {Place: "B"}, {Place: "A"}}}
	if got := d.RouteStops(); !slices.Equal(got, []string{"A", "B", "A"}) {
		t.Fatalf("RouteStops = %q", got)
	}
}

func TestShortName(t *testing.T) {
	for in, want := range map[string]string{
		"Hotel pre psov Podlesok, 135, Paňovce, okres Košice-okolie, Slovensko": "Hotel pre psov Podlesok, 135, Paňovce",
		"Dedinky, Slovensko": "Dedinky, Slovensko",
		"":                   "",
	} {
		if got := shortName(in); got != want {
			t.Errorf("shortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func newLegsServer(t *testing.T, api API) string {
	t.Helper()
	upstream, _ := fakeGeo(t)
	api.geo = newFakeGeo(t, upstream.URL, filepath.Join(t.TempDir(), "geo-cache.json"))
	srv, _ := newTestServerWith(t, api, SecurityConfig{})
	return srv.URL + "/api/v1/itineraries/slovensky-raj-1yo/days/"
}

func TestDayLegsEndpoint(t *testing.T) {
	base := newLegsServer(t, API{})

	code, body := do(t, "GET", base+"0/legs?"+url.Values{"start": {"Hotel"}}.Encode(), "")
	var got Route
	if code != 200 || json.Unmarshal(body, &got) != nil || !got.Complete || len(got.Legs) != 2 || got.Legs[1].Meters != 12346 || got.Places[0] != "Hotel, 1, Town" {
		t.Fatalf("legs: %d %s", code, body)
	}
	if code, body := do(t, "GET", base+"0/legs", ""); code != 200 || !strings.Contains(string(body), `"legs":[{`) {
		t.Fatalf("without start: %d %s", code, body)
	}
	for _, path := range []string{"9/legs", "-1/legs", "x/legs", "../nope/days/0/legs"} {
		if code, _ := do(t, "GET", base+path, ""); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}
	if code, _ := do(t, "GET", base+"0/legs?start="+strings.Repeat("x", maxTextRunes+1), ""); code != 400 {
		t.Errorf("long start: %d", code)
	}

	off, _ := newTestServerWith(t, API{}, SecurityConfig{})
	if code, _ := do(t, "GET", off.URL+"/api/v1/itineraries/slovensky-raj-1yo/days/0/legs", ""); code != 404 {
		t.Errorf("disabled: %d", code)
	}
}

func TestDayLegsLimitsNewStarts(t *testing.T) {
	root := newLegsServer(t, API{startLimiter: newRateLimiter(1, 2)})
	base := root + "0/legs?start="
	if code, _ := do(t, "GET", base+"Hotel+A", ""); code != 200 { // geocode + leg = the whole burst
		t.Fatalf("first start: %d", code)
	}
	if code, _ := do(t, "GET", root+"2/legs?start=Hotel+A", ""); code != 429 {
		t.Fatalf("a known start routed to another day's first stop must still be charged: %d", code)
	}
	req, _ := http.NewRequest("GET", base+"Hotel+B", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("second new start: %d retry-after=%q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if code, _ := do(t, "GET", base+"hotel+a", ""); code != 200 {
		t.Fatalf("cached start must not count: %d", code)
	}
	if code, _ := do(t, "GET", base, ""); code != 200 {
		t.Fatalf("no start must not count: %d", code)
	}
}

func TestDayLegsNeedsReadAccess(t *testing.T) {
	base := newLegsServer(t, API{token: "secret"})
	if code, _ := do(t, "GET", base+"0/legs", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := do(t, "GET", base+"0/legs", "", "Authorization", "Bearer secret"); code != 200 {
		t.Fatalf("with token: %d", code)
	}
}

func TestDayLegsGlobalStartCap(t *testing.T) {
	base := newLegsServer(t, API{startLimiter: newRateLimiter(60, 60), startGlobal: newRateLimiter(1, 2)}) + "0/legs?start="
	if code, _ := do(t, "GET", base+"Hotel+A", ""); code != 200 {
		t.Fatalf("first start: %d", code)
	}
	if code, _ := do(t, "GET", base+"Hotel+B", ""); code != 429 {
		t.Fatalf("global cap must apply even when the client has budget: %d", code)
	}
	if code, _ := do(t, "GET", base, ""); code != 200 {
		t.Fatalf("stops without a start are never capped: %d", code)
	}
}
