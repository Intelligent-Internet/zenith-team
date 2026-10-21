package db

import (
	"testing"
	"time"
)

func TestPoolConfigAppliesExplicitLimits(t *testing.T) {
	t.Parallel()

	cfg, err := parsePoolConfig(PoolConfig{
		URL:               "postgres://zenith_team:zenith_team@localhost/zenith_team?sslmode=disable",
		MaxConns:          30,
		MinConns:          6,
		MaxConnLifetime:   90 * time.Minute,
		MaxConnIdleTime:   15 * time.Minute,
		HealthCheckPeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("parsePoolConfig() error = %v", err)
	}

	if cfg.MaxConns != 30 || cfg.MinConns != 6 {
		t.Errorf("pool sizes = %d/%d, want 30/6", cfg.MaxConns, cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 90*time.Minute {
		t.Errorf("MaxConnLifetime = %s", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 15*time.Minute {
		t.Errorf("MaxConnIdleTime = %s", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != 30*time.Second {
		t.Errorf("HealthCheckPeriod = %s", cfg.HealthCheckPeriod)
	}
}

func TestPoolConfigRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	_, err := parsePoolConfig(PoolConfig{
		URL:      "postgres://zenith_team:zenith_team@localhost/zenith_team",
		MaxConns: 2,
		MinConns: 3,
	})
	if err == nil {
		t.Fatal("parsePoolConfig() error = nil, want invalid bounds error")
	}
}
