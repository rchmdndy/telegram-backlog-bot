package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
)

func (r *BacklogRepository) ProjectCounts(ctx context.Context, id string) (active, done int, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='active'),0),COALESCE(SUM(status='done'),0) FROM backlog_items WHERE project_id=?`, id).Scan(&active, &done)
	return
}
func createItem(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, i domain.BacklogItem) error {
	_, err := exec.ExecContext(ctx, `INSERT INTO backlog_items(id,project_id,title,notes,quadrant,deadline_date,status,created_at,updated_at,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, i.ID, i.ProjectID, i.Title, i.Notes, i.Quadrant, i.DeadlineDate, i.Status, micros(i.CreatedAt), micros(i.UpdatedAt), nullableMicros(i.CompletedAt))
	return err
}
func (r *BacklogRepository) CreateItem(ctx context.Context, i domain.BacklogItem) error {
	return createItem(ctx, r.db, i)
}
func (r *BacklogRepository) CreateItemTx(ctx context.Context, tx *sql.Tx, i domain.BacklogItem) error {
	return createItem(ctx, tx, i)
}
func (r *BacklogRepository) ListItems(ctx context.Context, includeDone bool, projectID string) ([]domain.RecommendationItem, error) {
	filter := domain.ItemFilter{ProjectID: projectID, IncludeArchived: false, DeadlineBucket: -1}
	if includeDone {
		filter.Status = ""
	} else {
		filter.Status = domain.ItemActive
	}
	return r.ListItemsPage(ctx, filter, 0, 0)
}
func (r *BacklogRepository) ListItemsPage(ctx context.Context, filter domain.ItemFilter, limit, offset int) ([]domain.RecommendationItem, error) {
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
	rows, err := r.db.QueryContext(ctx, q, args...)
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
func (r *BacklogRepository) GetItem(ctx context.Context, id string) (domain.BacklogItem, error) {
	return scanItem(ctx, r.db, id)
}
func (r *BacklogRepository) GetItemTx(ctx context.Context, tx *sql.Tx, id string) (domain.BacklogItem, error) {
	return scanItem(ctx, tx, id)
}
func scanItem(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (domain.BacklogItem, error) {
	var i domain.BacklogItem
	var c, u int64
	var done sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT id,project_id,title,notes,quadrant,deadline_date,status,created_at,updated_at,completed_at FROM backlog_items WHERE id=?`, id).Scan(&i.ID, &i.ProjectID, &i.Title, &i.Notes, &i.Quadrant, &i.DeadlineDate, &i.Status, &c, &u, &done)
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
func (r *BacklogRepository) UpdateItem(ctx context.Context, i domain.BacklogItem) error {
	return updateItem(ctx, r.db, i)
}
func (r *BacklogRepository) UpdateItemIfVersion(ctx context.Context, i domain.BacklogItem, expected time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE backlog_items SET project_id=?,title=?,notes=?,quadrant=?,deadline_date=?,status=?,updated_at=?,completed_at=? WHERE id=? AND updated_at=?`, i.ProjectID, i.Title, i.Notes, i.Quadrant, i.DeadlineDate, i.Status, micros(i.UpdatedAt), nullableMicros(i.CompletedAt), i.ID, micros(expected))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}
func (r *BacklogRepository) UpdateItemTx(ctx context.Context, tx *sql.Tx, i domain.BacklogItem) error {
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
func (r *BacklogRepository) UpdateItemTxIfVersion(ctx context.Context, tx *sql.Tx, i domain.BacklogItem, expected time.Time) error {
	return updateItemIfVersion(ctx, tx, i, expected)
}
func (r *BacklogRepository) DeleteItem(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM backlog_items WHERE id=?`, id)
	return err
}
func (r *BacklogRepository) DeleteItemTx(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM backlog_items WHERE id=?`, id)
	return err
}
