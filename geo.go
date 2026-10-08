package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Driving distances between route stops. Places are geocoded with Nominatim
// and legs are routed with OSRM. Both public services allow about one request
// per second, so outbound calls are serialised and answers are cached.
// Itinerary stops are cached on disk; the free-text start is visitor input and
// is cached in memory only, so typing cannot grow the file.

const (
	defaultGeocodeURL = "https://nominatim.openstreetmap.org/search"
	defaultRouteURL   = "https://router.project-osrm.org/route/v1/driving"
	geoCallInterval   = 1100 * time.Millisecond
	geoCallTimeout    = 10 * time.Second
	geoWarmTimeout    = 10 * time.Minute
	maxGeoCache       = 20000 // disk entries per map; only stored itinerary stops reach it
	maxMemGeoCache    = 2000  // memory entries per map; reset when full
)

// Leg is the driving distance from one point to the next.
type Leg struct {
	Meters  int `json:"meters"`
	Seconds int `json:"seconds"`
}

type point struct {
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Name string  `json:"name"` // what the geocoder matched, so a wrong match is visible in the UI
}

// Route is the answer for the points [start?, stops...].
type Route struct {
	Legs     []*Leg   `json:"legs"`     // len(points)-1; nil when either end is unknown or unroutable
	Places   []string `json:"places"`   // matched name per point; "" when not found
	Complete bool     `json:"complete"` // false when a lookup failed or ran out of time; retrying may fill the gaps
}

// geoCache stores negative answers as null so unknown places are not retried.
type geoCache struct {
	Places map[string]*point `json:"places"`
	Legs   map[string]*Leg   `json:"legs"`
}

func newGeoCache() geoCache {
	return geoCache{Places: map[string]*point{}, Legs: map[string]*Leg{}}
}

type Geo struct {
	client     *http.Client
	geocodeURL string
	routeURL   string
	userAgent  string
	path       string
	interval   time.Duration

	slot     chan struct{} // one outbound call at a time; a channel so waiting honours ctx
	lastCall time.Time     // guarded by slot

	warmQueue chan *Itinerary

	saveMu sync.Mutex // orders file writes; taken before mu
	mu     sync.Mutex
	disk   geoCache
	mem    geoCache
	dirty  bool
}

// NewGeo loads the cache at path. A missing or unreadable cache starts empty:
// it only holds data that can be fetched again.
func NewGeo(path, userAgent string) *Geo {
	g := &Geo{
		client:     &http.Client{Timeout: geoCallTimeout},
		geocodeURL: defaultGeocodeURL,
		routeURL:   defaultRouteURL,
		userAgent:  userAgent,
		path:       path,
		interval:   geoCallInterval,
		slot:       make(chan struct{}, 1),
		warmQueue:  make(chan *Itinerary, 32),
		disk:       newGeoCache(),
		mem:        newGeoCache(),
	}
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &g.disk)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("geo cache %s ignored: %v", path, err)
	}
	if g.disk.Places == nil || g.disk.Legs == nil {
		g.disk = newGeoCache()
	}
	return g
}

// Route measures the legs between [start, stops...]; start may be empty.
func (g *Geo) Route(ctx context.Context, start string, stops []string) Route {
	type pt struct {
		name    string
		persist bool
	}
	pts := make([]pt, 0, len(stops)+1)
	if start != "" {
		pts = append(pts, pt{start, false})
	}
	for _, s := range stops {
		pts = append(pts, pt{s, true})
	}
	r := Route{Legs: []*Leg{}, Places: make([]string, len(pts)), Complete: true}
	if len(pts) < 2 {
		return r
	}
	found := make([]*point, len(pts))
	for i, p := range pts {
		var ok bool
		if found[i], ok = g.locate(ctx, p.name, p.persist); !ok {
			r.Complete = false
		}
		if found[i] != nil {
			r.Places[i] = found[i].Name
		}
	}
	r.Legs = make([]*Leg, len(pts)-1)
	for i := range r.Legs {
		if found[i] != nil && found[i+1] != nil {
			var ok bool
			if r.Legs[i], ok = g.leg(ctx, *found[i], *found[i+1], pts[i].persist && pts[i+1].persist); !ok {
				r.Complete = false
			}
		}
	}
	g.persist()
	return r
}

// Warm queues a new itinerary so its day routes are looked up before the
// first visitor asks. A full queue drops it; visitors then fill the cache.
func (g *Geo) Warm(it *Itinerary) {
	select {
	case g.warmQueue <- it:
	default:
		log.Printf("geo warm queue full, skipped itinerary %q", it.ID)
	}
}

// RunWarmer processes the warm queue one itinerary at a time until ctx ends.
func (g *Geo) RunWarmer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case it := <-g.warmQueue:
			g.warm(ctx, it)
		}
	}
}

func (g *Geo) warm(ctx context.Context, it *Itinerary) {
	ctx, cancel := context.WithTimeout(ctx, geoWarmTimeout)
	defer cancel()
	for _, d := range it.Days {
		g.Route(ctx, "", d.RouteStops())
	}
}

// StartCost counts the outbound calls a start would add to a route: geocoding
// the start and routing it to the first stop, when not cached.
func (g *Geo) StartCost(start, firstStop string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, known := g.cachedPlace(placeKey(start))
	switch {
	case !known:
		return 2
	case s == nil:
		return 0 // start not found: there is no leg from it
	}
	f, known := g.cachedPlace(placeKey(firstStop))
	switch {
	case !known:
		return 1
	case f == nil || (f.Lat == s.Lat && f.Lon == s.Lon):
		return 0
	}
	key := legKey(*s, *f)
	_, onDisk := g.disk.Legs[key]
	_, inMem := g.mem.Legs[key]
	if onDisk || inMem {
		return 0
	}
	return 1
}

// cachedPlace looks key up in both caches; mu must be held.
func (g *Geo) cachedPlace(key string) (*point, bool) {
	if p, ok := g.disk.Places[key]; ok {
		return p, true
	}
	p, ok := g.mem.Places[key]
	return p, ok
}

func legKey(a, b point) string { return fmt.Sprintf("%.5f,%.5f;%.5f,%.5f", a.Lat, a.Lon, b.Lat, b.Lon) }

func placeKey(place string) string { return strings.ToLower(strings.Join(strings.Fields(place), " ")) }

// locate returns the point for place, nil when unknown. ok is false when the
// lookup failed and was not cached, so a retry may succeed.
func (g *Geo) locate(ctx context.Context, place string, persist bool) (*point, bool) {
	key := placeKey(place)
	g.mu.Lock()
	p, ok := g.cachedPlace(key)
	g.mu.Unlock()
	if ok {
		return p, true
	}
	var hits []struct {
		Lat, Lon    string
		DisplayName string `json:"display_name"`
	}
	q := url.Values{"q": {place}, "format": {"jsonv2"}, "limit": {"1"}}
	if err := g.get(ctx, g.geocodeURL+"?"+q.Encode(), &hits, false); err != nil {
		return nil, false
	}
	if len(hits) > 0 {
		lat, err1 := strconv.ParseFloat(hits[0].Lat, 64)
		lon, err2 := strconv.ParseFloat(hits[0].Lon, 64)
		if err1 == nil && err2 == nil {
			p = &point{lat, lon, shortName(hits[0].DisplayName)}
		}
	}
	g.mu.Lock()
	if c := g.cacheFor(persist); len(c.Places) < maxGeoCache {
		c.Places[key] = p
	}
	g.mu.Unlock()
	return p, true
}

// leg works like locate for the driving leg from a to b.
func (g *Geo) leg(ctx context.Context, a, b point, persist bool) (*Leg, bool) {
	if a.Lat == b.Lat && a.Lon == b.Lon {
		return &Leg{}, true
	}
	key := legKey(a, b)
	g.mu.Lock()
	l, ok := g.disk.Legs[key]
	if !ok {
		l, ok = g.mem.Legs[key]
	}
	g.mu.Unlock()
	if ok {
		return l, true
	}
	var res struct {
		Code   string
		Routes []struct{ Distance, Duration float64 }
	}
	u := fmt.Sprintf("%s/%.6f,%.6f;%.6f,%.6f?overview=false", g.routeURL, a.Lon, a.Lat, b.Lon, b.Lat)
	if err := g.get(ctx, u, &res, true); err != nil {
		return nil, false
	}
	switch {
	case res.Code == "Ok" && len(res.Routes) > 0:
		l = &Leg{Meters: int(res.Routes[0].Distance + 0.5), Seconds: int(res.Routes[0].Duration + 0.5)}
	case res.Code == "NoRoute" || res.Code == "NoSegment":
		// final answer: no road between the points, cache as nil
	default:
		log.Printf("geo route %s: unexpected code %q", key, res.Code)
		return nil, false
	}
	g.mu.Lock()
	if c := g.cacheFor(persist); len(c.Legs) < maxGeoCache {
		c.Legs[key] = l
	}
	g.mu.Unlock()
	return l, true
}

// cacheFor picks the disk or memory cache; mu must be held.
func (g *Geo) cacheFor(persist bool) *geoCache {
	if persist {
		g.dirty = true
		return &g.disk
	}
	if len(g.mem.Places) >= maxMemGeoCache || len(g.mem.Legs) >= maxMemGeoCache {
		g.mem = newGeoCache()
	}
	return &g.mem
}

func (g *Geo) persist() {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	g.mu.Lock()
	if !g.dirty {
		g.mu.Unlock()
		return
	}
	data, err := json.Marshal(g.disk)
	g.dirty = false
	g.mu.Unlock()

	if err == nil {
		err = writeAtomic(g.path, data)
	}
	if err != nil {
		log.Printf("geo cache %s not saved: %v", g.path, err)
		g.mu.Lock()
		g.dirty = true
		g.mu.Unlock()
	}
}

// get performs one rate-limited GET and decodes the JSON body into dst.
// Other statuses are errors and therefore never cached; OSRM answers
// "no route" with 400, so the route call accepts it.
func (g *Geo) get(ctx context.Context, u string, dst any, accept400 bool) error {
	select {
	case g.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-g.slot }()
	if wait := g.interval - time.Since(g.lastCall); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { g.lastCall = time.Now() }()

	began := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", g.userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := g.client.Do(req)
	if err == nil {
		defer res.Body.Close()
		if res.StatusCode == http.StatusOK || (accept400 && res.StatusCode == http.StatusBadRequest) {
			err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(dst)
		} else {
			err = fmt.Errorf("status %s", res.Status)
		}
	}
	if err != nil && ctx.Err() == nil {
		log.Printf("geo GET %s failed after %dms: %v", req.URL.Host+req.URL.Path, time.Since(began).Milliseconds(), err)
	}
	return err
}

// shortName keeps the first three parts of a Nominatim display_name
// ("Name, street, town, district, region, …").
func shortName(display string) string {
	parts := strings.Split(display, ", ")
	return strings.Join(parts[:min(3, len(parts))], ", ")
}
