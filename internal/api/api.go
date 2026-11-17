// Package api exposes the service over HTTP + JSON and serves the board UI.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/feed"
	"github.com/yashchhabra57/recallroute/internal/inventory"
	"github.com/yashchhabra57/recallroute/internal/service"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/workflow"
)

// MaxBodyBytes caps uploads (a 50k-row inventory CSV is ~4 MB).
const MaxBodyBytes = 16 << 20

// Config holds optional handler settings.
type Config struct {
	// Token, if set, is required as "Authorization: Bearer <token>" on
	// every request that changes data.
	Token string
	// StoreKind is reported by /healthz ("memory" or "postgres").
	StoreKind string
	// Static serves the UI at "/". Nil disables it.
	Static fs.FS
	// WS handles /ws. Nil disables it.
	WS http.Handler
	// Poll, if set, is exposed as POST /api/recalls/poll.
	Poll func(r *http.Request) (service.IngestSummary, error)
	Log  *slog.Logger
}

type handler struct {
	svc *service.Service
	cfg Config
}

// New returns the HTTP handler.
func New(svc *service.Service, cfg Config) http.Handler {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &handler{svc: svc, cfg: cfg}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(h.logRequests)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", h.health)
	if cfg.WS != nil {
		r.Handle("/ws", cfg.WS)
	}

	r.Route("/api", func(r chi.Router) {
		r.Use(middleware.SetHeader("Cache-Control", "no-store"))
		r.Get("/recalls", h.listRecalls)
		r.Get("/lots", h.listLots)
		r.Get("/alerts", h.listAlerts)
		r.Get("/alerts/{id}", h.getAlert)

		r.Group(func(r chi.Router) {
			r.Use(h.requireToken)
			r.Post("/recalls/ingest", h.ingest)
			if cfg.Poll != nil {
				r.Post("/recalls/poll", h.poll)
			}
			r.Post("/lots", h.importLots)
			r.Post("/alerts/{id}/transitions", h.transition)
		})
	})

	if cfg.Static != nil {
		r.Handle("/*", http.FileServer(http.FS(cfg.Static)))
	}
	return r
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "store": h.cfg.StoreKind, "auth": h.cfg.Token != ""})
}

func (h *handler) listRecalls(w http.ResponseWriter, r *http.Request) {
	rs, err := h.svc.Recalls(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (h *handler) listLots(w http.ResponseWriter, r *http.Request) {
	ls, err := h.svc.Lots(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ls)
}

var validStates = map[domain.State]bool{
	"": true, domain.StateNew: true, domain.StateAcknowledged: true, domain.StatePulled: true,
	domain.StateNotified: true, domain.StateClosed: true, domain.StateDismissed: true,
}

func (h *handler) listAlerts(w http.ResponseWriter, r *http.Request) {
	state := domain.State(r.URL.Query().Get("state"))
	if !validStates[state] {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown state "+strconv.Quote(string(state)))
		return
	}
	as, err := h.svc.Alerts(r.Context(), state)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, as)
}

func (h *handler) getAlert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := h.svc.Alert(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) ingest(w http.ResponseWriter, r *http.Request) {
	rs, err := feed.ParseOpenFDA(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_feed", err.Error())
		return
	}
	sum, err := h.svc.IngestRecalls(r.Context(), rs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (h *handler) poll(w http.ResponseWriter, r *http.Request) {
	sum, err := h.cfg.Poll(r)
	if err != nil {
		writeError(w, http.StatusBadGateway, "feed_unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

type importResponse struct {
	service.ImportSummary
	RowErrors []inventory.RowError `json:"row_errors"`
}

func (h *handler) importLots(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var lots []domain.Lot
	rowErrs := []inventory.RowError{}
	switch ct {
	case "text/csv", "application/csv":
		var err error
		lots, rowErrs, err = inventory.ParseCSV(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_csv", err.Error())
			return
		}
		if rowErrs == nil {
			rowErrs = []inventory.RowError{}
		}
	case "application/json":
		if err := json.NewDecoder(body).Decode(&lots); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		for i, l := range lots {
			if strings.TrimSpace(l.Name) == "" || l.Quantity < 0 {
				writeError(w, http.StatusUnprocessableEntity, "validation", "lot "+strconv.Itoa(i)+": name required and quantity must be >= 0")
				return
			}
		}
	default:
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "send text/csv or application/json")
		return
	}
	sum, err := h.svc.ImportLots(r.Context(), lots)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, importResponse{sum, rowErrs})
}

type transitionRequest struct {
	Version int `json:"version"`
	workflow.Command
}

func (h *handler) transition(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req transitionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.Version <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "version is required (the alert version you are acting on)")
		return
	}
	v, err := h.svc.Transition(r.Context(), id, req.Version, req.Command)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *handler) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.cfg.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(h.cfg.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="recallroute"`)
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or wrong API token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if r.URL.Path == "/healthz" {
			return
		}
		h.cfg.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"dur_ms", time.Since(start).Milliseconds(), "req_id", middleware.GetReqID(r.Context()))
	})
}

// fail maps domain errors to HTTP status codes.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "version_conflict", "this alert was updated by someone else; reload and try again")
	case errors.Is(err, workflow.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_transition", err.Error())
	case errors.Is(err, workflow.ErrValidation):
		writeError(w, http.StatusUnprocessableEntity, "validation", err.Error())
	case errors.As(err, &maxErr):
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	default:
		h.cfg.Log.Error("internal error", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "id must be a positive integer")
		return 0, false
	}
	return id, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
