// Package main is the entry point for the Sympozium MCP bridge sidecar.
// It runs inside agent pods and translates between file-based IPC
// and remote MCP servers via JSON-RPC 2.0 Streamable HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sympozium-ai/sympozium/internal/mcpbridge"
	"github.com/sympozium-ai/sympozium/internal/skilltools"
	"github.com/sympozium-ai/sympozium/pkg/telemetry"
)

func main() {
	configPath := envOrDefault("MCP_CONFIG_PATH", "/config/mcp-servers.yaml")
	ipcPath := envOrDefault("MCP_IPC_PATH", "/ipc/tools")
	manifestPath := envOrDefault("MCP_MANIFEST_PATH", "/ipc/tools/mcp-tools.json")
	agentRunID := os.Getenv("AGENT_RUN_ID")
	debug := os.Getenv("DEBUG") == "true"

	if debug {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	log.Printf("MCP bridge starting (config=%s ipc=%s agentRunID=%s)", configPath, ipcPath, agentRunID)

	// Initialize OpenTelemetry SDK. Falls back to noop if OTEL_EXPORTER_OTLP_ENDPOINT is unset.
	tel, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName:     envOrDefault("OTEL_SERVICE_NAME", "sympozium-mcp-bridge"),
		BatchTimeout:    1 * time.Second,
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		log.Printf("OTel init failed, continuing without telemetry: %v", err)
	}
	if tel != nil {
		defer tel.Shutdown(context.Background())
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Check for stdio-adapter mode
	stdioAdapter := flag.Bool("stdio-adapter", false, "Run as stdio-to-HTTP adapter")
	skillTools := flag.Bool("serve-skill-tools", false, "Serve the run's SkillPack tools as an MCP server on loopback")
	flag.Parse()
	if !*stdioAdapter && os.Getenv("MCP_STDIO_ADAPTER") == "true" {
		*stdioAdapter = true
	}
	if !*skillTools && os.Getenv("MCP_SERVE_SKILL_TOOLS") == "true" {
		*skillTools = true
	}

	if *stdioAdapter {
		runStdioAdapter(ctx, cancel)
		return
	}

	if *skillTools {
		runSkillToolServer(ctx)
		return
	}

	// Load and validate configuration
	cfg, err := mcpbridge.LoadConfig(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("No MCP config file found at %s, exiting gracefully", configPath)
			os.Exit(0)
		}
		log.Fatalf("Failed to load MCP config: %v", err)
	}

	if err := mcpbridge.ValidateConfig(cfg); err != nil {
		log.Fatalf("Invalid MCP config: %v", err)
	}

	if len(cfg.Servers) == 0 {
		log.Printf("No MCP servers configured, exiting gracefully")
		os.Exit(0)
	}

	log.Printf("Loaded %d MCP server(s) from config", len(cfg.Servers))

	bridge := mcpbridge.NewBridge(cfg, ipcPath, manifestPath, agentRunID)

	discoverOnly := os.Getenv("MCP_DISCOVER_ONLY") == "true"
	if discoverOnly {
		log.Printf("Running in discover-only mode (init container)")
		if err := bridge.DiscoverAndWriteManifest(ctx); err != nil {
			log.Fatalf("Discovery failed: %v", err)
		}
		log.Printf("Discovery complete, exiting")
		return
	}

	if err := bridge.Run(ctx); err != nil {
		log.Fatalf("MCP bridge failed: %v", err)
	}

	log.Printf("MCP bridge exiting")
}

// runSkillToolServer serves the run's permitted SkillPack tools to a harness
// over MCP on loopback. See internal/skilltools for why this exists.
//
// It runs in the same image as the bridge because it is the same concern —
// MCP inside an agent pod — and shipping a second image for it would be a new
// build, a new tag and a new thing to keep in step for no gain.
func runSkillToolServer(ctx context.Context) {
	cfg := skilltools.Config{
		ManifestPath:    envOrDefault("SIDECAR_TOOLS_MANIFEST_PATH", "/config/sidecar-tools/sidecar-tools.json"),
		ToolPolicyAllow: os.Getenv("TOOL_POLICY_ALLOW"),
		ToolPolicyDeny:  os.Getenv("TOOL_POLICY_DENY"),
		IPCToolsDir:     envOrDefault("MCP_IPC_PATH", "/ipc/tools"),
		// Loopback only. Pod containers share a network namespace, so the
		// harness reaches this while nothing outside the pod can.
		Addr: envOrDefault("SKILL_TOOLS_ADDR", "127.0.0.1:8771"),
	}

	srv, err := skilltools.NewServer(cfg)
	if err != nil {
		log.Fatalf("skill-tools: %v", err)
	}
	if err := srv.Run(ctx); err != nil {
		log.Fatalf("skill-tools: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runStdioAdapter(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()

	stdioCmd := os.Getenv("STDIO_CMD")
	if stdioCmd == "" {
		log.Fatal("STDIO_CMD is required in stdio-adapter mode")
	}

	var stdioArgs []string
	if args := os.Getenv("STDIO_ARGS"); args != "" {
		stdioArgs = strings.Split(args, ",")
	}

	port := 8080
	if p := os.Getenv("STDIO_PORT"); p != "" {
		var err error
		port, err = strconv.Atoi(p)
		if err != nil {
			log.Fatalf("invalid STDIO_PORT: %v", err)
		}
	}

	serverName := envOrDefault("STDIO_SERVER_NAME", "stdio-mcp-server")

	// Collect STDIO_ENV_* vars for child process
	var childEnv []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "STDIO_ENV_") {
			// Strip STDIO_ENV_ prefix
			stripped := strings.TrimPrefix(e, "STDIO_ENV_")
			childEnv = append(childEnv, stripped)
		}
	}

	manager := mcpbridge.NewStdioManager(stdioCmd, stdioArgs, childEnv)
	adapter := mcpbridge.NewStdioAdapter(manager, serverName, port)

	log.Printf("Starting stdio adapter (cmd=%s args=%v port=%d server=%s)", stdioCmd, stdioArgs, port, serverName)

	if err := adapter.Run(ctx); err != nil {
		log.Fatalf("stdio adapter failed: %v", err)
	}
}
