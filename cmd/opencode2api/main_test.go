package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPortListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "empty", raw: "", want: ""},
		{name: "whitespace", raw: "  ", want: ""},
		{name: "render port", raw: "10000", want: "0.0.0.0:10000"},
		{name: "trimmed port", raw: " 10000 ", want: "0.0.0.0:10000"},
		{name: "maximum port", raw: "65535", want: "0.0.0.0:65535"},
		{name: "zero", raw: "0", wantErr: true},
		{name: "negative", raw: "-1", wantErr: true},
		{name: "invalid text", raw: "https", wantErr: true},
		{name: "address instead of port", raw: "0.0.0.0:10000", wantErr: true},
		{name: "out of range", raw: "65536", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := portListenAddress(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("portListenAddress(%q) returned no error", test.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("portListenAddress(%q) returned error: %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("portListenAddress(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestSinglePortHandlerRoutesAPIPaths(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	webui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	handler := singlePortHandler{api: api, webui: webui}

	for _, test := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/livez", wantStatus: http.StatusAccepted},
		{path: "/healthz", wantStatus: http.StatusAccepted},
		{path: "/v1", wantStatus: http.StatusAccepted},
		{path: "/v1/models", wantStatus: http.StatusAccepted},
		{path: "/v1/chat/completions", wantStatus: http.StatusAccepted},
		{path: "/v1/responses", wantStatus: http.StatusAccepted},
		{path: "/v1/messages", wantStatus: http.StatusAccepted},
		{path: "/v1/systemone", wantStatus: http.StatusAccepted},
		{path: "/", wantStatus: http.StatusCreated},
		{path: "/app.js", wantStatus: http.StatusCreated},
		{path: "/api/auth/session", wantStatus: http.StatusCreated},
		{path: "/api/model-quotas", wantStatus: http.StatusCreated},
		{path: "/api/api-keys", wantStatus: http.StatusCreated},
		{path: "/api/rotation", wantStatus: http.StatusCreated},
		{path: "/api/models/availability", wantStatus: http.StatusCreated},
		{path: "/v10/models", wantStatus: http.StatusCreated},
	} {
		t.Run(test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
			if recorder.Code != test.wantStatus {
				t.Fatalf("path %q returned %d, want %d", test.path, recorder.Code, test.wantStatus)
			}
		})
	}
}
