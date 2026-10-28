package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/avelarfe/devices-api/internal/notify"
)

const maxBodyBytes = 1 << 20

type errorBody struct {
	Error string `json:"error"`
}

// Handler serves the device routes and the state-change stream.
type Handler struct {
	svc *Service
	hub *notify.Hub
}

// NewHandler wires the service and the subscriber hub.
func NewHandler(svc *Service, hub *notify.Hub) *Handler {
	if svc == nil || hub == nil {
		panic("device.NewHandler: nil dependency")
	}
	return &Handler{svc: svc, hub: hub}
}

// Routes returns the HTTP routes for this service.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("POST /devices", h.create)
	mux.HandleFunc("GET /devices", h.list)
	// Registered as its own path so "events" is not treated as a device id.
	mux.HandleFunc("GET /devices/events", h.events)
	mux.HandleFunc("GET /devices/{id}", h.get)
	mux.HandleFunc("PUT /devices/{id}", h.replace)
	mux.HandleFunc("PATCH /devices/{id}", h.patch)
	mux.HandleFunc("DELETE /devices/{id}", h.remove)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	created, err := h.svc.Create(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var brand, state *string
	if query.Has("brand") {
		value := query.Get("brand")
		brand = &value
	}
	if query.Has("state") {
		value := query.Get("state")
		state = &value
	}
	devices, err := h.svc.List(r.Context(), brand, state)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, found)
}

func (h *Handler) replace(w http.ResponseWriter, r *http.Request) {
	var in ReplaceInput
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	updated, err := h.svc.Replace(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	var in PatchInput
	if err := readJSON(w, r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	updated, err := h.svc.Patch(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, errors.New("streaming is not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	events, cancel := h.hub.Subscribe()
	defer cancel()

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-events:
			payload, err := json.Marshal(event)
			if err != nil {
				slog.Error("marshal state change", "err", err)
				continue
			}
			fmt.Fprintf(w, "event: device_state_changed\ndata: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return jsonError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ValidationError{Message: "request body must contain a single JSON value"}
	}
	return nil
}

func jsonError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return ValidationError{Message: "request body is too large"}
	}
	if errors.Is(err, io.EOF) {
		return ValidationError{Message: "request body is required"}
	}
	message := err.Error()
	if strings.HasPrefix(message, "json: unknown field ") {
		return ValidationError{Message: strings.TrimPrefix(message, "json: ")}
	}
	return ValidationError{Message: "invalid JSON"}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid ValidationError
	var conflict ConflictError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: invalid.Message})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: errNotFoundMsg})
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, errorBody{Error: conflict.Message})
	default:
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errInternal})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write response", "err", err)
	}
}
