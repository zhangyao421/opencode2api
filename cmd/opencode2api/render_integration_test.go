package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adminui "opencode2api/internal/admin"
	"opencode2api/internal/config"
	"opencode2api/internal/gateway"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
	"opencode2api/internal/telemetry"
)

const (
	renderLocalKey    = "render-local-test-key"
	renderUpstreamKey = "render-upstream-test-key"
	renderPassword    = "render-test-password"
)

type renderFixture struct {
	handler    http.Handler
	manager    *gateway.RuntimeManager
	monitor    *telemetry.Monitor
	configPath string
}

func newRenderFixture(t *testing.T, seedCatalog bool, upstreamHandler http.Handler) *renderFixture {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)
	configPath := filepath.Join(t.TempDir(), "config.json")
	// This is the old configuration shape: no rotation, API Key metadata, or
	// first-event timeout fields. All credentials are local test values.
	legacy := fmt.Sprintf(`{
		"listen":"127.0.0.1:8080",
		"server_keys":[%q], "zen_keys":[%q], "go_keys":[%q],
		"proxies":["direct"],
		"upstream":{"zen":%q,"go":%q},
		"retry":{"max_attempts":1,"timeout_seconds":5},
		"webui":{"enabled":true,"listen":"127.0.0.1:8080","username":"admin","password":%q}
	}`, renderLocalKey, renderUpstreamKey, renderUpstreamKey, upstream.URL+"/zen", upstream.URL+"/go", renderPassword)
	if err := os.WriteFile(configPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load legacy Render config: %v", err)
	}
	if seedCatalog {
		catalog := models.NewCatalog(cfg.Prefer, cfg.Models.Protocols)
		catalog.ReplaceWithCapabilities([]string{"mock-free", "mock-system"}, []string{"glm-5.3", "mock-sweet"},
			map[config.Tier]map[string]wire.Protocol{
				config.TierZen: {"mock-free": wire.Chat, "mock-system": wire.SystemOne},
				config.TierGo:  {"glm-5.3": wire.Chat, "mock-sweet": wire.Chat},
			}, nil, nil)
		catalog.SetCachePath(models.CatalogCachePath(configPath))
		if err := catalog.SaveCache(); err != nil {
			t.Fatal(err)
		}
	}
	// Cancel background discovery/probes before startup so tests never fetch
	// public model metadata or send real inference. HTTP requests below use
	// their own contexts and can reach only the configured loopback upstream.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	monitor := telemetry.NewMonitor()
	hub := telemetry.NewLogHub(100)
	manager, err := gateway.NewRuntimeManager(ctx, configPath, cfg, logger, monitor, hub, config.NewSecretRedactor(), new(slog.LevelVar))
	if err != nil {
		t.Fatalf("start Render runtime: %v", err)
	}
	t.Cleanup(manager.Shutdown)
	return &renderFixture{
		handler: singlePortHandler{api: manager.Handler(), webui: adminui.New(manager, monitor, hub, logger).Handler()},
		manager: manager, monitor: monitor, configPath: configPath,
	}
}

func (f *renderFixture) request(t *testing.T, method, path, body string, headers http.Header, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://render.test"+path, strings.NewReader(body))
	if headers != nil {
		req.Header = headers.Clone()
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, response.Code, wantStatus, response.Body.String())
	}
	return response
}

func (f *renderFixture) login(t *testing.T) http.Header {
	t.Helper()
	headers := make(http.Header)
	headers.Set("X-Forwarded-Proto", "https")
	response := f.request(t, http.MethodPost, "/api/auth/login", fmt.Sprintf(`{"username":"admin","password":%q}`, renderPassword), headers, 200)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || session.CSRF == "" {
		t.Fatalf("login did not return a CSRF token: %v", err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("login behind Render HTTPS must set a Secure, HttpOnly session cookie")
	}
	headers.Set("Cookie", cookies[0].Name+"="+cookies[0].Value)
	headers.Set("X-CSRF-Token", session.CSRF)
	headers.Set("Origin", "https://render.test")
	return headers
}

func TestRenderLivenessWithoutReadyModels(t *testing.T) {
	var upstreamRequests atomic.Int32
	f := newRenderFixture(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequests.Add(1)
		http.Error(w, "upstream must not be contacted", 502)
	}))
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		f.request(t, method, "/livez", "", nil, 200)
		f.request(t, method, "/healthz", "", nil, 503)
	}
	snapshot := f.monitor.Snapshot()
	if snapshot.Lifetime.Total != 0 || snapshot.Active != 0 || upstreamRequests.Load() != 0 {
		t.Fatal("health probes must not affect request metrics or contact the upstream")
	}
}

func TestRenderManagementAndLegacyConfig(t *testing.T) {
	var quotaRequests atomic.Int32
	f := newRenderFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/go/v1/usage" || r.Header.Get("Authorization") != "Bearer "+renderUpstreamKey {
			t.Errorf("unexpected mock upstream request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected mock request", 502)
			return
		}
		quotaRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"usage":{"rolling":{"status":"ok","percent":25,"resetsAt":"2026-10-09T00:00:00Z"},"weekly":{"status":"ok","percent":40,"resetsAt":"2026-10-10T00:00:00Z"},"monthly":{"status":"ok","percent":55,"resetsAt":"2026-11-01T00:00:00Z"}}}`)
	}))
	page := f.request(t, http.MethodGet, "/", "", nil, 200).Body.String()
	for _, feature := range []string{"model-quotas", "api-keys", "rotation", "availability"} {
		if !strings.Contains(page, feature) {
			t.Errorf("embedded WebUI is missing %s", feature)
		}
	}
	apiHeaders := http.Header{"Authorization": {"Bearer " + renderLocalKey}}
	for _, path := range []string{"/api/model-quotas", "/api/api-keys", "/api/rotation", "/api/models/availability"} {
		f.request(t, http.MethodGet, path, "", apiHeaders, 401)
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/systemone"} {
		f.request(t, http.MethodPost, path, `{}`, nil, 401)
	}
	adminHeaders := f.login(t)
	f.request(t, http.MethodGet, "/v1/models", "", adminHeaders, 401)
	for _, path := range []string{"/api/api-keys", "/api/rotation", "/api/models/availability"} {
		f.request(t, http.MethodGet, path, "", adminHeaders, 200)
	}
	for range 2 {
		response := f.request(t, http.MethodGet, "/api/model-quotas", "", adminHeaders, 200)
		if !strings.Contains(response.Body.String(), `"remaining_percent":75`) || strings.Contains(response.Body.String(), renderUpstreamKey) {
			t.Fatal("quota response must show the mock percentage without exposing the upstream key")
		}
	}
	if quotaRequests.Load() != 1 {
		t.Fatal("quota requests must reuse the server cache")
	}
	modelList := f.request(t, http.MethodGet, "/v1/models", "", apiHeaders, 200).Body.String()
	for _, model := range []string{`"id":"sota"`, `"id":"sweet"`, `"native_protocol":"systemone"`, `"supported_endpoints":["/v1/systemone"]`} {
		if !strings.Contains(modelList, model) {
			t.Errorf("model discovery is missing %s", model)
		}
	}
	withoutCSRF := adminHeaders.Clone()
	withoutCSRF.Del("X-CSRF-Token")
	f.request(t, http.MethodPost, "/api/api-keys", `{"name":"test"}`, withoutCSRF, 403)
	created := f.request(t, http.MethodPost, "/api/api-keys", `{"name":"Render test"}`, adminHeaders, 201)
	var key struct {
		Value string `json:"key"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &key); err != nil || key.Value == "" {
		t.Fatalf("missing generated local API Key: %v", err)
	}
	newKeyHeaders := http.Header{"Authorization": {"Bearer " + key.Value}}
	keyPath := "/api/api-keys/" + config.Fingerprint(key.Value)
	f.request(t, http.MethodGet, "/v1/models", "", newKeyHeaders, 200)
	f.request(t, http.MethodPatch, keyPath, `{"enabled":false}`, adminHeaders, 200)
	f.request(t, http.MethodGet, "/v1/models", "", newKeyHeaders, 401)
	f.request(t, http.MethodPatch, keyPath, `{"enabled":true}`, adminHeaders, 200)
	f.request(t, http.MethodGet, "/v1/models", "", newKeyHeaders, 200)
	f.request(t, http.MethodDelete, keyPath, "", adminHeaders, 200)
	f.request(t, http.MethodGet, "/v1/models", "", newKeyHeaders, 401)
	primaryPath := "/api/api-keys/" + config.Fingerprint(renderLocalKey)
	f.request(t, http.MethodPatch, primaryPath, `{"enabled":false}`, adminHeaders, 400)
	f.request(t, http.MethodPatch, primaryPath, `{"name":"Primary Render key"}`, adminHeaders, 200)
	f.request(t, http.MethodPost, "/api/models/restore", `{"model":"mock-free"}`, withoutCSRF, 403)
	f.request(t, http.MethodPost, "/api/models/restore", `{"model":"mock-free"}`, adminHeaders, 200)
	f.request(t, http.MethodPost, "/api/rotation/current", `{"group":"sota","model":"glm-5.3"}`, adminHeaders, 200)
	saved, err := config.Load(f.configPath)
	if err != nil {
		t.Fatalf("reload migrated config: %v", err)
	}
	if len(saved.ServerKeys) != 1 || saved.ServerKeys[0] != renderLocalKey || saved.GoKeys[0] != renderUpstreamKey || !saved.WebUI.Enabled || saved.Anonymous {
		t.Fatal("config migration and management updates must preserve existing credentials and feature settings")
	}
	if saved.ServerKeyMetadata[config.Fingerprint(renderLocalKey)].Name != "Primary Render key" || saved.Rotation.SOTA.Alias != "sota" || saved.Rotation.FirstOutputSeconds != 30 {
		t.Fatal("new API Key metadata and rotation defaults did not persist")
	}
	if saved.WebUI.Password != "" || !config.VerifyPassword(saved.WebUI.PasswordHash, renderPassword) {
		t.Fatal("legacy WebUI password did not migrate correctly")
	}
	for _, statePath := range []string{f.configPath + ".rotation.json", f.configPath + "." + config.Fingerprint(saved.Upstream.Zen) + ".availability.json"} {
		if _, err := os.Stat(statePath); err != nil {
			t.Fatalf("state was not saved alongside config.json: %v", err)
		}
	}
}

func TestRenderRotationStreamsOnSinglePort(t *testing.T) {
	finish := make(chan struct{})
	var finishOnce sync.Once
	release := func() { finishOnce.Do(func() { close(finish) }) }
	f := newRenderFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/go/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+renderUpstreamKey {
			http.Error(w, "unexpected mock request", 502)
			return
		}
		var payload struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "glm-5.3" || !payload.Stream {
			t.Errorf("rotation did not resolve the alias to a streaming upstream request: %+v, %v", payload, err)
			http.Error(w, "invalid mock request", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"mock\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"id\":\"mock\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	server := httptest.NewServer(f.handler)
	defer server.Close()
	defer release()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"sota","stream":true,"messages":[{"role":"user","content":"Hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+renderLocalKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("stream did not start before upstream completion: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-Resolved-Model") != "glm-5.3" || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected rotation stream response: %s, %v", response.Status, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	var prefix strings.Builder
	for !strings.Contains(prefix.String(), "hello") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("first output was buffered until upstream completion: %v", err)
		}
		prefix.WriteString(line)
	}
	release()
	tail, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	body := prefix.String() + string(tail)
	if !strings.Contains(body, `"model":"sota"`) || !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream lost the public alias or completion marker: %s", body)
	}
}
