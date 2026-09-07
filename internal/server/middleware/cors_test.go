package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCorsDevelopmentOrigins(t *testing.T) {
	testCases := []struct {
		name    string
		debug   string
		origin  string
		allowed bool
	}{
		{name: "debug localhost", debug: "true", origin: "http://localhost:3000", allowed: true},
		{name: "debug loopback", debug: "true", origin: "http://127.0.0.1:3000", allowed: true},
		{name: "production localhost", debug: "false", origin: "http://localhost:3000"},
		{name: "production loopback", debug: "false", origin: "http://127.0.0.1:3000"},
		{name: "debug unset", origin: "http://localhost:3000"},
		{name: "debug remote origin", debug: "true", origin: "https://example.com"},
		{name: "debug localhost lookalike", debug: "true", origin: "http://localhost.example.com:3000"},
		{name: "debug loopback lookalike", debug: "true", origin: "http://127.0.0.1.example.com:3000"},
		{name: "debug other port", debug: "true", origin: "http://localhost:3001"},
		{name: "debug other scheme", debug: "true", origin: "https://localhost:3000"},
		{name: "debug null origin", debug: "true", origin: "null"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("OCTOPUS_DEBUG", testCase.debug)
			router := gin.New()
			router.Use(Cors())
			router.Match([]string{http.MethodGet, http.MethodPost}, "/test", func(context *gin.Context) {
				context.Status(http.StatusOK)
			})

			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
				t.Run(method, func(t *testing.T) {
					request := httptest.NewRequest(method, "http://127.0.0.1:8080/test", nil)
					request.Header.Set("Origin", testCase.origin)
					if method == http.MethodOptions {
						request.Header.Set("Access-Control-Request-Method", http.MethodPost)
						request.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
					} else {
						request.Header.Set("Authorization", "Bearer test-token")
						request.Header.Set("Content-Type", "application/json")
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)

					wantStatus := http.StatusForbidden
					if testCase.allowed {
						wantStatus = http.StatusOK
						if method == http.MethodOptions {
							wantStatus = http.StatusNoContent
						}
					}
					if response.Code != wantStatus {
						t.Fatalf("status = %d, want %d", response.Code, wantStatus)
					}

					wantOrigin := ""
					if testCase.allowed {
						wantOrigin = testCase.origin
					}
					if gotOrigin := response.Header().Get("Access-Control-Allow-Origin"); gotOrigin != wantOrigin {
						t.Fatalf("allowed origin = %q, want %q", gotOrigin, wantOrigin)
					}
					if !testCase.allowed {
						return
					}
					if credentials := response.Header().Get("Access-Control-Allow-Credentials"); credentials != "true" {
						t.Fatalf("allow credentials = %q, want true", credentials)
					}
					if method == http.MethodOptions {
						allowedHeaders := strings.Split(strings.ToLower(response.Header().Get("Access-Control-Allow-Headers")), ",")
						for _, requiredHeader := range []string{"authorization", "content-type"} {
							found := false
							for _, allowedHeader := range allowedHeaders {
								if strings.TrimSpace(allowedHeader) == requiredHeader {
									found = true
									break
								}
							}
							if !found {
								t.Errorf("required header %q is not explicitly allowed: %v", requiredHeader, allowedHeaders)
							}
						}
					}
				})
			}
		})
	}
}
