package ntfy

import (
	"strings"
	"testing"
)

func TestBuildActions(t *testing.T) {
	ch := &Channel{
		config: Config{
			WebhookURL: "https://example.com/webhooks/ntfy",
		},
	}

	tests := []struct {
		name    string
		actions []string
		want    string
	}{
		{
			name:    "empty actions",
			actions: nil,
			want:    "",
		},
		{
			name:    "single action",
			actions: []string{"reply", "Reply"},
			want:    "http, Reply, https://example.com/webhooks/ntfy/TEST_TOKEN?action=reply, method=GET, clear=true",
		},
		{
			name:    "multiple actions",
			actions: []string{"reply", "Reply", "dismiss", "Dismiss"},
			want:    "http, Reply, https://example.com/webhooks/ntfy/TEST_TOKEN?action=reply, method=GET, clear=true; http, Dismiss, https://example.com/webhooks/ntfy/TEST_TOKEN?action=dismiss, method=GET, clear=true",
		},
		{
			name:    "skips default action",
			actions: []string{"default", "Open", "reply", "Reply"},
			want:    "http, Reply, https://example.com/webhooks/ntfy/TEST_TOKEN?action=reply, method=GET, clear=true",
		},
		{
			name:    "limits to 3 actions",
			actions: []string{"a1", "A1", "a2", "A2", "a3", "A3", "a4", "A4"},
			want:    "http, A1, https://example.com/webhooks/ntfy/TEST_TOKEN?action=a1, method=GET, clear=true; http, A2, https://example.com/webhooks/ntfy/TEST_TOKEN?action=a2, method=GET, clear=true; http, A3, https://example.com/webhooks/ntfy/TEST_TOKEN?action=a3, method=GET, clear=true",
		},
		{
			name:    "url encodes action key",
			actions: []string{"reply all", "Reply All"},
			want:    "http, Reply All, https://example.com/webhooks/ntfy/TEST_TOKEN?action=reply+all, method=GET, clear=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ch.buildActions(tt.actions, "TEST_TOKEN")
			// Replace the actual token in the result for comparison since the base URL is fixed
			if got != tt.want {
				t.Errorf("buildActions() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildActionsNoWebhookURL(t *testing.T) {
	ch := &Channel{
		config: Config{},
	}

	actions := []string{"reply", "Reply"}
	got := ch.buildActions(actions, "TOKEN")
	if got != "" {
		t.Errorf("expected empty string when no webhook URL, got %q", got)
	}
}

func TestBuildActionsTrailingSlash(t *testing.T) {
	ch := &Channel{
		config: Config{
			WebhookURL: "https://example.com/webhooks/ntfy/",
		},
	}

	actions := []string{"reply", "Reply"}
	got := ch.buildActions(actions, "TOKEN")
	want := "https://example.com/webhooks/ntfy/TOKEN?action=reply"
	if !strings.Contains(got, want) {
		t.Errorf("expected %q to contain %q", got, want)
	}
}
