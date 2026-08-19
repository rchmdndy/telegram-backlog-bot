package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	_ "modernc.org/sqlite"
	moderncsqlite "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

var ErrDuplicate = errors.New("duplicate")
var ErrReceiptConflict = errors.New("mutation receipt conflict")

func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

type NotificationRun struct {
	LocalDate, Status       string
	ScheduledFor, UpdatedAt time.Time
	AttemptCount            int
}
type NotificationPart struct {
	Index    int
	Payload  string
	Keyboard string
}

type NotificationItem struct {
	Ordinal       int
	BacklogItemID string
	ProjectName   string
	Title         string
	Quadrant      string
	DeadlineDate  string
}

const notificationRetryWindow = 15 * time.Minute

func (s *Store) RecoverStaleNotifications(ctx context.Context, before time.Time) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, `UPDATE notification_runs SET status='failed', last_error='stale sending recovery', attempt_count=attempt_count+1, retry_at=CASE WHEN attempt_count+1 < 4 THEN ? ELSE NULL END, updated_at=? WHERE status='sending' AND updated_at < ?`, micros(now.Add(notificationRetryWindow)), micros(now), micros(before))
	return err
}
func (s *Store) RetryFailedNotifications(ctx context.Context, date string, now time.Time) error {
	now = now.UTC()
	_, err := s.DB.ExecContext(ctx, `UPDATE notification_runs SET status='sending', retry_at=NULL, updated_at=? WHERE local_date=? AND status='failed' AND attempt_count < 4 AND retry_at IS NOT NULL AND retry_at <= ?`, micros(now), date, micros(now))
	return err
}

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

func micros(t time.Time) int64     { return t.UTC().UnixMicro() }
func fromMicros(v int64) time.Time { return time.UnixMicro(v).UTC() }
func nullableTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMicros(v.Int64)
	return &t
}
func createProject(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, p domain.Project) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO projects(id,name,normalized_name,description,status,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.NormalizedName, p.Description, p.Status, micros(p.CreatedAt), micros(p.UpdatedAt), nullableMicros(p.ArchivedAt))
	return err
}

func nullableMicros(t *time.Time) any {
	if t == nil {
		return nil
	}
	return micros(*t)
}

func (s *Store) CreateProject(ctx context.Context, p domain.Project) error {
	return createProject(ctx, s.DB, p)
}
func (s *Store) CreateProjectTx(ctx context.Context, tx *sql.Tx, p domain.Project) error {
	return createProject(ctx, tx, p)
}

func (s *Store) ActiveProjectNameConflict(ctx context.Context, normalized, excludeID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE status='active' AND normalized_name=? AND id<>?`, normalized, excludeID).Scan(&n)
	return n != 0, err
}
func (s *Store) ActiveProjectNameConflictTx(ctx context.Context, tx *sql.Tx, normalized, excludeID string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE status='active' AND normalized_name=? AND id<>?`, normalized, excludeID).Scan(&n)
	return n != 0, err
}
func (s *Store) ListProjects(ctx context.Context, archived bool) ([]domain.Project, error) {
	return s.ListProjectsPage(ctx, archived, 0, 0)
}
func (s *Store) ListProjectsPage(ctx context.Context, archived bool, limit, offset int) ([]domain.Project, error) {
	status := domain.ProjectActive
	if archived {
		status = domain.ProjectArchived
	}
	q := `SELECT id,name,normalized_name,description,status,created_at,updated_at,archived_at FROM projects WHERE status=? ORDER BY name COLLATE NOCASE,id`
	args := []any{status}
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Project
	for rows.Next() {
		var p domain.Project
		var ca sql.NullInt64
		var c, u int64
		if err := rows.Scan(&p.ID, &p.Name, &p.NormalizedName, &p.Description, &p.Status, &c, &u, &ca); err != nil {
			return nil, err
		}
		p.CreatedAt = fromMicros(c)
		p.UpdatedAt = fromMicros(u)
		p.ArchivedAt = nullableTime(ca)
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) GetProject(ctx context.Context, id string) (domain.Project, error) {
	var p domain.Project
	var ca sql.NullInt64
	var c, u int64
	err := s.DB.QueryRowContext(ctx, `SELECT id,name,normalized_name,description,status,created_at,updated_at,archived_at FROM projects WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.NormalizedName, &p.Description, &p.Status, &c, &u, &ca)
	if err != nil {
		return p, err
	}
	p.CreatedAt = fromMicros(c)
	p.UpdatedAt = fromMicros(u)
	p.ArchivedAt = nullableTime(ca)
	return p, nil
}
func updateProject(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, p domain.Project) error {
	res, err := exec.ExecContext(ctx, `UPDATE projects SET name=?,normalized_name=?,description=?,status=?,updated_at=?,archived_at=? WHERE id=?`, p.Name, p.NormalizedName, p.Description, p.Status, micros(p.UpdatedAt), nullableMicros(p.ArchivedAt), p.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) UpdateProject(ctx context.Context, p domain.Project) error {
	return updateProject(ctx, s.DB, p)
}
func (s *Store) UpdateProjectIfVersion(ctx context.Context, p domain.Project, expected time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE projects SET name=?,normalized_name=?,description=?,status=?,updated_at=?,archived_at=? WHERE id=? AND updated_at=?`, p.Name, p.NormalizedName, p.Description, p.Status, micros(p.UpdatedAt), nullableMicros(p.ArchivedAt), p.ID, micros(expected))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) UpdateProjectTx(ctx context.Context, tx *sql.Tx, p domain.Project) error {
	return updateProject(ctx, tx, p)
}
func updateProjectIfVersion(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, p domain.Project, expected time.Time) error {
	res, err := exec.ExecContext(ctx, `UPDATE projects SET name=?,normalized_name=?,description=?,status=?,updated_at=?,archived_at=? WHERE id=? AND updated_at=?`, p.Name, p.NormalizedName, p.Description, p.Status, micros(p.UpdatedAt), nullableMicros(p.ArchivedAt), p.ID, micros(expected))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) UpdateProjectTxIfVersion(ctx context.Context, tx *sql.Tx, p domain.Project, expected time.Time) error {
	return updateProjectIfVersion(ctx, tx, p, expected)
}
func (s *Store) ProjectCounts(ctx context.Context, id string) (active, done int, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='active'),0),COALESCE(SUM(status='done'),0) FROM backlog_items WHERE project_id=?`, id).Scan(&active, &done)
	return
}
func createItem(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, i domain.BacklogItem) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO backlog_items(id,project_id,title,notes,quadrant,deadline_date,status,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, i.ID, i.ProjectID, i.Title, i.Notes, i.Quadrant, i.DeadlineDate, i.Status, micros(i.CreatedAt), micros(i.UpdatedAt), nullableMicros(i.CompletedAt))
	return err
}
func (s *Store) CreateItem(ctx context.Context, i domain.BacklogItem) error {
	return createItem(ctx, s.DB, i)
}
func (s *Store) CreateItemTx(ctx context.Context, tx *sql.Tx, i domain.BacklogItem) error {
	return createItem(ctx, tx, i)
}
func (s *Store) ListItems(ctx context.Context, includeDone bool, projectID string) ([]domain.RecommendationItem, error) {
	filter := domain.ItemFilter{ProjectID: projectID, IncludeArchived: false, DeadlineBucket: -1}
	if includeDone {
		filter.Status = ""
	} else {
		filter.Status = domain.ItemActive
	}
	return s.ListItemsPage(ctx, filter, 0, 0)
}
func (s *Store) ListItemsPage(ctx context.Context, filter domain.ItemFilter, limit, offset int) ([]domain.RecommendationItem, error) {
	if filter.DeadlineBucket < -1 || filter.DeadlineBucket > 2 {
		filter.DeadlineBucket = -1
	}
	if filter.Today == "" {
		filter.Today = time.Now().UTC().Format("2006-01-02")
	}
	q := `SELECT b.id,b.project_id,b.title,b.notes,b.quadrant,b.deadline_date,b.status,b.created_at,b.updated_at,b.completed_at,p.name FROM backlog_items b JOIN projects p ON p.id=b.project_id WHERE 1=1`
	args := []any{}
	if !filter.IncludeArchived {
		q += ` AND p.status='active'`
	}
	if filter.Status != "" {
		q += ` AND b.status=?`
		args = append(args, filter.Status)
	}
	if filter.ProjectID != "" {
		q += ` AND b.project_id=?`
		args = append(args, filter.ProjectID)
	}
	if filter.Quadrant != "" {
		q += ` AND b.quadrant=?`
		args = append(args, filter.Quadrant)
	}
	if filter.DeadlineBucket >= 0 {
		q += ` AND b.deadline_date ` + map[int]string{0: "<", 1: "=", 2: ">"}[filter.DeadlineBucket] + ` ?`
		args = append(args, filter.Today)
	}
	q += ` ORDER BY b.deadline_date,b.quadrant,b.created_at,b.id`
	if limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, offset)
	}
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.RecommendationItem
	for rows.Next() {
		var i domain.BacklogItem
		var c, u int64
		var done sql.NullInt64
		var p string
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Title, &i.Notes, &i.Quadrant, &i.DeadlineDate, &i.Status, &c, &u, &done, &p); err != nil {
			return nil, err
		}
		i.CreatedAt = fromMicros(c)
		i.UpdatedAt = fromMicros(u)
		i.CompletedAt = nullableTime(done)
		out = append(out, domain.RecommendationItem{Item: i, ProjectName: p})
	}
	return out, rows.Err()
}
func (s *Store) GetItem(ctx context.Context, id string) (domain.BacklogItem, error) {
	var i domain.BacklogItem
	var c, u int64
	var done sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT id,project_id,title,notes,quadrant,deadline_date,status,created_at,updated_at,completed_at FROM backlog_items WHERE id=?`, id).Scan(&i.ID, &i.ProjectID, &i.Title, &i.Notes, &i.Quadrant, &i.DeadlineDate, &i.Status, &c, &u, &done)
	i.CreatedAt = fromMicros(c)
	i.UpdatedAt = fromMicros(u)
	i.CompletedAt = nullableTime(done)
	return i, err
}
func updateItem(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, i domain.BacklogItem) error {
	res, err := exec.ExecContext(ctx, `UPDATE backlog_items SET project_id=?,title=?,notes=?,quadrant=?,deadline_date=?,status=?,updated_at=?,completed_at=? WHERE id=?`, i.ProjectID, i.Title, i.Notes, i.Quadrant, i.DeadlineDate, i.Status, micros(i.UpdatedAt), nullableMicros(i.CompletedAt), i.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) UpdateItem(ctx context.Context, i domain.BacklogItem) error {
	return updateItem(ctx, s.DB, i)
}
func (s *Store) UpdateItemIfVersion(ctx context.Context, i domain.BacklogItem, expected time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE backlog_items SET project_id=?,title=?,notes=?,quadrant=?,deadline_date=?,status=?,updated_at=?,completed_at=? WHERE id=? AND updated_at=?`, i.ProjectID, i.Title, i.Notes, i.Quadrant, i.DeadlineDate, i.Status, micros(i.UpdatedAt), nullableMicros(i.CompletedAt), i.ID, micros(expected))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) UpdateItemTx(ctx context.Context, tx *sql.Tx, i domain.BacklogItem) error {
	return updateItem(ctx, tx, i)
}
func updateItemIfVersion(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, i domain.BacklogItem, expected time.Time) error {
	res, err := exec.ExecContext(ctx, `UPDATE backlog_items SET project_id=?,title=?,notes=?,quadrant=?,deadline_date=?,status=?,updated_at=?,completed_at=? WHERE id=? AND updated_at=?`, i.ProjectID, i.Title, i.Notes, i.Quadrant, i.DeadlineDate, i.Status, micros(i.UpdatedAt), nullableMicros(i.CompletedAt), i.ID, micros(expected))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) UpdateItemTxIfVersion(ctx context.Context, tx *sql.Tx, i domain.BacklogItem, expected time.Time) error {
	return updateItemIfVersion(ctx, tx, i, expected)
}
func (s *Store) DeleteItem(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM backlog_items WHERE id=?`, id)
	return err
}
func (s *Store) DeleteItemTx(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM backlog_items WHERE id=?`, id)
	return err
}
func (s *Store) SaveState(ctx context.Context, user int64, flow, step, draft, nonce string, version int, expires time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO conversation_states(telegram_user_id,flow,step,draft_json,draft_id,draft_version,schema_version,updated_at,expires_at) VALUES(?,?,?,?,?,?,1,?,?) ON CONFLICT(telegram_user_id) DO UPDATE SET flow=excluded.flow,step=excluded.step,draft_json=excluded.draft_json,draft_id=excluded.draft_id,draft_version=excluded.draft_version,schema_version=1,updated_at=excluded.updated_at,expires_at=excluded.expires_at`, user, flow, step, draft, nonce, version, micros(time.Now()), micros(expires))
	return err
}
func (s *Store) SaveStateVersion(ctx context.Context, user int64, flow, step, draft, nonce string, expected, next int, expires time.Time) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE conversation_states SET flow=?,step=?,draft_json=?,draft_id=?,draft_version=?,schema_version=1,updated_at=?,expires_at=? WHERE telegram_user_id=? AND draft_version=?`, flow, step, draft, nonce, next, micros(time.Now()), micros(expires), user, expected)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (s *Store) ClearState(ctx context.Context, user int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM conversation_states WHERE telegram_user_id=?`, user)
	return err
}
func (s *Store) ClearStateTx(ctx context.Context, tx *sql.Tx, user int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM conversation_states WHERE telegram_user_id=?`, user)
	return err
}
func (s *Store) GetState(ctx context.Context, user int64) (flow, step, draft, nonce string, version int, expires time.Time, err error) {
	var ex int64
	err = s.DB.QueryRowContext(ctx, `SELECT flow,step,draft_json,draft_id,draft_version,expires_at FROM conversation_states WHERE telegram_user_id=?`, user).Scan(&flow, &step, &draft, &nonce, &version, &ex)
	expires = fromMicros(ex)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}

func (s *Store) SaveCallbackToken(ctx context.Context, token string, user int64, payload string, expires time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO callback_tokens(token,telegram_user_id,payload,created_at,expires_at) VALUES(?,?,?,?,?)`, token, user, payload, micros(time.Now()), micros(expires))
	return err
}

func (s *Store) ResolveCallbackToken(ctx context.Context, token string, user int64, now time.Time) (string, error) {
	var payload string
	err := s.DB.QueryRowContext(ctx, `SELECT payload FROM callback_tokens WHERE token=? AND telegram_user_id=? AND expires_at>=?`, token, user, micros(now)).Scan(&payload)
	if err != nil {
		return "", err
	}
	return payload, nil
}
func (s *Store) IsProcessed(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_updates WHERE update_id=?`, id).Scan(&n)
	return n != 0, err
}

func (s *Store) MarkProcessed(ctx context.Context, id int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO processed_updates(update_id,processed_at) VALUES(?,?)`, id, micros(time.Now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
func (s *Store) Receipt(ctx context.Context, nonce string) (string, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT result_json FROM mutation_receipts WHERE nonce=?`, nonce).Scan(&v)
	return v, err
}
func (s *Store) SaveReceipt(ctx context.Context, nonce, action, id, result string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO mutation_receipts(nonce,action,entity_id,result_json,processed_at) VALUES(?,?,?,?,?)`, nonce, action, id, result, micros(time.Now()))
	return err
}

// Mutate atomically applies a mutation, stores its nonce receipt, and marks the update processed.
func (s *Store) Mutate(ctx context.Context, updateID int64, nonce, action, entityID string, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	return s.mutate(ctx, updateID, nonce, action, entityID, 0, false, fn)
}

// MutateAndClearState couples a mutation receipt/update marker with draft cleanup.
func (s *Store) MutateAndClearState(ctx context.Context, updateID int64, nonce, action, entityID string, userID int64, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	return s.mutate(ctx, updateID, nonce, action, entityID, userID, true, fn)
}

func (s *Store) mutate(ctx context.Context, updateID int64, nonce, action, entityID string, userID int64, clearState bool, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	var existing string
	var existingAction, existingEntity string
	err = tx.QueryRowContext(ctx, `SELECT action,entity_id,result_json FROM mutation_receipts WHERE nonce=?`, nonce).Scan(&existingAction, &existingEntity, &existing)
	if err == nil {
		_ = tx.Rollback()
		if existingAction != action || existingEntity != entityID {
			return "", false, ErrReceiptConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return "", false, err
	}
	var processed int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_updates WHERE update_id=?`, updateID).Scan(&processed); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	if processed != 0 {
		_ = tx.Rollback()
		return "", true, nil
	}
	result, err := fn(tx)
	if err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	now := micros(time.Now())
	if _, err = tx.ExecContext(ctx, `INSERT INTO mutation_receipts(nonce,action,entity_id,result_json,processed_at) VALUES(?,?,?,?,?)`, nonce, action, entityID, result, now); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO processed_updates(update_id,processed_at) VALUES(?,?)`, updateID, now); err != nil {
		_ = tx.Rollback()
		return "", false, err
	}
	if clearState {
		if err = s.ClearStateTx(ctx, tx, userID); err != nil {
			_ = tx.Rollback()
			return "", false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return "", false, err
	}
	return result, false, nil
}

func (s *Store) GetNotificationRun(ctx context.Context, date string) (NotificationRun, error) {
	var r NotificationRun
	var scheduled, updated int64
	err := s.DB.QueryRowContext(ctx, `SELECT local_date,status,scheduled_for,attempt_count,updated_at FROM notification_runs WHERE local_date=?`, date).Scan(&r.LocalDate, &r.Status, &scheduled, &r.AttemptCount, &updated)
	r.ScheduledFor, r.UpdatedAt = fromMicros(scheduled), fromMicros(updated)
	return r, err
}
func (s *Store) EnsureNotificationRun(ctx context.Context, date string, scheduled, now time.Time) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO notification_runs(local_date,scheduled_for,status,attempt_count,created_at,updated_at) VALUES(?,?, 'pending',0,?,?) ON CONFLICT(local_date) DO NOTHING`, date, micros(scheduled), micros(now), micros(now))
	return err
}
func (s *Store) SnapshotNotification(ctx context.Context, date string, items []domain.RecommendationItem, parts []NotificationPart, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM notification_runs WHERE local_date=?`, date).Scan(&status); err != nil {
		return err
	}
	if status != "pending" {
		return nil
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_run_items(local_date,ordinal,backlog_item_id,project_name,title,quadrant,deadline_date) VALUES(?,?,?,?,?,?,?)`, date, item.Ordinal, item.Item.ID, item.ProjectName, item.Item.Title, item.Item.Quadrant, item.Item.DeadlineDate); err != nil {
			return err
		}
	}
	for _, part := range parts {
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_parts(local_date,part_index,payload_json,keyboard_json,status) VALUES(?,?,?,?,'pending')`, date, part.Index, part.Payload, part.Keyboard); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE notification_runs SET status='sending',updated_at=? WHERE local_date=? AND status='pending'`, micros(now), date)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) NotificationSnapshotExists(ctx context.Context, date string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_runs WHERE local_date=? AND EXISTS (SELECT 1 FROM notification_parts WHERE local_date=?)`, date, date).Scan(&n)
	return n > 0, err
}

func (s *Store) NotificationItems(ctx context.Context, date string) ([]NotificationItem, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ordinal,backlog_item_id,project_name,title,quadrant,deadline_date FROM notification_run_items WHERE local_date=? ORDER BY ordinal`, date)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []NotificationItem
	for rows.Next() {
		var item NotificationItem
		if err := rows.Scan(&item.Ordinal, &item.BacklogItemID, &item.ProjectName, &item.Title, &item.Quadrant, &item.DeadlineDate); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) PendingNotificationParts(ctx context.Context, date string) ([]NotificationPart, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT part_index,payload_json,keyboard_json FROM notification_parts WHERE local_date=? AND status='pending' ORDER BY part_index`, date)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []NotificationPart
	for rows.Next() {
		var p NotificationPart
		if err := rows.Scan(&p.Index, &p.Payload, &p.Keyboard); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) MarkNotificationPartSent(ctx context.Context, date string, index, messageID int, now time.Time) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE notification_parts SET status='sent',telegram_message_id=?,sent_at=? WHERE local_date=? AND part_index=? AND status='pending'`, messageID, micros(now), date, index)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("notification part %d is not pending", index)
	}
	return nil
}
func (s *Store) MarkNotificationSent(ctx context.Context, date string, now time.Time) error {
	var pending int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_parts WHERE local_date=? AND status='pending'`, date).Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return fmt.Errorf("notification still has pending parts")
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE notification_runs SET status='sent',sent_at=?,updated_at=? WHERE local_date=? AND status IN ('pending','sending')`, micros(now), micros(now), date)
	return err
}
func (s *Store) FailNotificationRun(ctx context.Context, date, reason string, now time.Time) error {
	now = now.UTC()
	_, err := s.DB.ExecContext(ctx, `UPDATE notification_runs SET status='failed',last_error=?,attempt_count=attempt_count+1,retry_at=CASE WHEN attempt_count+1 < 4 THEN ? ELSE NULL END,updated_at=? WHERE local_date=? AND status IN ('sending','pending')`, reason, micros(now.Add(notificationRetryWindow)), micros(now), date)
	return err
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
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
		return err
	}
	var result string
	if err = conn.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
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
