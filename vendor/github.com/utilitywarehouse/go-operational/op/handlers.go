package op

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

func newHealthCheckHandler(hc *Status) http.Handler {
	if len(hc.checkers) == 0 {
		return http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		if err := newEncoder(w).Encode(hc.Check()); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func newReadyHandler(hc *Status) http.Handler {
	if hc.ready == nil {
		return http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hc.ready() {
			w.Header().Add("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "ready\n")
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
}

func newAboutHandler(os *Status) http.Handler {

	j, err := json.MarshalIndent(os.About(), "  ", "  ")
	if err != nil {
		panic(err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write(j)
		if err != nil {
			log.Println("failed to write about response")
		}
	})
}

// NewHandler created a new HTTP handler that should be mapped to "/__/".
// It will create all the standard endpoints it can based on how the OpStatus
// is configured.
func NewHandler(os *Status) http.Handler {
	m := http.NewServeMux()
	m.Handle("/__/about", newAboutHandler(os))
	m.Handle("/__/health", newHealthCheckHandler(os))
	m.Handle("/__/ready", newReadyHandler(os))
	return m
}
