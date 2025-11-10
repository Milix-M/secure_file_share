package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime configuration derived from environment variables.
type Config struct {
	Addr              string
	StoragePath       string
	MaxUploadSize     int64
	StorageCapacity   int64
	UploadTTL         time.Duration
	CleanupInterval   time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ShutdownTimeout   time.Duration
	WebSocketIdlePing time.Duration
	AllowedOrigins    []string
}

const (
	defaultAddr              = ":8080"
	defaultStoragePath       = "/dev/shm/ephemeralpod"
	defaultMaxUploadSizeMB   = 200
	defaultStorageCapacityMB = 12 * 1024
	defaultUploadTTL         = 30 * time.Minute
	defaultCleanupInterval   = 1 * time.Minute
	defaultReadTimeout       = 10 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultShutdownTimeout   = 5 * time.Second
	defaultIdlePing          = 30 * time.Second
)

// Load reads configuration from the current environment and applies defaults when not provided.
func Load() Config {
	cfg := Config{
		Addr:              getEnv("EPHEMERALPOD_ADDR", defaultAddr),
		StoragePath:       getEnv("EPHEMERALPOD_STORAGE_PATH", defaultStoragePath),
		MaxUploadSize:     int64(getEnvInt("EPHEMERALPOD_MAX_UPLOAD_SIZE_MB", defaultMaxUploadSizeMB)) * 1024 * 1024,
		StorageCapacity:   int64(getEnvInt("EPHEMERALPOD_STORAGE_CAPACITY_MB", defaultStorageCapacityMB)) * 1024 * 1024,
		UploadTTL:         getEnvDuration("EPHEMERALPOD_UPLOAD_TTL", defaultUploadTTL),
		CleanupInterval:   getEnvDuration("EPHEMERALPOD_CLEANUP_INTERVAL", defaultCleanupInterval),
		ReadTimeout:       getEnvDuration("EPHEMERALPOD_HTTP_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:      getEnvDuration("EPHEMERALPOD_HTTP_WRITE_TIMEOUT", defaultWriteTimeout),
		ShutdownTimeout:   getEnvDuration("EPHEMERALPOD_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		WebSocketIdlePing: getEnvDuration("EPHEMERALPOD_WS_IDLE_PING", defaultIdlePing),
		AllowedOrigins:    parseOrigins(getEnv("EPHEMERALPOD_ALLOWED_ORIGINS", "*")),
	}

	if cfg.MaxUploadSize <= 0 {
		cfg.MaxUploadSize = int64(defaultMaxUploadSizeMB) * 1024 * 1024
	}

	if cfg.StorageCapacity <= 0 {
		cfg.StorageCapacity = int64(defaultStorageCapacityMB) * 1024 * 1024
	}

	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = defaultCleanupInterval
	}

	if cfg.UploadTTL <= 0 {
		cfg.UploadTTL = defaultUploadTTL
	}

	if cfg.WebSocketIdlePing <= 0 {
		cfg.WebSocketIdlePing = defaultIdlePing
	}

	return cfg
}

func getEnv(key string, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		parsed, err := strconv.Atoi(val)
		if err != nil {
			panic(fmt.Sprintf("invalid value for %s: %v", key, err))
		}
		return parsed
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		dur, err := time.ParseDuration(val)
		if err != nil {
			panic(fmt.Sprintf("invalid duration for %s: %v", key, err))
		}
		return dur
	}
	return fallback
}

func parseOrigins(raw string) []string {
	parts := strings.Split(raw, ",")
	var origins []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		origins = append(origins, trimmed)
	}
	if len(origins) == 0 {
		return []string{"*"}
	}
	return origins
}
