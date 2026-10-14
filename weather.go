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
	"strconv"
	"sync"
	"time"
)

// Daily weather for an itinerary day from Open-Meteo (open source, no API
// key). The day's place is geocoded through Geo, so only stored itinerary
// places are looked up; the visitor only chooses the date.

const (
	defaultWeatherURL = "https://api.open-meteo.com/v1/forecast"
	weatherTimeout    = 10 * time.Second
	weatherTTL        = time.Hour
	maxWeatherCache   = 2000 // entries; reset when full
	forecastDays      = 16   // Open-Meteo forecasts today plus 15 days
	// Uncached fetches of all clients together, well within Open-Meteo's free 10,000 calls a day.
	weatherFetchesPerMinute = 6
	weatherFetchBurst       = 60
)

// Weather statuses: only "ok" carries values.
const (
	weatherOK       = "ok"
	weatherNoPlace  = "no_place"  // the day has no stop and the itinerary no startLocation
	weatherNotFound = "not_found" // the place is unknown to OpenStreetMap
	weatherTooFar   = "too_far"   // beyond the forecast horizon; see availableFrom
	weatherPast     = "past"      // the day is over, so no forecast is looked up
	weatherDisabled = "disabled"  // WEATHER=false or DISTANCES=false
)

// DayWeather is the daily summary for one itinerary day.
type DayWeather struct {
	Date                string   `json:"date"`
	Status              string   `json:"status"`
	Place               string   `json:"place,omitempty"`         // what the geocoder matched
	AvailableFrom       string   `json:"availableFrom,omitempty"` // with too_far: first day the forecast exists
	WeatherCode         *int     `json:"weatherCode,omitempty"`   // WMO weather interpretation code
	TempMax             *float64 `json:"tempMax,omitempty"`       // °C
	TempMin             *float64 `json:"tempMin,omitempty"`
	PrecipitationMm     *float64 `json:"precipitationMm,omitempty"`
	PrecipitationChance *int     `json:"precipitationChance,omitempty"` // %
	WindMaxKmh          *float64 `json:"windMaxKmh,omitempty"`
	Sunrise             string   `json:"sunrise,omitempty"` // local time HH:MM
	Sunset              string   `json:"sunset,omitempty"`
}

type cachedWeather struct {
	w       DayWeather
	fetched time.Time
}

type weatherCall struct {
	done chan struct{}
	w    DayWeather
	err  error
}

type Weather struct {
	geo       *Geo
	client    *http.Client
	url       string
	userAgent string
	now       func() time.Time
	limit     *rateLimiter

	mu       sync.Mutex
	cache    map[string]cachedWeather
	inFlight map[string]*weatherCall
}

func NewWeather(geo *Geo, userAgent string) *Weather {
	return &Weather{
		geo:       geo,
		client:    &http.Client{Timeout: weatherTimeout},
		url:       defaultWeatherURL,
		userAgent: userAgent,
		now:       time.Now,
		limit:     newRateLimiter(weatherFetchesPerMinute, weatherFetchBurst),
		cache:     map[string]cachedWeather{},
		inFlight:  map[string]*weatherCall{},
	}
}

// ErrWeatherUnavailable means the lookup failed and a retry may succeed.
var ErrWeatherUnavailable = errors.New("weather is temporarily unavailable")

// ErrWeatherLimited means too many uncached lookups; retry later.
var ErrWeatherLimited = errors.New("too many new weather lookups, try again later")

// Day returns the weather at place on date (YYYY-MM-DD). allow charges the
// caller's budget for an uncached fetch.
func (wx *Weather) Day(ctx context.Context, place, date string, allow func() bool) (DayWeather, error) {
	res := DayWeather{Date: date}
	day, err := time.Parse(dateLayout, date)
	if err != nil {
		return res, invalid("date must be YYYY-MM-DD")
	}
	// Compare in UTC with a day of slack: the forecast follows the place's local date.
	today := wx.now().UTC().Truncate(24 * time.Hour)
	switch {
	case day.Before(today.AddDate(0, 0, -1)):
		res.Status = weatherPast
		return res, nil
	case place == "":
		res.Status = weatherNoPlace
		return res, nil
	case day.After(today.AddDate(0, 0, forecastDays-2)):
		res.Status = weatherTooFar
		res.AvailableFrom = day.AddDate(0, 0, -(forecastDays - 2)).Format(dateLayout)
		return res, nil
	}
	wx.geo.mu.Lock()
	_, known := wx.geo.cachedPlace(placeKey(place))
	wx.geo.mu.Unlock()
	if !known && !allow() {
		return res, ErrWeatherLimited
	}
	p, ok := wx.geo.locate(ctx, place, true)
	wx.geo.persist()
	if !ok {
		return res, ErrWeatherUnavailable
	}
	if p == nil {
		res.Status = weatherNotFound
		return res, nil
	}
	res, err = wx.forecast(ctx, *p, date, allow)
	res.Place = p.Name
	return res, err
}

func (wx *Weather) forecast(ctx context.Context, p point, date string, allow func() bool) (DayWeather, error) {
	key := fmt.Sprintf("%.4f,%.4f,%s", p.Lat, p.Lon, date)
	wx.mu.Lock()
	c, hit := wx.cache[key]
	if hit && wx.now().Sub(c.fetched) < weatherTTL {
		wx.mu.Unlock()
		return c.w, nil
	}
	if call, ok := wx.inFlight[key]; ok {
		wx.mu.Unlock()
		select {
		case <-call.done:
			return call.w, call.err
		case <-ctx.Done():
			return DayWeather{}, ctx.Err()
		}
	}
	call := &weatherCall{done: make(chan struct{})}
	wx.inFlight[key] = call
	wx.mu.Unlock()

	call.w, call.err = wx.fetchAllowed(ctx, p, date, allow)
	wx.mu.Lock()
	if call.err == nil {
		if len(wx.cache) >= maxWeatherCache {
			wx.cache = map[string]cachedWeather{}
		}
		wx.cache[key] = cachedWeather{call.w, wx.now()}
	}
	delete(wx.inFlight, key)
	close(call.done)
	wx.mu.Unlock()
	return call.w, call.err
}

func (wx *Weather) fetchAllowed(ctx context.Context, p point, date string, allow func() bool) (DayWeather, error) {
	if !allow() {
		return DayWeather{}, ErrWeatherLimited
	}
	if ok, _ := wx.limit.allow("*", wx.now()); !ok {
		return DayWeather{}, ErrWeatherLimited
	}
	return wx.fetch(ctx, p, date)
}

func (wx *Weather) fetch(ctx context.Context, p point, date string) (DayWeather, error) {
	q := url.Values{
		"latitude":   {strconv.FormatFloat(p.Lat, 'f', 4, 64)},
		"longitude":  {strconv.FormatFloat(p.Lon, 'f', 4, 64)},
		"daily":      {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,wind_speed_10m_max,sunrise,sunset"},
		"timezone":   {"auto"},
		"start_date": {date},
		"end_date":   {date},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wx.url+"?"+q.Encode(), nil)
	if err != nil {
		return DayWeather{}, err
	}
	req.Header.Set("User-Agent", wx.userAgent)
	req.Header.Set("Accept", "application/json")
	res, err := wx.client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // url.Error's message repeats the coordinates
		}
		log.Printf("weather lookup failed: %v", err)
		return DayWeather{}, ErrWeatherUnavailable
	}
	defer res.Body.Close()
	var body struct {
		Reason string `json:"reason"`
		Daily  struct {
			Time                        []string   `json:"time"`
			WeatherCode                 []*int     `json:"weather_code"`
			Temperature2mMax            []*float64 `json:"temperature_2m_max"`
			Temperature2mMin            []*float64 `json:"temperature_2m_min"`
			PrecipitationSum            []*float64 `json:"precipitation_sum"`
			PrecipitationProbabilityMax []*int     `json:"precipitation_probability_max"`
			WindSpeed10mMax             []*float64 `json:"wind_speed_10m_max"`
			Sunrise                     []string   `json:"sunrise"`
			Sunset                      []string   `json:"sunset"`
		} `json:"daily"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil || res.StatusCode != http.StatusOK {
		log.Printf("weather lookup failed: status %s %s %v", res.Status, body.Reason, err)
		return DayWeather{}, ErrWeatherUnavailable
	}
	d := body.Daily
	if len(d.Time) == 0 || d.Time[0] != date {
		log.Printf("weather lookup: no daily values for the requested date")
		return DayWeather{}, ErrWeatherUnavailable
	}
	return DayWeather{
		Date:                date,
		Status:              weatherOK,
		WeatherCode:         first(d.WeatherCode),
		TempMax:             first(d.Temperature2mMax),
		TempMin:             first(d.Temperature2mMin),
		PrecipitationMm:     first(d.PrecipitationSum),
		PrecipitationChance: first(d.PrecipitationProbabilityMax),
		WindMaxKmh:          first(d.WindSpeed10mMax),
		Sunrise:             clockTime(d.Sunrise),
		Sunset:              clockTime(d.Sunset),
	}, nil
}

func first[T any](values []*T) *T {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

// clockTime keeps HH:MM from Open-Meteo's local ISO time "2026-10-15T06:58".
func clockTime(values []string) string {
	if len(values) == 0 {
		return ""
	}
	if t, err := time.Parse("2006-01-02T15:04", values[0]); err == nil {
		return t.Format("15:04")
	}
	return ""
}
