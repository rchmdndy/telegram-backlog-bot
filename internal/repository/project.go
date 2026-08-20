package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
)

func createProject(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, p domain.Project) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO projects(id,name,normalized_name,description,status,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.NormalizedName, p.Description, p.Status, micros(p.CreatedAt), micros(p.UpdatedAt), nullableMicros(p.ArchivedAt))
	return err
}
func (r *ProjectRepository) CreateProject(ctx context.Context, p domain.Project) error {
	return createProject(ctx, r.db, p)
}
func (r *ProjectRepository) CreateProjectTx(ctx context.Context, tx *sql.Tx, p domain.Project) error {
	return createProject(ctx, tx, p)
}
func (r *ProjectRepository) ActiveProjectNameConflict(ctx context.Context, normalized, excludeID string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE status='active' AND normalized_name=? AND id<>?`, normalized, excludeID).Scan(&n)
	return n != 0, err
}
func (r *ProjectRepository) ActiveProjectNameConflictTx(ctx context.Context, tx *sql.Tx, normalized, excludeID string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE status='active' AND normalized_name=? AND id<>?`, normalized, excludeID).Scan(&n)
	return n != 0, err
}
func (r *ProjectRepository) ListProjects(ctx context.Context, archived bool) ([]domain.Project, error) {
	return r.ListProjectsPage(ctx, archived, 0, 0)
}
func (r *ProjectRepository) ListProjectsPage(ctx context.Context, archived bool, limit, offset int) ([]domain.Project, error) {
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
	rows, err := r.db.QueryContext(ctx, q, args...)
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
func (r *ProjectRepository) GetProject(ctx context.Context, id string) (domain.Project, error) {
	return scanProject(ctx, r.db, id)
}
func (r *ProjectRepository) GetProjectTx(ctx context.Context, tx *sql.Tx, id string) (domain.Project, error) {
	return scanProject(ctx, tx, id)
}
func scanProject(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.Project, error) {
	var p domain.Project
	var ca sql.NullInt64
	var c, u int64
	err := q.QueryRowContext(ctx, `SELECT id,name,normalized_name,description,status,created_at,updated_at,archived_at FROM projects WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.NormalizedName, &p.Description, &p.Status, &c, &u, &ca)
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
func (r *ProjectRepository) UpdateProject(ctx context.Context, p domain.Project) error {
	return updateProject(ctx, r.db, p)
}
func (r *ProjectRepository) UpdateProjectIfVersion(ctx context.Context, p domain.Project, expected time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE projects SET name=?,normalized_name=?,description=?,status=?,updated_at=?,archived_at=? WHERE id=? AND updated_at=?`, p.Name, p.NormalizedName, p.Description, p.Status, micros(p.UpdatedAt), nullableMicros(p.ArchivedAt), p.ID, micros(expected))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (r *ProjectRepository) UpdateProjectTx(ctx context.Context, tx *sql.Tx, p domain.Project) error {
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
func (r *ProjectRepository) UpdateProjectTxIfVersion(ctx context.Context, tx *sql.Tx, p domain.Project, expected time.Time) error {
	return updateProjectIfVersion(ctx, tx, p, expected)
}
