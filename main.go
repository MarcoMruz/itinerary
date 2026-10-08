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
	store, err := OpenStore(env("DATA_FILE", "data/itineraries.json"), seedData)
	if err != nil {
		return err
	}
	api := &API{
		store:     store,
		token:     os.Getenv("API_TOKEN"),
		publicURL: strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
	}
	handler, err := newHandler(store, api)
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
func newHandler(store *Store, api *API) (http.Handler, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/index.html")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", indexHandler(store, tmpl))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("GET /api/v1/openapi.json", api.openAPI)
	mux.HandleFunc("GET /api/v1/itineraries", api.listItineraries)
	mux.HandleFunc("GET /api/v1/itineraries/{id}", api.getItinerary)
	mux.HandleFunc("POST /api/v1/itineraries", api.requireToken(api.createItinerary))
	mux.HandleFunc("DELETE /api/v1/itineraries/{id}", api.requireToken(api.deleteItinerary))
	mux.HandleFunc("POST /mcp", api.mcp) // reads are public; add_itinerary checks the token itself

	return securityHeaders(mux), nil
}

// indexHandler renders the shell with the list and first itinerary inlined,
// so the first paint needs no extra API round trip.
func indexHandler(store *Store, tmpl *template.Template) http.HandlerFunc {
	type bootData struct {
		List   []Summary  `json:"list"`
		Active *Itinerary `json:"active"`
	}
	return func(w http.ResponseWriter, _ *http.Request) {
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
		w.Header().Set("Cache-Control", "no-cache")
		buf.WriteTo(w)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
