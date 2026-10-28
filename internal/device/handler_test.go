package device

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avelarfe/devices-api/internal/notify"
)

func TestCreateAndFetch(t *testing.T) {
	t.Parallel()
	handler, _, _ := newHandler(newMem())

	response := serve(handler, http.MethodPost, "/devices", `{"name":"Pixel 8","brand":"Google","state":"available"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, part := range []string{
		`"id":"11111111-1111-4111-8111-111111111111"`,
		`"name":"Pixel 8"`,
		`"brand":"Google"`,
		`"state":"available"`,
		`"created_at":"2026-10-27T15:04:05.123456Z"`,
	} {
		if !strings.Contains(body, part) {
			t.Fatalf("body %s missing %s", body, part)
		}
	}

	response = serve(handler, http.MethodGet, "/devices/"+sampleID, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"Pixel 8"`) {
		t.Fatalf("get %d %s", response.Code, response.Body.String())
	}

	response = serve(handler, http.MethodGet, "/devices?brand=Google&state=available", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), sampleID) {
		t.Fatalf("list %d %s", response.Code, response.Body.String())
	}
	response = serve(handler, http.MethodGet, "/devices?brand=Apple", "")
	if response.Body.String() != "[]\n" {
		t.Fatalf("empty list = %q", response.Body.String())
	}
}

func TestRejectedRequests(t *testing.T) {
	t.Parallel()
	current := sampleDevice()
	current.State = StateInUse
	handler, store, _ := newHandler(newMem(current))

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		part   string
	}{
		{name: "unknown created_at", method: http.MethodPost, path: "/devices", body: `{"name":"A","brand":"B","state":"available","created_at":"2020-01-01T00:00:00Z"}`, status: http.StatusBadRequest, part: "created_at"},
		{name: "empty body", method: http.MethodPost, path: "/devices", status: http.StatusBadRequest, part: "request body is required"},
		{name: "invalid json", method: http.MethodPost, path: "/devices", body: `{`, status: http.StatusBadRequest, part: "invalid JSON"},
		{name: "bad id", method: http.MethodGet, path: "/devices/events-nope", status: http.StatusBadRequest, part: errInvalidID},
		{name: "missing", method: http.MethodGet, path: "/devices/22222222-2222-4222-8222-222222222222", status: http.StatusNotFound, part: errNotFoundMsg},
		{name: "bad state filter", method: http.MethodGet, path: "/devices?state=broken", status: http.StatusBadRequest, part: errStateInvalid},
		{name: "rename in use", method: http.MethodPatch, path: "/devices/" + sampleID, body: `{"name":"Other"}`, status: http.StatusConflict, part: errInUseRename},
		{name: "put rename in use", method: http.MethodPut, path: "/devices/" + sampleID, body: `{"name":"Other","brand":"Google","state":"in-use"}`, status: http.StatusConflict, part: errInUseRename},
		{name: "delete in use", method: http.MethodDelete, path: "/devices/" + sampleID, status: http.StatusConflict, part: errInUseDelete},
		{name: "empty patch", method: http.MethodPatch, path: "/devices/" + sampleID, body: `{}`, status: http.StatusBadRequest, part: errPatchEmpty},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serve(handler, test.method, test.path, test.body)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.part) {
				t.Fatalf("status %d body %s", response.Code, response.Body.String())
			}
		})
	}
	if store.deletes != 0 || store.updates != 0 {
		t.Fatalf("rejected requests wrote: updates=%d deletes=%d", store.updates, store.deletes)
	}
}

func TestUpdateDeleteAndMethod(t *testing.T) {
	t.Parallel()
	handler, store, _ := newHandler(newMem(sampleDevice()))

	response := serve(handler, http.MethodPatch, "/devices/"+sampleID, `{"state":"inactive"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"inactive"`) {
		t.Fatalf("patch %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"created_at":"2026-10-27T15:04:05.123456Z"`) {
		t.Fatalf("creation time changed: %s", response.Body.String())
	}

	response = serve(handler, http.MethodPut, "/devices/"+sampleID, `{"name":"Pixel 9","brand":"Google","state":"available"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"Pixel 9"`) {
		t.Fatalf("put %d %s", response.Code, response.Body.String())
	}

	response = serve(handler, http.MethodDelete, "/devices/"+sampleID, "")
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("delete %d %s", response.Code, response.Body.String())
	}
	if _, err := store.Get(context.Background(), sampleID); !errors.Is(err, ErrNotFound) {
		t.Fatal("device still stored")
	}

	response = serve(handler, http.MethodPut, "/devices", "")
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", response.Code)
	}
}

func TestInternalErrorHidesCause(t *testing.T) {
	t.Parallel()
	store := newMem()
	store.insertErr = errors.New("dial tcp secret-host:3306")
	handler, _, _ := newHandler(store)
	response := serve(handler, http.MethodPost, "/devices", `{"name":"Pixel 8","brand":"Google","state":"available"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", response.Code)
	}
	if response.Body.String() != `{"error":"internal error"}`+"\n" {
		t.Fatalf("body %q", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-host") {
		t.Fatal("response leaked the database error")
	}
}

func TestHealthAndEmptyList(t *testing.T) {
	t.Parallel()
	handler, _, _ := newHandler(newMem())
	response := serve(handler, http.MethodGet, "/healthz", "")
	if response.Code != http.StatusOK || response.Body.String() != `{"status":"ok"}`+"\n" {
		t.Fatalf("health %d %s", response.Code, response.Body.String())
	}
	response = serve(handler, http.MethodGet, "/devices", "")
	if response.Body.String() != "[]\n" {
		t.Fatalf("list %q", response.Body.String())
	}
}

func TestStateChangeReachesSubscriber(t *testing.T) {
	t.Parallel()
	handler, _, _ := newHandler(newMem())
	routes := handler.Routes()

	created := httptest.NewRecorder()
	routes.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/devices", strings.NewReader(`{"name":"Pixel 8","brand":"Google","state":"available"}`)))
	if created.Code != http.StatusCreated {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	stream := newSSERecorder()
	done := make(chan struct{})
	go func() {
		routes.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/devices/events", nil).WithContext(ctx))
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	stream.waitFlush(t)
	if stream.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", stream.header.Get("Content-Type"))
	}
	if !strings.Contains(stream.body(), ": connected") {
		t.Fatalf("body %q", stream.body())
	}

	patched := httptest.NewRecorder()
	routes.ServeHTTP(patched, httptest.NewRequest(http.MethodPatch, "/devices/"+sampleID, strings.NewReader(`{"state":"in-use"}`)))
	if patched.Code != http.StatusOK {
		t.Fatalf("patch %d %s", patched.Code, patched.Body.String())
	}
	stream.waitFlush(t)
	body := stream.body()
	for _, part := range []string{
		"event: device_state_changed",
		`"device_id":"` + sampleID + `"`,
		`"previous_state":"available"`,
		`"new_state":"in-use"`,
	} {
		if !strings.Contains(body, part) {
			t.Fatalf("body %s missing %s", body, part)
		}
	}
	if strings.Count(body, "device_state_changed") != 1 {
		t.Fatalf("body %s", body)
	}

	// Same state, and a delete, must not produce another event.
	noop := httptest.NewRecorder()
	routes.ServeHTTP(noop, httptest.NewRequest(http.MethodPatch, "/devices/"+sampleID, strings.NewReader(`{"state":"in-use"}`)))
	if noop.Code != http.StatusOK {
		t.Fatalf("noop %d %s", noop.Code, noop.Body.String())
	}
	// Move off in-use so delete is allowed, which itself must not notify.
	// That move does notify, so consume it, then delete and assert the count stays.
	moved := httptest.NewRecorder()
	routes.ServeHTTP(moved, httptest.NewRequest(http.MethodPatch, "/devices/"+sampleID, strings.NewReader(`{"state":"available"}`)))
	if moved.Code != http.StatusOK {
		t.Fatalf("move %d %s", moved.Code, moved.Body.String())
	}
	stream.waitFlush(t)
	deleted := httptest.NewRecorder()
	routes.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/devices/"+sampleID, nil))
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete %d %s", deleted.Code, deleted.Body.String())
	}
	if strings.Count(stream.body(), "device_state_changed") != 2 {
		t.Fatalf("body after delete %s", stream.body())
	}
}

func TestBodyTooLarge(t *testing.T) {
	t.Parallel()
	handler, _, _ := newHandler(newMem())
	payload := make([]byte, 0, maxBodyBytes+64)
	payload = append(payload, `{"name":"`...)
	payload = append(payload, bytes.Repeat([]byte("a"), maxBodyBytes)...)
	payload = append(payload, `","brand":"Google","state":"available"}`...)
	request := httptest.NewRequest(http.MethodPost, "/devices", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "too large") {
		t.Fatalf("status %d body %s", response.Code, response.Body.String())
	}
}

func newHandler(store *memStore) (*Handler, *memStore, *notify.Hub) {
	hub := notify.NewHub()
	service := NewService(store, hub.Publish, func() (string, error) {
		return sampleID, nil
	})
	return NewHandler(service, hub), store, hub
}

func serve(handler *Handler, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	response := httptest.NewRecorder()
	handler.Routes().ServeHTTP(response, request)
	return response
}

type sseRecorder struct {
	mu      sync.Mutex
	header  http.Header
	buf     bytes.Buffer
	status  int
	flushed chan struct{}
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{
		header:  make(http.Header),
		flushed: make(chan struct{}, 8),
	}
}

func (s *sseRecorder) Header() http.Header { return s.header }

func (s *sseRecorder) WriteHeader(status int) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *sseRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *sseRecorder) Flush() {
	select {
	case s.flushed <- struct{}{}:
	default:
	}
}

func (s *sseRecorder) waitFlush(t *testing.T) {
	t.Helper()
	select {
	case <-s.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the stream to flush")
	}
}

func (s *sseRecorder) body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
