package ntfy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookHandlerHTTP(t *testing.T) {
	h := NewWebhookHandler()
	if err := h.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	// Test missing token
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.handleWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	// Test missing action
	req = httptest.NewRequest(http.MethodGet, "/sometoken", nil)
	rec = httptest.NewRecorder()
	h.handleWebhook(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	// Test expired token
	req = httptest.NewRequest(http.MethodGet, "/expired?action=reply", nil)
	rec = httptest.NewRecorder()
	h.handleWebhook(rec, req)
	if rec.Code != http.StatusGone {
		t.Errorf("expected status 410, got %d", rec.Code)
	}

	// Test successful dispatch
	token, ch := h.Register()
	req = httptest.NewRequest(http.MethodGet, "/"+token+"?action=reply", nil)
	rec = httptest.NewRecorder()
	h.handleWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	select {
	case event := <-ch:
		if event.ActionKey != "reply" {
			t.Errorf("expected action key 'reply', got %q", event.ActionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Test POST method
	newToken, newCh := h.Register()
	req = httptest.NewRequest(http.MethodPost, "/"+newToken+"?action=dismiss", nil)
	rec = httptest.NewRecorder()
	h.handleWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for POST, got %d", rec.Code)
	}

	select {
	case event := <-newCh:
		if event.ActionKey != "dismiss" {
			t.Errorf("expected action key 'dismiss', got %q", event.ActionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Test unsupported method
	req = httptest.NewRequest(http.MethodPut, "/"+token+"?action=reply", nil)
	rec = httptest.NewRecorder()
	h.handleWebhook(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", rec.Code)
	}
}

func TestWebhookHandlerServeWithoutListen(t *testing.T) {
	h := NewWebhookHandler()
	if err := h.Serve(); err == nil {
		t.Error("expected error when Serve called without Listen")
	}
}

func TestWebhookHandlerAddress(t *testing.T) {
	h := NewWebhookHandler()
	if h.Address() != "" {
		t.Error("expected empty address before Listen")
	}

	if err := h.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	addr := h.Address()
	if addr == "" {
		t.Error("expected non-empty address after Listen")
	}
}
