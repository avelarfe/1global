package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusWriterForwardsFlush(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapped := &statusWriter{ResponseWriter: recorder, status: http.StatusOK}
	wrapped.WriteHeader(http.StatusNoContent)
	wrapped.Flush()

	if wrapped.status != http.StatusNoContent {
		t.Fatalf("status = %d", wrapped.status)
	}
	if !recorder.Flushed {
		t.Fatal("flush did not reach the underlying response")
	}
}
