// Package config loads all runtime settings using viper. Precedence (lowest to
// highest): SetDefault values < config.yaml < environment variables. Nested YAML
// keys map onto nested structs via `mapstructure` tags. Secrets live in
// config.yaml (gitignored) or env, never in config.yaml.example (committed).
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// Config is the whole application's settings, resolved once at boot.
type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	JWT      JWTConfig      `mapstructure:"jwt"`
	Redis    RedisConfig    `mapstructure:"redis"`
	CORS     CORSConfig     `mapstructure:"cors"`
	Kafka    KafkaConfig    `mapstructure:"kafka"`
}

type ServerConfig struct {
	Port            int    `mapstructure:"port"`
	Mode            string `mapstructure:"mode"`             // gin mode: "debug" | "release" | "test"
	ReadTimeout     int    `mapstructure:"read_timeout"`     // seconds
	WriteTimeout    int    `mapstructure:"write_timeout"`    // seconds
	IdleTimeout     int    `mapstructure:"idle_timeout"`     // seconds
	ShutdownTimeout int    `mapstructure:"shutdown_timeout"` // seconds to drain on SIGTERM
}

type DatabaseConfig struct {
	Host                   string `mapstructure:"host"`
	Port                   int    `mapstructure:"port"`
	User                   string `mapstructure:"user"`
	Password               string `mapstructure:"password"`
	DBName                 string `mapstructure:"dbname"`
	SSLMode                string `mapstructure:"sslmode"`
	MaxConns               int32  `mapstructure:"maxConns"`               // pool ceiling
	MaxConnLifetimeMinutes int    `mapstructure:"maxConnLifetimeMinutes"` // retire conns after
	MaxConnIdleMinutes     int    `mapstructure:"maxConnIdleMinutes"`     // close idle conns after
}

// DSN builds the pgx connection URL. 127.0.0.1 (not localhost) avoids IPv6/::1
// ambiguity; see docs/LEARNING.md P3 gotchas.
func (c *DatabaseConfig) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.User, c.Password, c.Host, c.Port, c.DBName, c.SSLMode)
}

type JWTConfig struct {
	SecretKey   string `mapstructure:"secretkey"`
	ExpiryHours int    `mapstructure:"expiryHours"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type KafkaConfig struct {
	Brokers  []string `mapstructure:"brokers"`  // e.g. ["localhost:9092"]
	Topic    string   `mapstructure:"topic"`    // outbox events land here
	DLQTopic string   `mapstructure:"dlqTopic"` // messages that exhausted retries (P22)
}

type CORSConfig struct {
	AllowedOrigins   []string `mapstructure:"allowedOrigins"`
	AllowedMethods   []string `mapstructure:"allowedMethods"`
	AllowedHeaders   []string `mapstructure:"allowedHeaders"`
	ExposedHeaders   []string `mapstructure:"exposedHeaders"`
	AllowCredentials bool     `mapstructure:"allowCredentials"`
	MaxAge           int      `mapstructure:"maxAge"`
}

// LoadConfig reads config.yaml from `path` (if present), overlays env vars, and
// unmarshals into Config. A missing config file is fine — defaults + env cover it.
func LoadConfig(path string) (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(path)

	// --- defaults (lowest precedence) ---
	viper.SetDefault("server.port", 8080)
	viper.SetDefault("server.mode", "debug")
	viper.SetDefault("server.read_timeout", 15)
	viper.SetDefault("server.write_timeout", 15)
	viper.SetDefault("server.idle_timeout", 60)
	viper.SetDefault("server.shutdown_timeout", 30)

	viper.SetDefault("database.host", "127.0.0.1")
	viper.SetDefault("database.port", 15432) // compose maps 15432->5432 (5432 taken by native PG; 5433-5532 reserved by WinNAT)
	viper.SetDefault("database.user", "crisislink")
	viper.SetDefault("database.password", "crisislink_dev_pw")
	viper.SetDefault("database.dbname", "crisislink")
	viper.SetDefault("database.sslmode", "disable")
	viper.SetDefault("database.maxConns", 10)
	viper.SetDefault("database.maxConnLifetimeMinutes", 60)
	viper.SetDefault("database.maxConnIdleMinutes", 5)

	viper.SetDefault("jwt.secretkey", "dev-secret-change-in-prod")
	viper.SetDefault("jwt.expiryHours", 24)

	viper.SetDefault("redis.host", "127.0.0.1")
	viper.SetDefault("redis.port", 6379)
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.db", 0)

	viper.SetDefault("kafka.brokers", []string{"localhost:9092"})
	viper.SetDefault("kafka.topic", "crisislink.events")
	viper.SetDefault("kafka.dlqTopic", "crisislink.events.dlq")

	viper.SetDefault("cors.allowedOrigins", []string{"http://localhost:3000", "http://localhost:5173"})
	viper.SetDefault("cors.allowedMethods", []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"})
	viper.SetDefault("cors.allowedHeaders", []string{"Content-Type", "Authorization", "X-Request-ID"})
	viper.SetDefault("cors.exposedHeaders", []string{"X-Request-ID"})
	viper.SetDefault("cors.allowCredentials", true)
	viper.SetDefault("cors.maxAge", 86400)

	// --- config file (middle precedence) ---
	if err := viper.ReadInConfig(); err != nil {
		// Not-found is acceptable; any other read error is fatal.
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}

	// --- env vars (highest precedence) ---
	// Replacer lets SERVER_PORT override server.port, DATABASE_PASSWORD override
	// database.password, etc. (dots aren't legal in env var names).
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validateSecrets(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// devJWTSecret is the placeholder shipped in the repo so a fresh clone runs. It is
// public by definition — anyone who can read the source can forge a token signed
// with it, including an admin one.
const devJWTSecret = "dev-secret-change-in-prod"

// validateSecrets refuses to boot with a known-public secret outside development.
//
// This is a FAIL-FAST, and the choice of failure mode is the point. A warning would
// be ignored; booting anyway means the deployment is silently forgeable and nobody
// finds out until it is exploited. Crashing at startup is loud, immediate, and
// happens before any traffic is served. Configuration mistakes should be caught by
// the process refusing to exist, not by an alert nobody reads.
//
// The check is scoped to release mode so local development stays frictionless —
// security controls that make development painful get disabled and stay disabled.
func (c *Config) validateSecrets() error {
	if c.Server.Mode != "release" {
		return nil
	}
	if c.JWT.SecretKey == "" || c.JWT.SecretKey == devJWTSecret {
		return fmt.Errorf(
			"refusing to start in release mode with the default JWT secret: " +
				"set jwt.secretkey (or JWT_SECRETKEY) to a strong random value")
	}
	if len(c.JWT.SecretKey) < 32 {
		// HS256 keys shorter than the 256-bit hash output weaken the HMAC and are
		// brute-forceable offline once an attacker has any valid token.
		return fmt.Errorf("jwt.secretkey must be at least 32 characters in release mode")
	}
	return nil
}
