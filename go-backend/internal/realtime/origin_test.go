package realtime

import (
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestParseAllowedOriginsNormalizesAndDeduplicates(t *testing.T) {
	set, err := ParseAllowedOrigins(
		" HTTP://LOCALHOST:80/, https://Example.com:443, https://example.com, " +
			"https://example.com:8443, http://[::1]:80 ",
	)
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}

	want := []string{
		"http://localhost",
		"https://example.com",
		"https://example.com:8443",
		"http://[::1]",
	}
	if len(set.allowed) != len(want) {
		t.Fatalf("allowed = %#v, want %d entries", set.allowed, len(want))
	}
	for _, origin := range want {
		if _, ok := set.allowed[origin]; !ok {
			t.Fatalf("normalized origin %q is missing from %#v", origin, set.allowed)
		}
	}
}

func TestParseAllowedOriginsRejectsInvalidValues(t *testing.T) {
	tests := []string{
		"",
		" , ",
		"*",
		"null",
		"localhost:5173",
		"ftp://example.com",
		"https://example.com/app",
		"https://example.com?from=app",
		"https://example.com#fragment",
		"https://user:pass@example.com",
		"https://example.com:",
		"https://example.com:0",
		"https://example.com:65536",
		"https://example.com:invalid",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseAllowedOrigins(raw)
			if !errors.Is(err, ErrInvalidAllowedOrigins) {
				t.Fatalf("ParseAllowedOrigins(%q) error = %v", raw, err)
			}
		})
	}
}

func TestOriginSetAllowsRequests(t *testing.T) {
	set, err := ParseAllowedOrigins("http://localhost:5173,https://game.example.com")
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}

	tests := []struct {
		name    string
		origins []string
		want    bool
	}{
		{name: "no origin", want: true},
		{name: "allowed", origins: []string{"HTTP://LOCALHOST:5173/"}, want: true},
		{name: "unknown", origins: []string{"https://other.example.com"}, want: false},
		{name: "null", origins: []string{"null"}, want: false},
		{name: "comma separated", origins: []string{"http://localhost:5173, https://game.example.com"}, want: false},
		{name: "multiple headers", origins: []string{"http://localhost:5173", "https://game.example.com"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "http://server/ws", nil)
			for _, origin := range tt.origins {
				request.Header.Add("Origin", origin)
			}
			if got := set.Allows(request); got != tt.want {
				t.Fatalf("Allows() = %v, want %v", got, tt.want)
			}
		})
	}

	if (*OriginSet)(nil).Allows(httptest.NewRequest("GET", "http://server/ws", nil)) {
		t.Fatal("nil OriginSet must reject requests")
	}
	if set.Allows(nil) {
		t.Fatal("nil request must be rejected")
	}
}

func TestOriginSetAllowsConcurrentReads(t *testing.T) {
	set, err := ParseAllowedOrigins("https://game.example.com")
	if err != nil {
		t.Fatalf("ParseAllowedOrigins() error = %v", err)
	}

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				request := httptest.NewRequest("GET", "http://server/ws", nil)
				request.Header.Set("Origin", "https://game.example.com")
				if !set.Allows(request) {
					t.Error("allowed origin was rejected")
					return
				}
			}
		}()
	}
	wait.Wait()
}
