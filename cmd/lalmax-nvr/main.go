package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	_ "net/http/pprof"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/api"
	"github.com/lalmax-pro/lalmax-nvr/internal/ban"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/cleanup"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/ftp"
	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/health"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/merge"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	authmw "github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware/remotelog"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/mqtt"
	"github.com/lalmax-pro/lalmax-nvr/internal/observability"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/streamhistory"
	ui "github.com/lalmax-pro/lalmax-nvr/internal/ui"
	"github.com/lalmax-pro/lalmax-nvr/internal/upload"
	"github.com/lalmax-pro/lalmax-nvr/internal/webdav"
	_ "github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	lalmaxserver "github.com/q191201771/lalmax/server"
)

var (
	configPath = flag.String("config", "config/lalmax-nvr.yaml", "path to configuration file")
	version    = flag.Bool("version", false, "print version and exit")
)

var appVersion = "0.1.0-dev" // overridden via -ldflags at build time

func autoInitConfig(configPath string) *config.Config {
	// Determine data directory
	dataDir := os.Getenv("NVR_DATA_DIR")
	if dataDir == "" {
		// Check if /data exists (Docker container)
		if info, err := os.Stat("/data"); err == nil && info.IsDir() {
			dataDir = "/data"
		} else {
			dataDir = "/var/lib/lalmax-nvr"
		}
	}

	// Check for initial password from env var
	password := os.Getenv("NVR_PASSWORD")

	cfg := &config.Config{
		Server:        config.ServerConfig{Listen: ":9090"},
		Storage:       config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: "admin"},
		Cameras:       []config.CameraConfig{},
		Cleanup:       config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:           config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:        config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{LogLevel: "info", LogFormat: "text"},
		Version:       "1.0",
	}
	// Apply defaults so all fields (HLS, etc.) are populated before saving
	cfg.ApplyDefaults()

	if password != "" {
		if len(password) < 8 {
			slog.Error("NVR_PASSWORD must be at least 8 characters")
			os.Exit(1)
		}
		cfg.Auth.Password = password
	}
	// Create data directory if needed
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		slog.Warn("failed to create data directory", "dir", dataDir, "error", err)
	}

	// Create config directory if needed
	configDir := filepath.Dir(configPath)
	if configDir != "." && configDir != "/" {
		if err := os.MkdirAll(configDir, 0755); err != nil {
			slog.Warn("failed to create config directory", "dir", configDir, "error", err)
		}
	}

	if err := config.Save(configPath, cfg); err != nil {
		slog.Warn("failed to save auto-generated config", "path", configPath, "error", err)
	} else {
		slog.Info("auto-generated default config", "path", configPath, "data_dir", dataDir)
		if password == "" {
			slog.Warn("no password set — all API requests will return 503 until a password is configured. Set via NVR_PASSWORD env var or edit the config")
		}
	}

	return cfg
}

// dockerStorageDir detects the correct storage directory for Docker environments.
// Returns empty string if not running in Docker or no Docker-specific path found.
func dockerStorageDir() string {
	// Method 1: Explicit env var (set in Dockerfile and docker-compose.yml)
	if dir := os.Getenv("NVR_DATA_DIR"); dir != "" {
		return dir
	}
	// Method 2: /data directory exists (Docker container indicator)
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	// Method 3: Docker marker files
	if _, err := os.Stat("/.dockerenv"); err == nil {
		// Running in Docker but NVR_DATA_DIR not set — check /data
		if info, err := os.Stat("/data"); err == nil && info.IsDir() {
			return "/data"
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// CLI subcommands
// ---------------------------------------------------------------------------

func cmdHealth() {
	addr := ":9090"
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--addr":
			i++
			if i < len(os.Args) {
				addr = os.Args[i]
			}
		case "--config":
			i++
			if i < len(os.Args) {
				cfg, err := config.Load(os.Args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
					os.Exit(1)
				}
				if cfg.Server.Listen != "" {
					addr = cfg.Server.Listen
				}
			}
		}
	}
	resp, err := http.Get("http://localhost" + addr + "/api/health")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Health check failed: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	os.Exit(0)
}

func cmdInit() {
	var password, dataDir, listenAddr, cfgPath, username string
	var force bool
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--password":
			i++
			if i < len(os.Args) {
				password = os.Args[i]
			}
		case "--data-dir":
			i++
			if i < len(os.Args) {
				dataDir = os.Args[i]
			}
		case "--listen":
			i++
			if i < len(os.Args) {
				listenAddr = os.Args[i]
			}
		case "--config":
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		case "--username":
			i++
			if i < len(os.Args) {
				username = os.Args[i]
			}
		case "--force":
			force = true
		}
	}
	if dataDir == "" {
		dataDir = "/var/lib/lalmax-nvr"
	}
	if listenAddr == "" {
		listenAddr = ":9090"
	}
	if cfgPath == "" {
		cfgPath = "lalmax-nvr.yaml"
	}
	if username == "" {
		username = "admin"
	}
	if password == "" {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			fmt.Print("Enter password: ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				password = scanner.Text()
			}
		}
		if password == "" {
			fmt.Fprintln(os.Stderr, "Error: password is required (use --password or provide via terminal)")
			os.Exit(1)
		}
	}
	if len(password) < 8 {
		fmt.Fprintln(os.Stderr, "Error: password must be at least 8 characters")
		os.Exit(1)
	}
	if _, err := os.Stat(cfgPath); err == nil && !force {
		fmt.Fprintf(os.Stderr, "Error: config file %s already exists (use --force to overwrite)\n", cfgPath)
		os.Exit(2)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating data directory: %v\n", err)
		os.Exit(1)
	}
	hash, err := authmw.HashPassword(password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error hashing password: %v\n", err)
		os.Exit(1)
	}
	cfg := config.Config{
		Server:        config.ServerConfig{Listen: listenAddr},
		Storage:       config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:          config.AuthConfig{Username: username, PasswordHash: hash},
		Cameras:       []config.CameraConfig{},
		Cleanup:       config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:           config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:        config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{LogLevel: "info", LogFormat: "text"},
		Version:       "1.0",
	}
	if err := config.Save(cfgPath, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Configuration saved to %s\n", cfgPath)
	fmt.Printf("Data directory: %s\n", dataDir)
	fmt.Println("\nNext steps:")
	fmt.Printf("  1. Edit %s to add your cameras\n", cfgPath)
	fmt.Printf("  2. Run: ./lalmax-nvr -config %s\n", cfgPath)
	fmt.Printf("  3. Open http://localhost%s in your browser\n", listenAddr)
	os.Exit(0)
}

func cmdHashPassword() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: lalmax-nvr hash-password <password>")
		os.Exit(1)
	}
	hash, err := authmw.HashPassword(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(hash)
	os.Exit(0)
}

func cmdEncryptConfig() {
	cfgPath := "lalmax-nvr.yaml"
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--config":
			i++
			if i < len(os.Args) {
				cfgPath = os.Args[i]
			}
		}
	}
	fields, err := config.EncryptConfigFile(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(fields) == 0 {
		fmt.Println("No plaintext sensitive fields found. All fields are already encrypted or empty.")
	} else {
		fmt.Printf("Encrypted %d sensitive field(s) in %s:\n", len(fields), cfgPath)
		for _, f := range fields {
			fmt.Printf("  - %s\n", f)
		}
	}
	os.Exit(0)
}

// resolveConfigPath follows the setup relocation marker written when the
// initial setup selected a data directory. The marker lets existing service
// commands keep using their original -config argument while the canonical
// config lives beside the database and recordings.
func resolveConfigPath(path string) string {
	markerPath := path + ".target"
	b, err := os.ReadFile(markerPath)
	if err != nil {
		return path
	}
	target := strings.TrimSpace(string(b))
	if target == "" {
		return path
	}
	if _, err := os.Stat(target); err != nil {
		return path
	}
	return target
}

// ---------------------------------------------------------------------------
// App — encapsulates all application dependencies and lifecycle
// ---------------------------------------------------------------------------

// App holds all major components of the NVR application.
//
// Initialization order (dependency chain):
//
//  1. Config — loaded/validated before App creation
//  2. Storage (DB) — SQLite, schema migrations
//  3. Metrics — Prometheus registry
//  4. Storage Manager — file operations, temp cleanup, orphan reconciliation
//  5. Auth middleware — bcrypt, auto-hash persistence
//  6. Merge manager — depends on DB, Storage, Config
//  7. Camera manager — depends on Config, Storage, DB, Metrics, Merge
//  8. HLS manager — depends on Config, Metrics
//  9. API handler — depends on DB, Storage, Auth, Config, Camera, HLS, Merge
//  10. WebDAV server — depends on Storage, Auth, DB, Config
//  11. Upload handler — depends on Storage, DB
//  12. Cleanup manager — depends on DB, Storage, Config, Metrics
//  13. MQTT client — depends on Config
//  14. FTP server — depends on Config, Storage, DB
//  15. HTTP server — depends on Router (which depends on all above)
type App struct {
	cfg        *config.Config
	configPath string
	watcher    *config.Watcher

	// Core infrastructure
	db          *storage.DB
	store       *storage.Manager
	metrics     *metrics.Metrics
	authMW      func(http.Handler) http.Handler
	multiUserMW func(http.Handler) http.Handler

	// Managers
	mergeMgr     *merge.MergeManager
	camMgr       *camera.CameraManager
	media        *media.Runtime
	mediaEngine  media.Engine
	cleanupMgr   *cleanup.CleanupManager
	healthMgr    *health.Manager
	eventBus     *event.EventBus
	eventArchive *event.Archiver
	recSched     *recorder.RecordingScheduler

	// Optional network services (nil when disabled)
	mqttClient *mqtt.Client
	ftpServer  *ftp.Server
	rtmpIngest *media.IngestHandler
	srtIngest  *media.IngestHandler
	gb28181Svr *gb28181.Server

	// Stream management
	banMgr     *ban.Manager
	historyMgr *streamhistory.Manager

	// HTTP server
	httpServer *http.Server

	// Remote log handler (nil when disabled)
	remoteLogHandler *remotelog.Handler
	otelProvider     *observability.Provider

	// Lifecycle
	cancel context.CancelFunc
}

// NewApp constructs the application with all dependencies initialized in
// correct order. It opens the database, runs migrations, creates storage
// and all managers but does NOT start any goroutines — call Start() for that.
func NewApp(cfg *config.Config, configPath string) (*App, error) {
	a := &App{
		cfg:        cfg,
		configPath: configPath,
	}

	// Step 0: Ensure storage root directory exists
	if err := os.MkdirAll(cfg.Storage.RootDir, 0755); err != nil {
		return nil, fmt.Errorf("create storage dir %s: %w", cfg.Storage.RootDir, err)
	}

	// Step 1: Open database
	dbPath := filepath.Join(cfg.Storage.RootDir, "lalmax-nvr.db")
	db, err := storage.New(dbPath)
	if err != nil {
		return nil, fmt.Errorf("db open: %w", err)
	}
	a.db = db

	ctx := context.Background()
	if err := db.Init(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("db init: %w", err)
	}
	if err := camera.SyncCamerasFromStorage(ctx, cfg, db, configPath); err != nil {
		db.Close()
		return nil, fmt.Errorf("sync cameras from storage: %w", err)
	}
	a.eventBus = event.NewEventBus(128)
	a.eventArchive = event.NewArchiver(a.eventBus, db)

	// Step 2: Metrics
	a.metrics = metrics.NewMetrics()

	// Step 2.5: Remote log handler (if enabled)
	if cfg.RemoteLog.Enabled {
		var logLevel slog.Level
		switch cfg.Observability.LogLevel {
		case "debug":
			logLevel = slog.LevelDebug
		case "warn":
			logLevel = slog.LevelWarn
		case "error":
			logLevel = slog.LevelError
		default:
			logLevel = slog.LevelInfo
		}
		rh := remotelog.New(cfg.RemoteLog.Endpoint, cfg.RemoteLog.Format, logLevel, a.metrics)
		a.remoteLogHandler = rh
		// Wrap slog.Default() with multi-handler to fan out to both stdout and remote
		if current := slog.Default(); current.Handler() != nil {
			slog.SetDefault(slog.New(remotelog.MultiHandler(current.Handler(), rh)))
		} else {
			slog.SetDefault(slog.New(rh))
		}
	}

	// Step 3: Storage manager
	store, err := storage.NewManager(cfg.Storage.RootDir, a.metrics)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: %w", err)
	}
	a.store = store

	// Cleanup temp files from previous crash
	if err := store.CleanupTempFiles(); err != nil {
		slog.Warn("temp cleanup", "error", err)
	}
	if err := db.CleanupIncomplete(ctx); err != nil {
		slog.Warn("incomplete cleanup", "error", err)
	}

	// Reconcile orphaned recording files (exists on disk but not in DB)
	cameraIDs := make(map[string]bool)
	for _, cam := range cfg.Cameras {
		cameraIDs[cam.ID] = true
	}
	reconciled, err := store.ReconcileOrphanedFiles(ctx, db, cameraIDs)
	if err != nil {
		slog.Error("failed to reconcile orphaned files", "error", err)
	} else if reconciled > 0 {
		slog.Info("reconciled orphaned recording files", "count", reconciled)
	}

	// Step 4: Auth middleware — multi-user with legacy fallback
	authMW, effectiveHash := authmw.NewAuthMiddleware(authmw.AuthProvider{
		GetUsername: func() string { return cfg.Auth.Username },
		GetHash:     func() string { return cfg.Auth.PasswordHash },
	}, cfg.Auth.Password)
	a.authMW = authMW
	if effectiveHash != "" && cfg.Auth.PasswordHash == "" && cfg.Auth.Password != "" {
		slog.Info("persisting auto-hashed password to config", "component", "main")
		cfg.Auth.PasswordHash = effectiveHash
		cfg.Auth.Password = ""
		if err := config.Save(configPath, cfg); err != nil {
			slog.Error("failed to save auto-hash", "error", err)
		}
	}

	// Multi-user auth middleware — wraps the legacy single-user auth.
	// When users exist in the DB, authenticates against the users table.
	// When no users exist yet, falls back to legacy config-based auth.
	multiUserMW := authmw.NewMultiUserAuthMiddleware(authmw.MultiUserProvider{
		GetUserByUsername: func(ctx context.Context, username string) (*model.User, error) {
			return a.db.GetUserByUsername(ctx, username)
		},
		CountUsers: func(ctx context.Context) (int, error) {
			return a.db.CountUsers(ctx)
		},
		GetLegacyUsername: func() string { return cfg.Auth.Username },
	}, authMW)
	a.multiUserMW = multiUserMW

	// Step 5: Merge manager (created before camera manager so ArchiveCamera can use it)
	a.mergeMgr = merge.NewMergeManager(
		db, store,
		func() config.MergeConfig { return cfg.Merge },
		func(cameraID string) *config.MergeConfig {
			for _, c := range cfg.Cameras {
				if c.ID == cameraID {
					return c.Merge
				}
			}
			return nil
		},
		func() []config.CameraConfig { return cfg.Cameras },
	)

	// Step 6: Camera manager
	a.camMgr = camera.NewCameraManager(cfg, store, db, configPath, a.metrics, a.mergeMgr)
	a.camMgr.SetEventBus(a.eventBus)
	// Step 6.5: Health manager (after camera manager, before streaming)
	a.healthMgr = health.NewManager(cfg.Health, db)
	if a.healthMgr != nil {
		a.camMgr.SetHealthManager(a.healthMgr)
	}
	// Wire auto-remediation into health manager
	if a.healthMgr != nil && a.camMgr != nil {
		a.healthMgr.SetRestarter(a.camMgr.RestartRecorder)
		a.healthMgr.SetCameraEnabledFn(func(cameraID string) bool {
			cam := a.camMgr.GetCameraConfig(cameraID)
			return cam != nil && cam.Enabled
		})
	}

	// Step 7: media runtime
	a.media = media.NewRuntime(cfg, a.metrics)

	// Create ban manager before media engine (provides IAuthentication)
	a.banMgr = ban.NewManager(db)

	// Create media engine with ban manager's auth for proactive rejection
	engine, err := newMediaEngine(cfg, lalmaxserver.WithAuthentication(a.banMgr))
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("media engine: %w", err)
	}
	a.mediaEngine = engine
	a.camMgr.SetMediaEngine(engine)

	// Set kick function on ban manager (deferred to break circular dependency)
	a.banMgr.SetKickFunc(func(ctx context.Context, sessionID string) error {
		return engine.KickSession(ctx, sessionID)
	})

	// Create stream history manager
	a.historyMgr = streamhistory.NewManager(db, engine)
	if emb, ok := engine.(*media.EmbeddedLalmax); ok {
		if _, err := a.historyMgr.RegisterAsHookPlugin(emb.Server()); err != nil {
			slog.Warn("failed to register history hook plugin", "error", err)
		}
	}

	// Step 7.5: GB28181 SIP server (optional)
	if cfg.GB28181.Enabled != nil && *cfg.GB28181.Enabled {
		// Skip GB28181 if ID is not configured
		if cfg.GB28181.ID == "" {
			slog.Warn("GB28181 enabled but id is empty, skipping. Set gb28181.id in config to enable")
		} else {
			gbCfg := &gb28181.Config{
				Enabled:         true,
				Host:            cfg.GB28181.Host,
				Port:            cfg.GB28181.Port,
				ID:              cfg.GB28181.ID,
				Password:        cfg.GB28181.Password,
				MediaIP:         cfg.GB28181.MediaIP,
				MediaPort:       cfg.GB28181.MediaPort,
				StandardVersion: cfg.GB28181.StandardVersion,
			}
			if err := gbCfg.Validate(); err != nil {
				db.Close()
				return nil, fmt.Errorf("gb28181 config: %w", err)
			}
			gbCfg.ApplyDefaults()
			gbSvr, _ := gb28181.NewServer(gbCfg, engine, db)
			a.gb28181Svr = gbSvr
			if cfg.GB28181.MediaPort > 0 {
				slog.Info("GB28181 SIP server started (single port mode)", "port", cfg.GB28181.Port, "media_port", cfg.GB28181.MediaPort, "id", cfg.GB28181.ID)
			} else {
				slog.Info("GB28181 SIP server started (multi port mode)", "port", cfg.GB28181.Port, "id", cfg.GB28181.ID)
			}
		}
	}

	// Step 8: Cleanup manager
	a.cleanupMgr, err = cleanup.NewCleanupManager(db, store, cfg.Cleanup, a.metrics)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("cleanup: %w", err)
	}
	if cfg.Health.Enabled {
		healthRetention, err := time.ParseDuration(cfg.Health.EventsRetention)
		if err != nil {
			slog.Warn("invalid health events_retention, disabling health cleanup", "error", err)
		} else {
			a.cleanupMgr.SetHealthConfig(true, healthRetention)
		}
	}

	// Wire ffprobe path for zero-duration recording repair
	if path, err := exec.LookPath("ffprobe"); err == nil {
		a.cleanupMgr.SetFFprobePath(path)
	}

	// Step 9: Optional MQTT client
	if cfg.MQTT.Enabled {
		a.mqttClient = mqtt.NewClient(cfg.MQTT.Broker, cfg.MQTT.ClientID, cfg.MQTT.Topic, cfg.MQTT.Username, cfg.MQTT.Password, nil)
	}

	// Wire MQTT client into health manager for event publishing
	if a.healthMgr != nil && a.mqttClient != nil {
		a.healthMgr.SetMQTTClient(a.mqttClient)
	}

	// Step 10: Optional FTP server
	if cfg.FTP.Enabled != nil && *cfg.FTP.Enabled {
		ftpAddr := fmt.Sprintf(":%d", cfg.FTP.Port)
		a.ftpServer = ftp.NewServer(ftpAddr, cfg.FTP.PassivePortRange, cfg.Auth.Username, cfg.Auth.Password, store, db)
	}

	// Step 11.5: OpenTelemetry providers. The local API dashboard works without
	// an exporter; external OTLP/HTTP export is configured only through YAML.
	otlpTimeout, _ := time.ParseDuration(cfg.Observability.OTLP.Timeout)
	otlpMetricsInterval, _ := time.ParseDuration(cfg.Observability.OTLP.MetricsExportInterval)
	a.otelProvider, err = observability.NewProvider(context.Background(), appVersion, observability.ProviderConfig{
		Enabled:               cfg.Observability.OTLP.Enabled,
		Endpoint:              cfg.Observability.OTLP.Endpoint,
		ServiceName:           cfg.Observability.OTLP.ServiceName,
		TracesEnabled:         cfg.Observability.OTLP.ExportTraces(),
		MetricsEnabled:        cfg.Observability.OTLP.ExportMetrics(),
		Headers:               cfg.Observability.OTLP.Headers,
		Timeout:               otlpTimeout,
		MetricsExportInterval: otlpMetricsInterval,
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("observability: %w", err)
	}

	// Step 12: Build HTTP router
	a.httpServer = &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: a.buildRouter(),
	}

	return a, nil
}

// RestartGB28181 restarts the GB28181 SIP server with new configuration.
func (a *App) RestartGB28181(ctx context.Context, cfg *config.GB28181Config) error {
	// Dispose old server
	if a.gb28181Svr != nil {
		a.gb28181Svr.Stop()
		a.gb28181Svr = nil
	}

	// If disabled, just stop
	enabled := cfg.Enabled != nil && *cfg.Enabled
	if !enabled {
		slog.Info("GB28181 disabled")
		return nil
	}

	// Validate config
	if cfg.ID == "" {
		slog.Warn("GB28181 enabled but id is empty, skipping")
		return nil
	}

	// Convert config
	gbCfg := &gb28181.Config{
		Enabled:         enabled,
		Host:            cfg.Host,
		Port:            cfg.Port,
		ID:              cfg.ID,
		Password:        cfg.Password,
		MediaIP:         cfg.MediaIP,
		MediaPort:       cfg.MediaPort,
		StandardVersion: cfg.StandardVersion,
	}
	if err := gbCfg.Validate(); err != nil {
		return fmt.Errorf("gb28181 config: %w", err)
	}
	gbCfg.ApplyDefaults()

	// Create new server
	gbSvr, _ := gb28181.NewServer(gbCfg, a.mediaEngine, a.db)
	a.gb28181Svr = gbSvr

	slog.Info("GB28181 SIP server restarted", "port", cfg.Port, "id", cfg.ID)
	return nil
}

func (a *App) CurrentGB28181Server() *gb28181.Server {
	return a.gb28181Svr
}

// buildRouter constructs the chi router with all routes mounted.
func (a *App) buildRouter() http.Handler {
	cfg := a.cfg

	cloudProxy := api.NewLocalXiaomiAuth(cfg)
	handler := api.NewHandler(a.db, a.store, a.authMW, cfg, a.camMgr, a.configPath, a.mergeMgr, cloudProxy)
	handler.SetMultiUserAuthMW(a.multiUserMW)
	handler.SetRestartFunc(func() {
		go a.restartProcess()
	})

	// Wire streaming managers
	handler.SetMediaEngine(a.mediaEngine)
	if a.gb28181Svr != nil {
		handler.SetGB28181Server(a.gb28181Svr)
		handler.SetGB28181ServerInstance(a.gb28181Svr)
	}
	handler.SetGB28181Restarter(a)
	handler.SetWSManager(a.media.WS())
	handler.SetHealthManager(a.healthMgr)
	handler.SetStabilityProvider(a.healthMgr)
	if a.banMgr != nil {
		handler.SetBanManager(a.banMgr)
	}
	if a.watcher != nil {
		handler.SetConfigWatcher(a.watcher)
	}

	// Wire AI Manager
	aiMgr := ai.NewManager(cfg.AI)
	if a.db != nil {
		aiMgr.SetStore(a.db)
	}
	handler.SetAIManager(aiMgr)

	// Create and populate StreamRegistry for protocol discovery
	reg := api.NewStreamRegistry()
	if a.mediaEngine != nil {
		hlsCodecs := []model.Format{model.FormatH264, model.FormatH265}
		if cfg.IsHLSEnabled() {
			reg.Register(&api.StaticStreamHandler{Protocol: "hls", Codecs: hlsCodecs})
			if cfg.HLS.LowLatency {
				reg.Register(&api.StaticStreamHandler{Protocol: "ll-hls", Codecs: hlsCodecs})
			} else {
				reg.Register(&api.ConditionalStaticStreamHandler{
					StaticStreamHandler: api.StaticStreamHandler{Protocol: "ll-hls", Codecs: hlsCodecs},
					Available:           false,
					Reason:              "Enable low-latency HLS in Settings",
				})
			}
		} else {
			reg.Register(&api.ConditionalStaticStreamHandler{
				StaticStreamHandler: api.StaticStreamHandler{Protocol: "hls", Codecs: hlsCodecs},
				Available:           false,
				Reason:              "Enable HLS in Settings",
			})
			reg.Register(&api.ConditionalStaticStreamHandler{
				StaticStreamHandler: api.StaticStreamHandler{Protocol: "ll-hls", Codecs: hlsCodecs},
				Available:           false,
				Reason:              "Enable HLS in Settings",
			})
		}
		reg.Register(&api.StaticStreamHandler{
			Protocol: "webrtc",
			Codecs:   []model.Format{model.FormatH264, model.FormatH265},
		})
		reg.Register(&api.StaticStreamHandler{
			Protocol: "flv",
			Codecs:   []model.Format{model.FormatH264, model.FormatH265},
		})
		reg.Register(&api.StaticStreamHandler{
			Protocol: "ws-flv",
			Codecs:   []model.Format{model.FormatH264, model.FormatH265},
		})
		// HTTP fMP4 stream handler is available when media engine is enabled
		reg.Register(&api.FMP4StreamHandler{})
	}
	// WebSocket stream handler is always available
	reg.Register(&api.WSStreamHandler{})
	handler.SetStreamRegistry(reg)

	// WebDAV
	var davHandler http.Handler
	if cfg.WebDAV.Enabled != nil && *cfg.WebDAV.Enabled {
		davSrv := webdav.NewServer(a.store, cfg.WebDAV.PathPrefix, a.authMW, a.db, cfg.WebDAV.ReadWrite)
		davHandler = davSrv.Handler()
	}

	// Upload handler
	uploadHandler := upload.NewHandler(a.store, a.db, 100<<20) // 100MB max

	// Register WebDAV methods with chi so it doesn't reject them as 405.
	chi.RegisterMethod("PROPFIND")
	chi.RegisterMethod("MKCOL")
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
	chi.RegisterMethod("COPY")
	chi.RegisterMethod("MOVE")

	r := chi.NewRouter()
	r.Use(authmw.RequestLogger(slog.Default(), "/api/health", "/api/readyz", "/api/observability/api"))
	r.Use(middleware.Recoverer)
	r.Use(authmw.SecurityHeaders)
	r.Use(authmw.COOPHeaders)

	// Prometheus metrics — independent auth when configured, public otherwise
	if cfg.MetricsAuth.IsConfigured() {
		metricsAuthMW, _ := authmw.NewAuthMiddleware(authmw.AuthProvider{
			GetUsername: func() string { return cfg.MetricsAuth.Username },
			GetHash:     func() string { return cfg.MetricsAuth.PasswordHash },
		}, cfg.MetricsAuth.Password)
		r.With(metricsAuthMW).Handle("/metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	} else {
		r.Handle("/metrics", promhttp.HandlerFor(a.metrics.Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError}))
	}

	r.Mount("/", handler.Routes())

	// WebDAV
	if davHandler != nil {
		r.Mount(cfg.WebDAV.PathPrefix, davHandler)
	}

	// Upload routes (authenticated)
	r.Group(func(r chi.Router) {
		r.Use(a.authMW)
		uploadHandler.RegisterRoutes(r)
	})

	// Static UI — serve from embedded filesystem
	staticContent, err := fs.Sub(ui.StaticFS, "static")
	if err != nil {
		slog.Error("static fs", "error", err)
		os.Exit(1)
	}
	fileServer := http.FileServer(http.FS(staticContent))
	// Static files served without auth — SPA handles login flow client-side.
	// All sensitive data is protected via API endpoints in handler.Routes().
	r.NotFound(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fileServer.ServeHTTP(w, r)
	}))

	return r
}

// restartProcess performs a graceful in-place process restart. Using exec
// keeps the PID stable, which is important for scripts/unix/start.sh and
// systemd-style supervisors that track the service by PID.
func (a *App) restartProcess() {
	// Give the setup response time to reach the browser before stopping HTTP.
	time.Sleep(300 * time.Millisecond)
	if err := a.Stop(); err != nil {
		slog.Error("service restart shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("restarting lalmax-nvr after initial setup")
	if err := restartProcessInPlace(os.Args[0], os.Args, os.Environ()); err != nil {
		slog.Error("service restart failed", "error", err)
		os.Exit(1)
	}
}

// Start launches all service goroutines and blocks until a shutdown signal
// is received or the context is cancelled.
func (a *App) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	// Start camera manager
	go func() {
		if err := a.camMgr.Start(ctx); err != nil {
			slog.Error("camera manager", "error", err)
		}
	}()

	// Start recording scheduler (recording plans)
	a.recSched = recorder.NewRecordingScheduler(a.db)
	a.recSched.Start(ctx, a.camMgr.PauseRecording, a.camMgr.ResumeRecording)

	// Start health manager (optional, after camera manager)
	if a.healthMgr != nil {
		if err := a.healthMgr.Start(ctx); err != nil {
			slog.Error("health manager", "error", err)
		}
	}

	if a.eventArchive != nil {
		go a.eventArchive.Start(ctx)
	}

	// Start merge manager
	go func() {
		if a.cfg.Merge.Enabled {
			a.mergeMgr.Run(ctx)
			slog.Info("merge-manager stopped")
		}
	}()

	// Start cleanup manager
	go a.cleanupMgr.Run(ctx)

	// Start MQTT client (optional)
	if a.mqttClient != nil {
		go func() {
			if err := a.mqttClient.Start(ctx); err != nil {
				slog.Error("mqtt", "error", err)
			}
		}()
	}

	// Start FTP server (optional)
	if a.ftpServer != nil {
		go func() {
			if err := a.ftpServer.Start(ctx); err != nil {
				slog.Error("ftp", "error", err)
			}
		}()
	}

	// Start media runtime (optional protocols live behind this boundary)
	if a.media != nil {
		go func() {
			if err := a.media.Start(ctx); err != nil {
				slog.Error("media runtime", "error", err)
			}
		}()
	}
	if a.mediaEngine != nil {
		go func() {
			if err := a.mediaEngine.Start(ctx); err != nil {
				slog.Error("media engine", "error", err)
			}
		}()

		// Start stream history manager
		if a.historyMgr != nil {
			if err := a.historyMgr.Start(ctx); err != nil {
				slog.Error("stream history manager", "error", err)
			}
		}

		// Wire RTMP ingest via lalmax
		if a.cfg.RTMP.Enabled != nil && *a.cfg.RTMP.Enabled {
			keyToCamera := media.BuildReverseMap(a.cfg.RTMP.StreamKeys)
			resolver := func(streamName string) (string, bool) {
				if camID, ok := keyToCamera[streamName]; ok {
					return camID, true
				}
				for _, cam := range a.cfg.Cameras {
					if cam.ID == streamName {
						return cam.ID, true
					}
				}
				return "", false
			}

			a.rtmpIngest = media.NewRTMPIngestHandler(a.mediaEngine, resolver, nil, nil)
			if err := a.rtmpIngest.Start(ctx); err != nil {
				slog.Warn("rtmp ingest handler failed to start", "error", err)
			}
		}

		// Wire SRT ingest via lalmax
		if a.cfg.SRT.Enabled != nil && *a.cfg.SRT.Enabled {
			srtResolver := func(streamName string) (string, bool) {
				for _, cam := range a.cfg.Cameras {
					if cam.ID == streamName {
						return cam.ID, true
					}
				}
				return "", false
			}

			a.srtIngest = media.NewSRTIngestHandler(a.mediaEngine, srtResolver, nil, nil)
			if err := a.srtIngest.Start(ctx); err != nil {
				slog.Warn("srt ingest handler failed to start", "error", err)
			}
		}

		// Start stream event monitoring (stops/starts recorders based on lalmax stream status)
		go a.camMgr.MonitorStreamEvents(ctx)
	}

	// Start HTTP server
	go func() {
		slog.Info("lalmax-nvr listening", "version", appVersion, "addr", a.cfg.Server.Listen)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down", "signal", sig.String())

	return a.Stop()
}

// Stop gracefully shuts down all components in reverse dependency order
// with a 30-second timeout. Shutdown order:
//
//  1. HTTP server — stop accepting new requests
//  2. FTP server — close listener
//  3. MQTT client — disconnect from broker
//  4. WebDAV — handled via HTTP server shutdown
//  5. Cleanup manager — stopped via context cancellation
//  6. Merge manager — stopped via context cancellation
//  7. HLS manager — stop all active streams
//  8. Camera manager — stop all recorders
//  9. Storage (DB) — close connection
func (a *App) Stop() error {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		// Cancel context to signal all goroutines (FTP, cleanup, merge, MQTT)
		if a.cancel != nil {
			a.cancel()
		}

		log := authmw.ComponentLogger("server")
		log.Info("shutting down...")

		// 0. Remote log handler — flush remaining logs
		if a.remoteLogHandler != nil {
			log.Info("flushing remote log handler")
			a.remoteLogHandler.Close()
		}

		// 1. HTTP server — stop accepting new requests
		log.Info("stopping HTTP server")
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			log.Warn("HTTP server shutdown error", "error", err)
		}

		// 2. FTP server
		if a.ftpServer != nil {
			log.Info("stopping FTP server")
			a.ftpServer.Close()
		}

		if a.media != nil {
			log.Info("stopping media runtime")
			_ = a.media.Stop()
		}
		if a.historyMgr != nil {
			log.Info("stopping stream history manager")
			a.historyMgr.Stop()
		}
		if a.rtmpIngest != nil {
			log.Info("stopping RTMP ingest handler")
			a.rtmpIngest.Stop()
		}
		if a.srtIngest != nil {
			log.Info("stopping SRT ingest handler")
			a.srtIngest.Stop()
		}
		if a.gb28181Svr != nil {
			log.Info("stopping GB28181 SIP server")
			a.gb28181Svr.Stop()
		}
		if a.mediaEngine != nil {
			log.Info("stopping media engine")
			_ = a.mediaEngine.Shutdown(shutdownCtx)
		}

		// 6. MQTT client
		if a.mqttClient != nil {
			log.Info("stopping MQTT client")
			if err := a.mqttClient.Stop(); err != nil {
				log.Warn("MQTT stop error", "error", err)
			}
		}

		// 4. WebDAV — no explicit stop needed (handler served by HTTP server)

		// 5. Cleanup manager — stopped via context cancellation above
		log.Info("cleanup manager stopped")

		// 6. Merge manager — stopped via context cancellation above
		log.Info("merge manager stopped")

		// 7.5. Health manager (before camera manager)
		if a.healthMgr != nil {
			log.Info("stopping health manager")
			a.healthMgr.Stop()
		}

		// 7.9. Recording scheduler
		if a.recSched != nil {
			log.Info("stopping recording scheduler")
			a.recSched.Stop()
		}

		log.Info("stopping camera manager")
		if err := a.camMgr.Stop(); err != nil {
			log.Warn("camera manager stop error", "error", err)
		}

		if a.otelProvider != nil {
			log.Info("flushing OpenTelemetry providers")
			if err := a.otelProvider.Shutdown(shutdownCtx); err != nil {
				log.Warn("OpenTelemetry shutdown error", "error", err)
			}
		}

		// 9. Storage (DB)
		log.Info("closing database")
		a.db.Close()

		close(done)
	}()

	select {
	case <-done:
		authmw.ComponentLogger("server").Info("shutdown complete")
	case <-shutdownCtx.Done():
		authmw.ComponentLogger("server").Warn("shutdown timed out, forcing exit")
	}

	slog.Info("lalmax-nvr stopped")
	return nil
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	// Dispatch CLI subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "health":
			cmdHealth()
		case "init":
			cmdInit()
		case "hash-password":
			cmdHashPassword()
		case "encrypt-config":
			cmdEncryptConfig()
		}
	}

	// Setup initial logger before config load
	logger := authmw.SetupLogger("info", "text")
	slog.SetDefault(logger)

	flag.Parse()

	if *version {
		fmt.Printf("lalmax-nvr version %s\n", appVersion)
		os.Exit(0)
	}

	// Load and validate config. A setup relocation marker may point this
	// process at the canonical config inside the selected data directory.
	effectiveConfigPath := resolveConfigPath(*configPath)
	cfg, err := config.Load(effectiveConfigPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("config", "error", err)
			os.Exit(1)
		}
		// Auto-initialize: config file not found, generate defaults
		slog.Info("config file not found, auto-initializing with defaults", "path", effectiveConfigPath)
		cfg = autoInitConfig(effectiveConfigPath)
	}
	// Fix Docker storage path mismatch: if running in Docker but config has
	// the non-Docker default /var/lib/lalmax-nvr, auto-fix to /data.
	if dockerDir := dockerStorageDir(); dockerDir != "" {
		if cfg.Storage.RootDir == "/var/lib/lalmax-nvr" || cfg.Storage.RootDir == "" {
			slog.Warn("auto-fixing storage.root_dir for Docker environment",
				"old", cfg.Storage.RootDir, "new", dockerDir)
			cfg.Storage.RootDir = dockerDir
			if err := config.Save(effectiveConfigPath, cfg); err != nil {
				slog.Warn("failed to save auto-fixed config", "error", err)
			}
		}
	}

	if err := config.Validate(cfg); err != nil {
		slog.Error("config validation", "error", err)
		os.Exit(1)
	}

	// Reconfigure logger with user settings after config load
	logger = authmw.SetupLogger(cfg.Observability.LogLevel, cfg.Observability.LogFormat)
	slog.SetDefault(logger)

	// Create config watcher for external change detection and hot-reload
	watcher, err := config.NewWatcher(cfg, effectiveConfigPath)
	if err != nil {
		slog.Warn("failed to create config watcher", "error", err)
	}

	app, err := NewApp(cfg, effectiveConfigPath)
	if err != nil {
		slog.Error("init", "error", err)
		os.Exit(1)
	}
	app.watcher = watcher

	if err := app.Start(); err != nil {
		slog.Error("run", "error", err)
		os.Exit(1)
	}
}

func newMediaEngine(cfg *config.Config, opts ...interface{}) (media.Engine, error) {
	if cfg.Media.Mode == "http" {
		return media.NewLalmaxHTTP(media.LalmaxHTTPConfig{
			BaseURL:   cfg.Media.LalmaxHTTPAddr,
			PublicURL: cfg.Media.LalmaxPublicURL,
			RTMPPort:  cfg.Media.RTMPPort,
			RTSPPort:  cfg.Media.RTSPPort,
			HTTPPort:  cfg.Media.HTTPPort,
		})
	}

	// Extract lalmax server options from variadic args
	var svrOpts []interface{}
	for _, o := range opts {
		svrOpts = append(svrOpts, o)
	}

	emb, err := media.NewEmbeddedLalmax(media.EmbeddedLalmaxConfig{
		HTTPAddr:              cfg.Media.LalmaxHTTPAddr,
		PublicURL:             cfg.Media.LalmaxPublicURL,
		ConfigPath:            cfg.Media.LalmaxConfigPath,
		RTMPPort:              cfg.RTMP.Port,
		RTSPPort:              cfg.Media.RTSPPort,
		HTTPPort:              cfg.Media.HTTPPort,
		RTMPEnabled:           cfg.RTMP.Enabled != nil && *cfg.RTMP.Enabled,
		SRTPort:               cfg.SRT.Port,
		SRTEnabled:            cfg.SRT.Enabled != nil && *cfg.SRT.Enabled,
		HLSEnabled:            cfg.IsHLSEnabled(),
		HLSOnDemand:           cfg.IsHLSOnDemand(),
		HLSIdleTimeoutMs:      int(cfg.HLSIdleTimeout() / time.Millisecond),
		LalFragmentDurationMs: cfg.HLS.LalFragmentDurationMs,
		LalFragmentNum:        cfg.HLS.LalFragmentNum,
		LalCleanupMode:        cfg.HLS.LalCleanupMode,
		LalUseMemory:          cfg.HLS.LalUseMemory,
		LalmaxSegmentCount:    cfg.HLS.SegmentCount,
		LalmaxSegmentDuration: cfg.HLS.LalmaxSegmentDuration,
		LalmaxPartDuration:    cfg.HLS.LalmaxPartDuration,
		RTSPAuthEnable:        cfg.Media.RTSPAuthEnable,
		RTSPAuthMethod:        cfg.Media.RTSPAuthMethod,
		RTSPUsername:          cfg.Media.RTSPUsername,
		RTSPPassword:          cfg.Media.RTSPPassword,
		LalLogLevel:           cfg.Observability.LogLevel,
	}, toLalMaxOpts(svrOpts)...)
	if err != nil {
		return nil, err
	}
	return emb, nil
}

func toLalMaxOpts(opts []interface{}) []lalmaxserver.LalMaxServerOption {
	var result []lalmaxserver.LalMaxServerOption
	for _, o := range opts {
		if opt, ok := o.(lalmaxserver.LalMaxServerOption); ok {
			result = append(result, opt)
		}
	}
	return result
}
