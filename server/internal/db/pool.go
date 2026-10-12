package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PoolConfig struct {
	URL               string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
}

func parsePoolConfig(input PoolConfig) (*pgxpool.Config, error) {
	if input.URL == "" {
		return nil, errors.New("database URL is required")
	}
	if input.MaxConns < 1 {
		return nil, errors.New("database max connections must be positive")
	}
	if input.MinConns < 0 || input.MinConns > input.MaxConns {
		return nil, errors.New("database min connections must be between zero and max connections")
	}

	cfg, err := pgxpool.ParseConfig(input.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	cfg.MaxConns = input.MaxConns
	cfg.MinConns = input.MinConns
	if input.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = input.MaxConnLifetime
	}
	if input.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = input.MaxConnIdleTime
	}
	if input.HealthCheckPeriod > 0 {
		cfg.HealthCheckPeriod = input.HealthCheckPeriod
	}
	return cfg, nil
}

func NewPool(ctx context.Context, input PoolConfig) (*pgxpool.Pool, error) {
	cfg, err := parsePoolConfig(input)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}
