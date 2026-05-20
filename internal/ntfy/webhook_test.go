package ntfy

import (
	"testing"
	"time"
)

func TestWebhookHandlerRegisterAndDispatch(t *testing.T) {
	h := NewWebhookHandler()

	token, ch := h.Register()
	if token == "" {
		t.Fatal("expected non-empty token")
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	// Dispatch should succeed
	if !h.Dispatch(token, "reply") {
		t.Fatal("expected Dispatch to return true")
	}

	// Channel should receive the event
	select {
	case event := <-ch:
		if event.ActionKey != "reply" {
			t.Errorf("expected action key 'reply', got %q", event.ActionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for action event")
	}

	// Dispatching again should fail (token consumed)
	if h.Dispatch(token, "reply") {
		t.Error("expected second Dispatch to return false")
	}
}

func TestWebhookHandlerDispatchUnknownToken(t *testing.T) {
	h := NewWebhookHandler()

	if h.Dispatch("nonexistent", "reply") {
		t.Error("expected Dispatch for unknown token to return false")
	}
}

func TestWebhookHandlerUnregister(t *testing.T) {
	h := NewWebhookHandler()

	token, ch := h.Register()
	h.Unregister(token)

	// Channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel after unregister")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}

	// Dispatch after unregister should fail
	if h.Dispatch(token, "reply") {
		t.Error("expected Dispatch after unregister to return false")
	}
}

func TestWebhookHandlerMultipleRegistrations(t *testing.T) {
	h := NewWebhookHandler()

	token1, ch1 := h.Register()
	token2, ch2 := h.Register()

	if token1 == token2 {
		t.Error("expected different tokens")
	}

	// Dispatch to token2 only
	if !h.Dispatch(token2, "dismiss") {
		t.Fatal("expected Dispatch to token2 to succeed")
	}

	select {
	case event := <-ch2:
		if event.ActionKey != "dismiss" {
			t.Errorf("expected 'dismiss', got %q", event.ActionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ch2")
	}

	// token1 is still valid, dispatch should succeed
	if !h.Dispatch(token1, "reply") {
		t.Fatal("expected token1 dispatch to succeed")
	}
	select {
	case event := <-ch1:
		if event.ActionKey != "reply" {
			t.Errorf("expected 'reply', got %q", event.ActionKey)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ch1")
	}
}
