// Package common holds cross-cutting helpers used by every layer: the shared
// structured logger and the HTTP response envelope. It depends on no other
// internal package, so anything can import it without cycles.
package common

import (
	"log"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger is the process-wide structured logger (zap SugaredLogger — the ergonomic
// API with Infow/Errorf). Initialised once at boot via InitLogger.
var Logger *zap.SugaredLogger

// InitLogger builds the logger based on mode. "release" => JSON, info level
// (production). Anything else => colored, debug level (development).
func InitLogger(mode string) {
	var (
		zapLogger *zap.Logger
		err       error
	)
	if mode == "release" {
		cfg := zap.NewProductionConfig()
		cfg.EncoderConfig.TimeKey = "ts"
		cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		zapLogger, err = cfg.Build()
	} else {
		cfg := zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zapLogger, err = cfg.Build()
	}
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	Logger = zapLogger.Sugar()
}
