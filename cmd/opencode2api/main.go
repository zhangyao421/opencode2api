// Command opencode2api starts the API gateway and its management interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	adminui "opencode2api/internal/admin"
	"opencode2api/internal/buildinfo"
	"opencode2api/internal/config"
	"opencode2api/internal/gateway"
	"opencode2api/internal/telemetry"
)

// version remains the linker injection point used by release builds.
var version = "dev"

func main() {
	buildinfo.Version = version
	configPath := flag.String("config", "config.json", "path to config.json")
	listen := flag.String("listen", "", "override the configured API listen address")
	webListen := flag.String("web-listen", "", "override the configured WebUI listen address")
	singlePort := flag.Bool("single-port", false, "serve the API and WebUI from one listener")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *webListen != "" {
		cfg.WebUI.Listen = *webListen
	}

	// Render exposes one public HTTP port and provides its value through PORT.
	// Keep the normal two-listener setup unless PORT is present or the operator
	// explicitly requests a single listener.
	portAddress, err := portListenAddress(os.Getenv("PORT"))
	if err != nil {
		slog.Error("invalid PORT environment variable", "error", err)
		os.Exit(1)
	}
	if portAddress != "" && *listen == "" {
		cfg.Listen = portAddress
	}
	useSinglePort := *singlePort || portAddress != ""
	if cfg.WebUI.Enabled && cfg.Listen == cfg.WebUI.Listen {
		useSinglePort = true
	}
	if useSinglePort && cfg.WebUI.Enabled {
		cfg.WebUI.Listen = cfg.Listen
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	level := new(slog.LevelVar)
	telemetry.SetLogLevel(level, cfg.Logging.Level)
	hub := telemetry.NewLogHub(cfg.Logging.RingSize)
	redactor := config.NewSecretRedactor()
	redactor.Replace(cfg)
	logger := telemetry.NewStructuredLogger(level, hub, redactor)
	monitor := telemetry.NewMonitor()
	manager, err := gateway.NewRuntimeManager(ctx, *configPath, cfg, logger, monitor, hub, redactor, level)
	if err != nil {
		logger.Error("failed to initialize runtime", "component", "runtime", "event", "runtime_initialization_failed", "error", err)
		os.Exit(1)
	}
	defer manager.Shutdown()

	apiHandler := manager.Handler()
	servers := make([]*http.Server, 0, 2)
	if useSinglePort && cfg.WebUI.Enabled {
		adminHandler := adminui.New(manager, monitor, hub, logger).Handler()
		server := &http.Server{
			Addr: cfg.Listen, Handler: singlePortHandler{api: apiHandler, webui: adminHandler},
			ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second,
		}
		servers = append(servers, server)
		go serveHTTP(cancel, logger, server, "api+webui")
	} else {
		apiServer := &http.Server{
			Addr: cfg.Listen, Handler: apiHandler, ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second,
		}
		servers = append(servers, apiServer)
		go serveHTTP(cancel, logger, apiServer, "api")

		if cfg.WebUI.Enabled {
			admin := adminui.New(manager, monitor, hub, logger)
			webServer := &http.Server{
				Addr: cfg.WebUI.Listen, Handler: admin.Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second,
			}
			servers = append(servers, webServer)
			go serveHTTP(cancel, logger, webServer, "webui")
		}
	}

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "component", "server", "event", "shutdown_failed", "address", server.Addr, "error", err)
		}
	}
}

// singlePortHandler keeps the public API paths separate from the management
// interface when both services share Render's single exposed port.
type singlePortHandler struct {
	api   http.Handler
	webui http.Handler
}

func (h singlePortHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isAPIPath(r.URL.Path) {
		h.api.ServeHTTP(w, r)
		return
	}
	h.webui.ServeHTTP(w, r)
}

func isAPIPath(path string) bool {
	return path == "/livez" || path == "/healthz" || path == "/v1" || strings.HasPrefix(path, "/v1/")
}

func portListenAddress(raw string) (string, error) {
	port := strings.TrimSpace(raw)
	if port == "" {
		return "", nil
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("PORT must be a numeric TCP port between 1 and 65535")
	}
	return net.JoinHostPort("0.0.0.0", strconv.Itoa(number)), nil
}

func serveHTTP(cancel context.CancelFunc, logger *slog.Logger, server *http.Server, component string) {
	logger.Info("server listening", "component", component, "event", "server_started", "address", server.Addr, "version", buildinfo.Version)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped unexpectedly", "component", component, "event", "server_failed", "address", server.Addr, "error", err)
		cancel()
	}
}
