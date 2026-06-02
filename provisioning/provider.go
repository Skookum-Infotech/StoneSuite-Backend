// Package provisioning creates, migrates, seeds, and tears down per-tenant
// databases. All provider-specific behavior lives behind the DBProvider
// interface so swapping Postgres hosts (local, Neon shared project, a dedicated
// Neon project, RDS, ...) touches one implementation, not the orchestration.
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// DBProvider abstracts where/how a tenant database is created and addressed.
type DBProvider interface {
	// CreateDatabase creates an empty database (idempotent — ok if it exists).
	CreateDatabase(ctx context.Context, dbName string) error
	// DropDatabase removes a database (idempotent — ok if absent).
	DropDatabase(ctx context.Context, dbName string) error
	// DSNFor returns a connection string for the named tenant database.
	DSNFor(dbName string) (string, error)
}

var dbNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// SanitizeDBName builds a safe tenant database name from a slug.
func SanitizeDBName(slug string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(slug))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "", fmt.Errorf("slug %q has no usable characters", slug)
	}
	name := "tenant_" + s
	if !dbNameRe.MatchString(name) {
		return "", fmt.Errorf("cannot derive a valid db name from slug %q", slug)
	}
	return name, nil
}

// SQLProvider creates tenant databases by issuing CREATE/DROP DATABASE on an
// admin connection. Portable across any Postgres host, including Neon.
type SQLProvider struct {
	adminDSN string // DSN with privileges to create databases (e.g. .../postgres)
}

// NewSQLProvider builds an SQLProvider from an admin DSN.
func NewSQLProvider(adminDSN string) (*SQLProvider, error) {
	if adminDSN == "" {
		return nil, errors.New("admin DSN is empty")
	}
	if _, err := url.Parse(adminDSN); err != nil {
		return nil, fmt.Errorf("parse admin dsn: %w", err)
	}
	return &SQLProvider{adminDSN: adminDSN}, nil
}

func (p *SQLProvider) CreateDatabase(ctx context.Context, dbName string) error {
	if !dbNameRe.MatchString(dbName) {
		return fmt.Errorf("invalid db name %q", dbName)
	}
	conn, err := pgx.Connect(ctx, p.adminDSN)
	if err != nil {
		return fmt.Errorf("admin connect: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)", dbName).Scan(&exists); err != nil {
		return fmt.Errorf("check db exists: %w", err)
	}
	if exists {
		return nil
	}
	// dbName is validated against dbNameRe, so quoting it is safe.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, dbName)); err != nil {
		return fmt.Errorf("create database %q: %w", dbName, err)
	}
	return nil
}

func (p *SQLProvider) DropDatabase(ctx context.Context, dbName string) error {
	if !dbNameRe.MatchString(dbName) {
		return fmt.Errorf("invalid db name %q", dbName)
	}
	conn, err := pgx.Connect(ctx, p.adminDSN)
	if err != nil {
		return fmt.Errorf("admin connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s" WITH (FORCE)`, dbName)); err != nil {
		return fmt.Errorf("drop database %q: %w", dbName, err)
	}
	return nil
}

func (p *SQLProvider) DSNFor(dbName string) (string, error) {
	if !dbNameRe.MatchString(dbName) {
		return "", fmt.Errorf("invalid db name %q", dbName)
	}
	u, err := url.Parse(p.adminDSN)
	if err != nil {
		return "", fmt.Errorf("parse admin dsn: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}
