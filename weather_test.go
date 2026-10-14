package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var weatherToday = time.Date(2026, 7, 1, 15, 0, 0, 0, time.UTC)

// fakeOpenMeteo answers like the Open-Meteo forecast API for the requested day.
func fakeOpenMeteo(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("latitude") == "" || q.Get("longitude") == "" || q.Get("timezone") != "auto" || q.Get("start_date") != q.Get("end_date") {
			t.Errorf("unexpected query %s", r.URL.RawQuery)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":true,"reason":"down"}`))
			return
		}
		date := q.Get("start_date")
		json.NewEncoder(w).Encode(map[string]any{"daily": map[string]any{
			"time": []string{date}, "weather_code": []int{61}, "temperature_2m_max": []float64{24.5}, "temperature_2m_min": []float64{12.1},
			"precipitation_sum": []float64{3.2}, "precipitation_probability_max": []int{70}, "wind_speed_10m_max": []float64{18},
			"sunrise": []string{date + "T05:12"}, "sunset": []string{date + "T20:48"},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newWeatherServer(t *testing.T, api API, status int) (string, *atomic.Int32) {
	t.Helper()
	geoUpstream, _ := fakeGeo(t)
	wxUpstream, calls := fakeOpenMeteo(t, status)
	api.geo = newFakeGeo(t, geoUpstream.URL, filepath.Join(t.TempDir(), "geo-cache.json"))
	api.weather = NewWeather(api.geo, "test-agent")
	api.weather.url, api.weather.now = wxUpstream.URL, func() time.Time { return weatherToday }
	srv, _ := newTestServerWith(t, api, SecurityConfig{})
	return srv.URL + "/api/v1/itineraries/slovensky-raj-1yo/days/", calls
}

func decodeWeather(t *testing.T, body []byte) DayWeather {
	t.Helper()
	var w DayWeather
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return w
}

func cacheControl(t *testing.T, url string) string {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.Header.Get("Cache-Control")
}

func TestDayWeatherEndpoint(t *testing.T) {
	base, calls := newWeatherServer(t, API{}, http.StatusOK)

	code, body := do(t, "GET", base+"1/weather?date=2026-07-03", "")
	if code != 200 {
		t.Fatalf("weather: %d %s", code, body)
	}
	w := decodeWeather(t, body)
	if w.Status != weatherOK || w.Date != "2026-07-03" || !strings.HasPrefix(w.Place, "Stratenský kaňon parkovisko") ||
		*w.WeatherCode != 61 || *w.TempMax != 24.5 || *w.TempMin != 12.1 || *w.PrecipitationChance != 70 || w.Sunrise != "05:12" || w.Sunset != "20:48" {
		t.Fatalf("weather: %+v", w)
	}
	if do(t, "GET", base+"1/weather?date=2026-07-03", ""); calls.Load() != 1 {
		t.Fatalf("repeat must be cached, upstream calls: %d", calls.Load())
	}
	if cc := cacheControl(t, base+"1/weather?date=2026-07-03"); cc != "private, max-age=3600" {
		t.Errorf("fresh answer Cache-Control: %q", cc)
	}
	if cc := cacheControl(t, base+"9/weather?date=2026-07-03"); cc != "no-store" {
		t.Errorf("error Cache-Control: %q", cc)
	}

	for date, from := range map[string]string{"2026-07-30": "2026-07-16", "2026-07-16": "2026-07-02"} {
		_, body = do(t, "GET", base+"0/weather?date="+date, "")
		if w := decodeWeather(t, body); w.Status != weatherTooFar || w.AvailableFrom != from {
			t.Fatalf("too far %s: %+v", date, w)
		}
	}
	for _, date := range []string{"2025-01-01", "2026-06-29"} {
		_, body = do(t, "GET", base+"0/weather?date="+date, "")
		if w := decodeWeather(t, body); w.Status != weatherPast {
			t.Fatalf("past %s: %+v", date, w)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("out-of-range dates must not reach upstream: %d", calls.Load())
	}
	// Yesterday in UTC may still be today at the place.
	_, body = do(t, "GET", base+"0/weather?date=2026-06-30", "")
	if w := decodeWeather(t, body); w.Status != weatherOK {
		t.Fatalf("yesterday in UTC: %+v", w)
	}

	for _, path := range []string{"0/weather", "0/weather?date=03.07.2026", "0/weather?date=2026-02-30"} {
		if code, _ := do(t, "GET", base+path, ""); code != 400 {
			t.Errorf("%s: %d", path, code)
		}
	}
	for _, path := range []string{"9/weather?date=2026-07-03", "-1/weather?date=2026-07-03"} {
		if code, _ := do(t, "GET", base+path, ""); code != 404 {
			t.Errorf("%s: %d", path, code)
		}
	}

	off, _ := newTestServerWith(t, API{}, SecurityConfig{})
	code, body = do(t, "GET", off.URL+"/api/v1/itineraries/slovensky-raj-1yo/days/0/weather?date=2026-07-03", "")
	if w := decodeWeather(t, body); code != 200 || w.Status != weatherDisabled {
		t.Errorf("disabled: %d %+v", code, w)
	}
	if cc := cacheControl(t, off.URL+"/api/v1/itineraries/slovensky-raj-1yo/days/0/weather?date=2026-07-03"); cc != "no-store" {
		t.Errorf("disabled Cache-Control: %q", cc)
	}
	if code, _ := do(t, "GET", off.URL+"/api/v1/itineraries/slovensky-raj-1yo/days/9/weather?date=2026-07-03", ""); code != 404 {
		t.Errorf("disabled, missing day: %d", code)
	}
}

func TestDayWeatherLimitsUncachedLookups(t *testing.T) {
	base, calls := newWeatherServer(t, API{startLimiter: newRateLimiter(1, 2)}, http.StatusOK)
	if code, _ := do(t, "GET", base+"0/weather?date=2026-07-03", ""); code != 200 {
		t.Fatalf("first lookup: %d", code)
	}
	if code, _ := do(t, "GET", base+"0/weather?date=2026-07-04", ""); code != 429 {
		t.Fatalf("second uncached lookup: %d", code)
	}
	if code, _ := do(t, "GET", base+"0/weather?date=2026-07-03", ""); code != 200 || calls.Load() != 1 {
		t.Fatalf("cached answers stay free: %d, upstream calls %d", code, calls.Load())
	}
}

func TestDayWeatherUsesStoredDatesAndPlaces(t *testing.T) {
	base, _ := newWeatherServer(t, API{}, http.StatusOK)
	root := strings.TrimSuffix(base, "/slovensky-raj-1yo/days/")
	patch := `{"startDate":"2026-07-02","startLocation":"Hotel","days":[{"label":"A","activities":[{"time":"9","title":"x"}]},{"label":"B","date":"2026-07-10","activities":[{"time":"9","title":"y","place":"Lake"}]}]}`
	if code, body := do(t, "PATCH", root+"/slovensky-raj-1yo", patch); code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	_, body := do(t, "GET", base+"0/weather", "")
	if w := decodeWeather(t, body); w.Date != "2026-07-02" || !strings.HasPrefix(w.Place, "Hotel") {
		t.Fatalf("day without stops uses startDate and startLocation: %+v", w)
	}
	_, body = do(t, "GET", base+"1/weather", "")
	if w := decodeWeather(t, body); w.Date != "2026-07-10" || !strings.HasPrefix(w.Place, "Lake") {
		t.Fatalf("day date and first stop win: %+v", w)
	}

	if code, _ := do(t, "PATCH", root+"/slovensky-raj-1yo", `{"startDate":"July 2"}`); code != 400 {
		t.Fatalf("invalid startDate accepted: %d", code)
	}
	if code, _ := do(t, "PATCH", root+"/slovensky-raj-1yo", `{"startLocation":"","days":[{"activities":[{"time":"9","title":"x"}]}]}`); code != 200 {
		t.Fatalf("clear places: %d", code)
	}
	_, body = do(t, "GET", base+"0/weather", "")
	if w := decodeWeather(t, body); w.Status != weatherNoPlace {
		t.Fatalf("no place: %+v", w)
	}
}

func TestDayWeatherUpstreamFailureIsRetryable(t *testing.T) {
	base, calls := newWeatherServer(t, API{}, http.StatusServiceUnavailable)
	for range 2 {
		req, _ := http.NewRequest("GET", base+"0/weather?date=2026-07-03", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 503 || res.Header.Get("Retry-After") == "" {
			t.Fatalf("upstream down: %d", res.StatusCode)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("failures must not be cached: %d", calls.Load())
	}
}

func TestDayWeatherNeedsReadAccess(t *testing.T) {
	base, _ := newWeatherServer(t, API{token: "secret"}, http.StatusOK)
	if code, _ := do(t, "GET", base+"0/weather?date=2026-07-03", ""); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := do(t, "GET", base+"0/weather?date=2026-07-03", "", "Authorization", "Bearer secret"); code != 200 {
		t.Fatalf("with token: %d", code)
	}
}

func TestWeatherExhaustedBudgetAvoidsGeocoding(t *testing.T) {
	upstream, calls := fakeGeo(t)
	g := newFakeGeo(t, upstream.URL, filepath.Join(t.TempDir(), "geo.json"))
	wx := NewWeather(g, "test-agent")
	wx.now = func() time.Time { return weatherToday }
	_, err := wx.Day(context.Background(), "Lake", "2026-07-03", func() bool { return false })
	if !errors.Is(err, ErrWeatherLimited) || calls.Load() != 0 {
		t.Fatalf("exhausted budget: err=%v, geocode calls=%d", err, calls.Load())
	}
}

func TestWeatherCoalescesConcurrentForecasts(t *testing.T) {
	g := NewGeo("", "test-agent")
	g.disk.Places[placeKey("Lake")] = &point{Lat: 48, Lon: 20, Name: "Lake"}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		w.Write([]byte(`{"daily":{"time":["2026-07-03"],"temperature_2m_max":[25]}}`))
	}))
	t.Cleanup(upstream.Close)
	wx := NewWeather(g, "test-agent")
	wx.url, wx.now = upstream.URL, func() time.Time { return weatherToday }
	var charges atomic.Int32
	done := make(chan error, 2)
	request := func() {
		_, err := wx.Day(context.Background(), "Lake", "2026-07-03", func() bool { charges.Add(1); return true })
		done <- err
	}
	go request()
	<-entered
	go request()
	select {
	case <-entered:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || charges.Load() != 1 {
		t.Fatalf("same forecast: upstream calls=%d, budget charges=%d; want 1 each", calls.Load(), charges.Load())
	}
}

func TestWeatherWaiterCancellationDoesNotBlockOtherDates(t *testing.T) {
	g := NewGeo("", "test-agent")
	g.disk.Places[placeKey("Lake")] = &point{Lat: 48, Lon: 20, Name: "Lake"}
	entered, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("start_date")
		if date == "2026-07-03" {
			close(entered)
			<-release
		}
		json.NewEncoder(w).Encode(map[string]any{"daily": map[string]any{"time": []string{date}}})
	}))
	t.Cleanup(upstream.Close)
	defer close(release)
	wx := NewWeather(g, "test-agent")
	wx.url, wx.now = upstream.URL, func() time.Time { return weatherToday }
	first := make(chan error, 1)
	go func() {
		_, err := wx.Day(context.Background(), "Lake", "2026-07-03", func() bool { return true })
		first <- err
	}()
	t.Cleanup(func() {
		if err := <-first; err != nil {
			t.Errorf("original lookup: %v", err)
		}
	})
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wx.Day(ctx, "Lake", "2026-07-03", func() bool { t.Error("waiter charged"); return false }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if w, err := wx.Day(ctx, "Lake", "2026-07-04", func() bool { return true }); err != nil || w.Status != weatherOK {
		t.Fatalf("unrelated date blocked: %+v, %v", w, err)
	}
}

func TestWeatherCacheExpiresAndSharesCoordinates(t *testing.T) {
	g := NewGeo("", "test-agent")
	g.disk.Places[placeKey("Lake")] = &point{Lat: 48, Lon: 20, Name: "Lake"}
	g.disk.Places[placeKey("Shore")] = &point{Lat: 48, Lon: 20, Name: "Shore"}
	upstream, calls := fakeOpenMeteo(t, http.StatusOK)
	wx := NewWeather(g, "test-agent")
	now := weatherToday
	wx.url, wx.now = upstream.URL, func() time.Time { return now }
	if _, err := wx.Day(context.Background(), "Lake", "2026-07-03", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(weatherTTL - time.Second)
	if w, err := wx.Day(context.Background(), "Shore", "2026-07-03", func() bool { return false }); err != nil || w.Place != "Shore" || calls.Load() != 1 ||
		!w.expires.Equal(weatherToday.Add(weatherTTL)) {
		t.Fatalf("cached alias keeps its first expiry: %+v, err=%v, calls=%d", w, err, calls.Load())
	}
	now = now.Add(time.Second)
	if _, err := wx.Day(context.Background(), "Lake", "2026-07-03", func() bool { return true }); err != nil || calls.Load() != 2 {
		t.Fatalf("expired cache: err=%v, calls=%d", err, calls.Load())
	}
}
