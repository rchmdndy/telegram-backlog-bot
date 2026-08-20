package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	moderncsqlite "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	DB   *sql.DB
	Path string
}

func openDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
}
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=foreign_keys(1)&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &Store{DB: db, Path: path}, nil
}
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Path: path}
	if err = s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	for version := 1; ; version++ {
		name := fmt.Sprintf("%03d_", version)
		entries, err := migrationFS.ReadDir("migrations")
		if err != nil {
			return err
		}
		var file string
		for _, entry := range entries {
			if len(entry.Name()) >= len(name) && entry.Name()[:len(name)] == name {
				file = entry.Name()
				break
			}
		}
		if file == "" {
			break
		}
		var applied int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&applied); err != nil {
			return err
		}
		if applied != 0 {
			continue
		}
		b, err := migrationFS.ReadFile("migrations/" + file)
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(b)); err == nil {
			_, err = tx.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, time.Now().UnixMicro())
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Integrity(ctx context.Context) error {
	var result string
	if err := s.DB.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}
func (s *Store) ReadOnlyIntegrity(ctx context.Context) error {
	probe, err := OpenReadOnly(s.Path)
	if err != nil {
		return err
	}
	defer func() { _ = probe.Close() }()
	return probe.Integrity(ctx)
}
func (s *Store) Backup(ctx context.Context, output string) error {
	if output == "" {
		return fmt.Errorf("backup output is required")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	name := filepath.Join(output, "backlog-"+time.Now().UTC().Format("20060102-150405")+".db")
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	err = conn.Raw(func(raw any) (err error) {
		src, ok := raw.(interface {
			NewBackup(string) (*moderncsqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite connection does not support online backup")
		}
		backup, err := src.NewBackup("file:" + name)
		if err != nil {
			return err
		}
		defer func() {
			if finishErr := backup.Finish(); err == nil {
				err = finishErr
			}
		}()
		for {
			more, err := backup.Step(-1)
			if err != nil {
				return err
			}
			if !more {
				break
			}
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0600); err != nil {
		_ = os.Remove(name)
		return err
	}
	check, err := Open(name)
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	defer func() { _ = check.Close() }()
	if err := check.Integrity(ctx); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("backup integrity check: %w", err)
	}
	return nil
}
