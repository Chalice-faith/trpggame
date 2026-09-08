package realtime

import (
	"net/http/httptest"
	"testing"
)

func TestNewUpgraderUsesSharedOriginPolicy(t *testing.T) {
	origins, err := ParseAllowedOrigins("https://game.example.com")
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}

	upgrader := NewUpgrader(origins)
	if upgrader.ReadBufferSize != websocketBufferSize {
		t.Fatalf("ReadBufferSize = %d, want %d", upgrader.ReadBufferSize, websocketBufferSize)
	}
	if upgrader.WriteBufferSize != websocketBufferSize {
		t.Fatalf("WriteBufferSize = %d, want %d", upgrader.WriteBufferSize, websocketBufferSize)
	}

	tests := []struct {
		name   string
		origin string
		want   bool
	}{
		{name: "no origin", want: true},
		{name: "allowed", origin: "https://game.example.com", want: true},
		{name: "unknown", origin: "https://other.example.com", want: false},
		{name: "null", origin: "null", want: false},
		{name: "multiple values", origin: "https://game.example.com, https://other.example.com", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://server/ws", nil)
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			if got := upgrader.CheckOrigin(request); got != tt.want {
				t.Fatalf("CheckOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewUpgraderRejectsNilOriginSet(t *testing.T) {
	upgrader := NewUpgrader(nil)
	request := httptest.NewRequest("GET", "http://server/ws", nil)
	if upgrader.CheckOrigin(request) {
		t.Fatal("nil OriginSet must not allow an upgrade")
	}
}
