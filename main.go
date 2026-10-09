package main

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Templates and seed data are compiled into the binary: the image needs no
// extra files, and an empty mounted volume is initialised from the seed.
var (
	//go:embed templates/index.html
	templateFS embed.FS
	//go:embed data/itineraries.json
	seedData []byte
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dataFile := env("DATA_FILE", "data/itineraries.json")
	store, err := OpenStore(dataFile, seedData)
	if err != nil {
		return err
	}
	api := &API{
		store:     store,
		token:     os.Getenv("API_TOKEN"),
		readToken: os.Getenv("READ_API_TOKEN"),
		publicURL: strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
	}
	if env("DISTANCES", "true") != "false" {
		// Nominatim's usage policy asks for an identifying User-Agent.
		api.geo = NewGeo(filepath.Join(filepath.Dir(dataFile), "geo-cache.json"), strings.TrimSpace(appName+"/"+appVersion+" "+api.publicURL))
		if n := envInt("START_LOOKUPS_PER_MINUTE", 20); n > 0 {
			api.startLimiter = newRateLimiter(n, max(n/2, 1))
			api.startGlobal = newRateLimiter(startLookupsGlobalPerMinute, startLookupsGlobalPerMinute/2)
		}
		api.ipHeader = os.Getenv("CLIENT_IP_HEADER")
		if api.startLimiter != nil && api.ipHeader == "" {
			log.Print("START_LOOKUPS_PER_MINUTE is keyed by peer IP; behind a proxy set CLIENT_IP_HEADER or all visitors share one budget")
		}
	}
	sec := SecurityConfig{
		CORSOrigins:    parseOrigins(os.Getenv("CORS_ORIGINS")),
		ClientIPHeader: os.Getenv("CLIENT_IP_HEADER"),
		RatePerMinute:  envInt("RATE_LIMIT_PER_MINUTE", 120),
		RateBurst:      envInt("RATE_LIMIT_BURST", 30),
		BlockBots:      env("BLOCK_BOTS", "true") != "false",
	}
	handler, err := newHandler(store, api, sec)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              ":" + env("PORT", "9876"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if api.geo != nil {
		go api.geo.RunWarmer(ctx)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("listening on %s (%d itineraries, write auth: %t)", srv.Addr, len(store.List()), api.token != "")

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newHandler wires every route in one place.
func newHandler(store *Store, api *API, sec SecurityConfig) (http.Handler, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/index.html")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", indexHandler(store, api, tmpl))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /robots.txt", robotsTxt)

	mux.HandleFunc("GET /api/v1/openapi.json", api.openAPI)
	mux.HandleFunc("GET /api/v1/itineraries", api.requireUIRead(api.listItineraries))
	mux.HandleFunc("GET /api/v1/itineraries/{id}", api.requireUIRead(api.getItinerary))
	mux.HandleFunc("GET /api/v1/itineraries/{id}/days/{day}/legs", api.requireUIRead(api.getDayLegs))
	mux.HandleFunc("POST /api/v1/itineraries", api.requireToken(api.createItinerary))
	mux.HandleFunc("PATCH /api/v1/itineraries/{id}", api.requireToken(api.patchItinerary))
	mux.HandleFunc("DELETE /api/v1/itineraries/{id}", api.requireToken(api.deleteItinerary))
	mux.HandleFunc("POST /mcp", api.requireReadToken(api.mcp))
	mux.HandleFunc("POST /api/v1/itineraries/{id}/owner-link", api.requireToken(api.ownerLink))
	mux.HandleFunc("POST /api/v1/itineraries/{id}/owner-session", api.ownerSession)
	mux.HandleFunc("GET /api/v1/itineraries/{id}/checklist-access", api.checklistAccess)
	mux.HandleFunc("GET /api/v1/itineraries/{id}/checklist-share", api.shareChecklist)
	mux.HandleFunc("PATCH /api/v1/itineraries/{id}/checklist", api.editChecklist)

	return protect(sec, mux), nil
}

// indexHandler renders the shell with the list and first itinerary inlined,
// so the first paint needs no extra API round trip.
func indexHandler(store *Store, api *API, tmpl *template.Template) http.HandlerFunc {
	type bootData struct {
		List   []Summary  `json:"list"`
		Active *Itinerary `json:"active"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		data := bootData{List: store.List()}
		if len(data.List) > 0 {
			data.Active, _ = store.Get(data.List[0].ID)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			log.Printf("render index: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "private, no-cache") // sets the session cookie: never cache at the edge
		api.setSession(w, r)
		buf.WriteTo(w)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
