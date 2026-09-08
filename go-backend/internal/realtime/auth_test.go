package realtime

import (
	"errors"
	"strings"
	"testing"

	jwt "github.com/golang-jwt/jwt/v5"

	"trpggame/internal/middleware"
)

func TestAuthenticateQueryToken(t *testing.T) {
	const secret = "realtime-auth-secret"
	token, err := middleware.GenerateToken(7, "investigator", secret, 15)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	claims, err := AuthenticateQueryToken("  "+token+"  ", secret)
	if err != nil {
		t.Fatalf("AuthenticateQueryToken() error = %v", err)
	}
	if claims.UserID != 7 || claims.Username != "investigator" {
		t.Fatalf("claims = %#v", claims)
	}
}

func TestAuthenticateQueryTokenClassifiesFailures(t *testing.T) {
	const secret = "realtime-auth-secret"
	expired, err := middleware.GenerateToken(7, "investigator", secret, -1)
	if err != nil {
		t.Fatalf("generate expired token: %v", err)
	}
	wrongSecret, err := middleware.GenerateToken(7, "investigator", "other-secret", 15)
	if err != nil {
		t.Fatalf("generate wrong-secret token: %v", err)
	}
	noneToken, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"user_id": 7}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("generate none token: %v", err)
	}

	tests := []struct {
		name    string
		token   string
		failure AuthFailure
	}{
		{name: "empty", token: "", failure: AuthMissingToken},
		{name: "blank", token: " \t\n", failure: AuthMissingToken},
		{name: "malformed", token: "not-a-jwt", failure: AuthInvalidToken},
		{name: "expired", token: expired, failure: AuthInvalidToken},
		{name: "wrong secret", token: wrongSecret, failure: AuthInvalidToken},
		{name: "wrong algorithm", token: noneToken, failure: AuthInvalidToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AuthenticateQueryToken(tt.token, secret)
			var authErr *AuthError
			if !errors.As(err, &authErr) || authErr.Failure != tt.failure {
				t.Fatalf("error = %#v, want failure %q", err, tt.failure)
			}
			if strings.Contains(authErr.Error(), tt.token) && tt.token != "" {
				t.Fatal("authentication error must not expose the raw token")
			}
		})
	}
}
