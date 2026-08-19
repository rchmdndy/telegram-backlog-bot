package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
)

// Services contain the business rules shared by Telegram and scheduled work.
type ProjectService struct{ DB *store.Store }
type BacklogService struct{ DB *store.Store }

func NewProjectService(db *store.Store) *ProjectService { return &ProjectService{DB: db} }
func NewBacklogService(db *store.Store) *BacklogService { return &BacklogService{DB: db} }

func (s *ProjectService) Create(ctx context.Context, name, description string, now time.Time) (domain.Project, error) {
	p := domain.Project{ID: domain.NewID(), Name: domain.NormalizeText(name), Description: domain.NormalizeText(description), Status: domain.ProjectActive, CreatedAt: now, UpdatedAt: now}
	p.NormalizedName = domain.NormalizeProjectName(p.Name)
	if err := domain.ValidateProject(p); err != nil {
		return p, err
	}
	if err := s.DB.CreateProject(ctx, p); err != nil {
		return p, mapStoreError(err)
	}
	return p, nil
}

func (s *ProjectService) Update(ctx context.Context, p domain.Project, name, description string, now time.Time) (domain.Project, error) {
	return s.UpdateIfVersion(ctx, p, name, description, time.Time{}, now)
}
func (s *ProjectService) UpdateIfVersion(ctx context.Context, p domain.Project, name, description string, expected, now time.Time) (domain.Project, error) {
	if p.Status != domain.ProjectActive && p.Status != domain.ProjectArchived {
		return p, domain.ErrInvalid
	}
	p.Name, p.Description = domain.NormalizeText(name), domain.NormalizeText(description)
	p.NormalizedName = domain.NormalizeProjectName(p.Name)
	if err := domain.ValidateProject(p); err != nil {
		return p, err
	}
	p.UpdatedAt = now
	var err error
	if expected.IsZero() {
		err = s.DB.UpdateProject(ctx, p)
	} else {
		err = s.DB.UpdateProjectIfVersion(ctx, p, expected)
	}
	if err != nil {
		return p, mapStoreError(err)
	}
	return p, nil
}

func (s *ProjectService) Archive(ctx context.Context, id string, now time.Time) error {
	p, err := s.DB.GetProject(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	if p.Status == domain.ProjectArchived {
		return nil
	}
	p.Status, p.ArchivedAt, p.UpdatedAt = domain.ProjectArchived, ptr(now), now
	return mapStoreError(s.DB.UpdateProject(ctx, p))
}

func (s *ProjectService) UpdateWithMutationAndClearState(ctx context.Context, updateID int64, nonce string, p domain.Project, name, description string, expected, now time.Time, userID int64) (domain.Project, bool, error) {
	p.Name, p.Description = domain.NormalizeText(name), domain.NormalizeText(description)
	p.NormalizedName = domain.NormalizeProjectName(p.Name)
	if err := domain.ValidateProject(p); err != nil {
		return p, false, err
	}
	p.UpdatedAt = now
	result, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "project.update", p.ID, userID, func(tx *sql.Tx) (string, error) {
		if !expected.IsZero() {
			if err := s.DB.UpdateProjectTxIfVersion(ctx, tx, p, expected); err != nil {
				return "", mapStoreError(err)
			}
		} else if err := s.DB.UpdateProjectTx(ctx, tx, p); err != nil {
			return "", mapStoreError(err)
		}
		b, err := json.Marshal(p)
		return string(b), err
	})
	if err != nil {
		return p, replay, err
	}
	if replay && result != "" {
		_ = json.Unmarshal([]byte(result), &p)
	}
	return p, replay, nil
}

func (s *ProjectService) ArchiveWithMutation(ctx context.Context, updateID int64, nonce, id string, now time.Time) (bool, error) {
	return s.ArchiveWithMutationVersion(ctx, updateID, nonce, id, time.Time{}, now)
}
func (s *ProjectService) ArchiveWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "project.archive", id, func(tx *sql.Tx) (string, error) {
		p, err := projectTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		if p.Status == domain.ProjectArchived {
			return `{"ok":true}`, nil
		}
		p.Status, p.ArchivedAt, p.UpdatedAt = domain.ProjectArchived, ptr(now), now
		var updateErr error
		if expected.IsZero() {
			updateErr = s.DB.UpdateProjectTx(ctx, tx, p)
		} else {
			updateErr = s.DB.UpdateProjectTxIfVersion(ctx, tx, p, expected)
		}
		if updateErr != nil {
			return "", mapStoreError(updateErr)
		}
		return `{"ok":true}`, nil
	})
	return replay, err
}

func (s *ProjectService) Restore(ctx context.Context, id string, now time.Time) error {
	p, err := s.DB.GetProject(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	if p.Status == domain.ProjectActive {
		return nil
	}
	conflict, err := s.DB.ActiveProjectNameConflict(ctx, p.NormalizedName, p.ID)
	if err != nil {
		return mapStoreError(err)
	}
	if conflict {
		return domain.ErrConflict
	}
	p.Status, p.ArchivedAt, p.UpdatedAt = domain.ProjectActive, nil, now
	return mapStoreError(s.DB.UpdateProject(ctx, p))
}

// CreateWithMutation commits a project and its mutation receipt/update marker atomically.
func (s *ProjectService) CreateWithMutation(ctx context.Context, updateID int64, nonce, name, description string, now time.Time) (domain.Project, bool, error) {
	return s.createWithMutation(ctx, updateID, nonce, name, description, now, false, 0)
}

func (s *ProjectService) CreateWithMutationAndClearState(ctx context.Context, updateID int64, nonce, name, description string, now time.Time, userID int64) (domain.Project, bool, error) {
	return s.createWithMutation(ctx, updateID, nonce, name, description, now, true, userID)
}

func (s *ProjectService) createWithMutation(ctx context.Context, updateID int64, nonce, name, description string, now time.Time, clearState bool, userID int64) (domain.Project, bool, error) {
	p := domain.Project{ID: domain.NewID(), Name: domain.NormalizeText(name), Description: domain.NormalizeText(description), Status: domain.ProjectActive, CreatedAt: now, UpdatedAt: now}
	p.NormalizedName = domain.NormalizeProjectName(p.Name)
	if err := domain.ValidateProject(p); err != nil {
		return p, false, err
	}
	mutate := s.DB.Mutate
	if clearState {
		mutate = func(ctx context.Context, updateID int64, nonce, action, entityID string, fn func(*sql.Tx) (string, error)) (string, bool, error) {
			return s.DB.MutateAndClearState(ctx, updateID, nonce, action, entityID, userID, fn)
		}
	}
	result, replay, err := mutate(ctx, updateID, nonce, "project.create", p.ID, func(tx *sql.Tx) (string, error) {
		if err := s.DB.CreateProjectTx(ctx, tx, p); err != nil {
			return "", mapStoreError(err)
		}
		b, err := json.Marshal(p)
		return string(b), err
	})
	if err != nil {
		return p, replay, err
	}
	if replay && result != "" {
		_ = json.Unmarshal([]byte(result), &p)
	}
	return p, replay, nil
}

// RestoreWithMutation validates the current project and performs restore, receipt, and update marking in one transaction.
func (s *ProjectService) RestoreWithMutation(ctx context.Context, updateID int64, nonce, id string, now time.Time) (bool, error) {
	return s.RestoreWithMutationVersion(ctx, updateID, nonce, id, time.Time{}, now)
}

func (s *ProjectService) RestoreWithMutationAndClearState(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time, userID int64) (bool, error) {
	_, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "project.restore", id, userID, func(tx *sql.Tx) (string, error) {
		return s.restoreTx(ctx, tx, id, expected, now)
	})
	return replay, err
}

func (s *ProjectService) RestoreWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "project.restore", id, func(tx *sql.Tx) (string, error) {
		return s.restoreTx(ctx, tx, id, expected, now)
	})
	return replay, err
}

func (s *ProjectService) restoreTx(ctx context.Context, tx *sql.Tx, id string, expected, now time.Time) (string, error) {
	p, err := projectTx(ctx, tx, id)
	if err != nil {
		return "", mapStoreError(err)
	}
	if p.Status == domain.ProjectActive {
		return `{"ok":true}`, nil
	}
	if !expected.IsZero() && !p.UpdatedAt.Equal(expected) {
		return "", domain.ErrConflict
	}
	conflict, err := s.DB.ActiveProjectNameConflictTx(ctx, tx, p.NormalizedName, p.ID)
	if err != nil {
		return "", err
	}
	if conflict {
		return "", domain.ErrConflict
	}
	p.Status, p.ArchivedAt, p.UpdatedAt = domain.ProjectActive, nil, now
	var updateErr error
	if expected.IsZero() {
		updateErr = s.DB.UpdateProjectTx(ctx, tx, p)
	} else {
		updateErr = s.DB.UpdateProjectTxIfVersion(ctx, tx, p, expected)
	}
	if updateErr != nil {
		return "", mapStoreError(updateErr)
	}
	return `{"ok":true}`, nil
}

func projectTx(ctx context.Context, tx *sql.Tx, id string) (domain.Project, error) {
	var p domain.Project
	var c, u int64
	var a sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,name,normalized_name,description,status,created_at,updated_at,archived_at FROM projects WHERE id=?`, id).Scan(&p.ID, &p.Name, &p.NormalizedName, &p.Description, &p.Status, &c, &u, &a)
	if err != nil {
		return p, err
	}
	p.CreatedAt = time.UnixMicro(c).UTC()
	p.UpdatedAt = time.UnixMicro(u).UTC()
	p.ArchivedAt = nil
	if a.Valid {
		t := time.UnixMicro(a.Int64).UTC()
		p.ArchivedAt = &t
	}
	return p, nil
}

func (s *ProjectService) List(ctx context.Context, archived bool, limit, offset int) ([]domain.Project, error) {
	return s.DB.ListProjectsPage(ctx, archived, limit, offset)
}

func (s *BacklogService) Create(ctx context.Context, projectID, title, notes string, q domain.Quadrant, deadline string, now time.Time) (domain.BacklogItem, error) {
	p, err := s.DB.GetProject(ctx, projectID)
	if err != nil {
		return domain.BacklogItem{}, mapStoreError(err)
	}
	if p.Status != domain.ProjectActive {
		return domain.BacklogItem{}, domain.ErrArchived
	}
	i := domain.BacklogItem{ID: domain.NewID(), ProjectID: projectID, Title: domain.NormalizeText(title), Notes: domain.NormalizeText(notes), Quadrant: q, DeadlineDate: deadline, Status: domain.ItemActive, CreatedAt: now, UpdatedAt: now}
	if err := domain.ValidateItem(i, now, false); err != nil {
		return i, err
	}
	if err := s.DB.CreateItem(ctx, i); err != nil {
		return i, mapStoreError(err)
	}
	return i, nil
}

func (s *BacklogService) CreateWithMutation(ctx context.Context, updateID int64, nonce, projectID, title, notes string, q domain.Quadrant, deadline string, now time.Time) (domain.BacklogItem, bool, error) {
	return s.createWithMutation(ctx, updateID, nonce, projectID, title, notes, q, deadline, now, false, 0)
}

func (s *BacklogService) CreateWithMutationAndClearState(ctx context.Context, updateID int64, nonce, projectID, title, notes string, q domain.Quadrant, deadline string, now time.Time, userID int64) (domain.BacklogItem, bool, error) {
	return s.createWithMutation(ctx, updateID, nonce, projectID, title, notes, q, deadline, now, true, userID)
}

func (s *BacklogService) createWithMutation(ctx context.Context, updateID int64, nonce, projectID, title, notes string, q domain.Quadrant, deadline string, now time.Time, clearState bool, userID int64) (domain.BacklogItem, bool, error) {
	p, err := s.DB.GetProject(ctx, projectID)
	if err != nil {
		return domain.BacklogItem{}, false, mapStoreError(err)
	}
	if p.Status != domain.ProjectActive {
		return domain.BacklogItem{}, false, domain.ErrArchived
	}
	i := domain.BacklogItem{ID: domain.NewID(), ProjectID: projectID, Title: domain.NormalizeText(title), Notes: domain.NormalizeText(notes), Quadrant: q, DeadlineDate: deadline, Status: domain.ItemActive, CreatedAt: now, UpdatedAt: now}
	if err := domain.ValidateItem(i, now, false); err != nil {
		return i, false, err
	}
	mutate := s.DB.Mutate
	if clearState {
		mutate = func(ctx context.Context, updateID int64, nonce, action, entityID string, fn func(*sql.Tx) (string, error)) (string, bool, error) {
			return s.DB.MutateAndClearState(ctx, updateID, nonce, action, entityID, userID, fn)
		}
	}
	result, replay, err := mutate(ctx, updateID, nonce, "backlog.create", i.ID, func(tx *sql.Tx) (string, error) {
		if err := s.DB.CreateItemTx(ctx, tx, i); err != nil {
			return "", mapStoreError(err)
		}
		b, err := json.Marshal(i)
		return string(b), err
	})
	if err != nil {
		return i, replay, err
	}
	if replay && result != "" {
		_ = json.Unmarshal([]byte(result), &i)
	}
	return i, replay, nil
}

func (s *BacklogService) Update(ctx context.Context, i domain.BacklogItem, p domain.Project, now time.Time) error {
	return s.UpdateIfVersion(ctx, i, p, time.Time{}, now)
}
func (s *BacklogService) UpdateIfVersion(ctx context.Context, i domain.BacklogItem, p domain.Project, expected, now time.Time) error {
	if p.Status != domain.ProjectActive {
		return domain.ErrArchived
	}
	if i.Status == domain.ItemDone {
		return domain.ErrDone
	}
	i.Title, i.Notes = domain.NormalizeText(i.Title), domain.NormalizeText(i.Notes)
	if err := domain.ValidateItem(i, now, true); err != nil {
		return err
	}
	i.UpdatedAt = now
	if expected.IsZero() {
		return mapStoreError(s.DB.UpdateItem(ctx, i))
	}
	return mapStoreError(s.DB.UpdateItemIfVersion(ctx, i, expected))
}

func (s *BacklogService) Complete(ctx context.Context, id string, now time.Time) error {
	i, err := s.DB.GetItem(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	p, err := s.DB.GetProject(ctx, i.ProjectID)
	if err != nil {
		return mapStoreError(err)
	}
	if p.Status != domain.ProjectActive && i.Status == domain.ItemDone {
		return nil
	}
	if err := domain.Complete(&i, now); err != nil {
		return err
	}
	return mapStoreError(s.DB.UpdateItem(ctx, i))
}

func (s *BacklogService) UpdateWithMutationAndClearState(ctx context.Context, updateID int64, nonce string, i domain.BacklogItem, p domain.Project, expected, now time.Time, userID int64) (bool, error) {
	if p.Status != domain.ProjectActive {
		return false, domain.ErrArchived
	}
	if i.Status == domain.ItemDone {
		return false, domain.ErrDone
	}
	i.Title, i.Notes = domain.NormalizeText(i.Title), domain.NormalizeText(i.Notes)
	if err := domain.ValidateItem(i, now, true); err != nil {
		return false, err
	}
	i.UpdatedAt = now
	_, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "backlog.update", i.ID, userID, func(tx *sql.Tx) (string, error) {
		current, err := itemTx(ctx, tx, i.ID)
		if err != nil {
			return "", mapStoreError(err)
		}
		source, err := projectTx(ctx, tx, current.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		destination, err := projectTx(ctx, tx, i.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		if source.Status != domain.ProjectActive || destination.Status != domain.ProjectActive {
			return "", domain.ErrArchived
		}
		if !expected.IsZero() && !current.UpdatedAt.Equal(expected) {
			return "", domain.ErrConflict
		}
		if err := s.DB.UpdateItemTx(ctx, tx, i); err != nil {
			return "", mapStoreError(err)
		}
		return `{"ok":true}`, nil
	})
	return replay, err
}

func (s *BacklogService) CompleteWithMutation(ctx context.Context, updateID int64, nonce, id string, now time.Time) (bool, error) {
	return s.CompleteWithMutationVersion(ctx, updateID, nonce, id, time.Time{}, now)
}
func (s *BacklogService) CompleteWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "backlog.complete", id, func(tx *sql.Tx) (string, error) {
		i, err := itemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := projectTx(ctx, tx, i.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		if p.Status != domain.ProjectActive && i.Status == domain.ItemDone {
			return `{"ok":true}`, nil
		}
		if err := domain.Complete(&i, now); err != nil {
			return "", err
		}
		var updateErr error
		if expected.IsZero() {
			updateErr = s.DB.UpdateItemTx(ctx, tx, i)
		} else {
			updateErr = s.DB.UpdateItemTxIfVersion(ctx, tx, i, expected)
		}
		if updateErr != nil {
			return "", mapStoreError(updateErr)
		}
		return `{"ok":true}`, nil
	})
	return replay, err
}

func (s *BacklogService) Reopen(ctx context.Context, id string, now time.Time) error {
	i, err := s.DB.GetItem(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	p, err := s.DB.GetProject(ctx, i.ProjectID)
	if err != nil {
		return mapStoreError(err)
	}
	if err := domain.Reopen(&i, p, now); err != nil {
		return err
	}
	return mapStoreError(s.DB.UpdateItem(ctx, i))
}

func (s *BacklogService) ReopenWithMutation(ctx context.Context, updateID int64, nonce, id string, now time.Time) (bool, error) {
	return s.ReopenWithMutationVersion(ctx, updateID, nonce, id, time.Time{}, now)
}
func (s *BacklogService) ReopenWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "backlog.reopen", id, func(tx *sql.Tx) (string, error) {
		i, err := itemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := projectTx(ctx, tx, i.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		if err := domain.Reopen(&i, p, now); err != nil {
			return "", err
		}
		var updateErr error
		if expected.IsZero() {
			updateErr = s.DB.UpdateItemTx(ctx, tx, i)
		} else {
			updateErr = s.DB.UpdateItemTxIfVersion(ctx, tx, i, expected)
		}
		if updateErr != nil {
			return "", mapStoreError(updateErr)
		}
		return `{"ok":true}`, nil
	})
	return replay, err
}

func itemTx(ctx context.Context, tx *sql.Tx, id string) (domain.BacklogItem, error) {
	var i domain.BacklogItem
	var c, u int64
	var done sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,title,notes,quadrant,deadline_date,status,created_at,updated_at,completed_at FROM backlog_items WHERE id=?`, id).Scan(&i.ID, &i.ProjectID, &i.Title, &i.Notes, &i.Quadrant, &i.DeadlineDate, &i.Status, &c, &u, &done)
	if err != nil {
		return i, err
	}
	i.CreatedAt = time.UnixMicro(c).UTC()
	i.UpdatedAt = time.UnixMicro(u).UTC()
	if done.Valid {
		t := time.UnixMicro(done.Int64).UTC()
		i.CompletedAt = &t
	}
	return i, nil
}

func (s *BacklogService) Delete(ctx context.Context, id string) error {
	i, err := s.DB.GetItem(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	p, err := s.DB.GetProject(ctx, i.ProjectID)
	if err != nil {
		return mapStoreError(err)
	}
	if p.Status != domain.ProjectActive {
		return domain.ErrArchived
	}
	return mapStoreError(s.DB.DeleteItem(ctx, id))
}

func (s *BacklogService) DeleteWithMutationAndClearState(ctx context.Context, updateID int64, nonce, id string, expected time.Time, userID int64) (bool, error) {
	_, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "backlog.delete", id, userID, func(tx *sql.Tx) (string, error) {
		i, err := itemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := projectTx(ctx, tx, i.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		if p.Status != domain.ProjectActive {
			return "", domain.ErrArchived
		}
		if !expected.IsZero() && !i.UpdatedAt.Equal(expected) {
			return "", domain.ErrConflict
		}
		if err := s.DB.DeleteItemTx(ctx, tx, id); err != nil {
			return "", mapStoreError(err)
		}
		return `{"ok":true}`, nil
	})
	return replay, err
}

func (s *BacklogService) DeleteWithMutation(ctx context.Context, updateID int64, nonce, id string) (bool, error) {
	return s.DeleteWithMutationVersion(ctx, updateID, nonce, id, time.Time{})
}
func (s *BacklogService) DeleteWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "backlog.delete", id, func(tx *sql.Tx) (string, error) {
		i, err := itemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := projectTx(ctx, tx, i.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		if p.Status != domain.ProjectActive {
			return "", domain.ErrArchived
		}
		if !expected.IsZero() && !i.UpdatedAt.Equal(expected) {
			return "", domain.ErrConflict
		}
		if err := s.DB.DeleteItemTx(ctx, tx, id); err != nil {
			return "", mapStoreError(err)
		}
		return `{"ok":true}`, nil
	})
	return replay, err
}

func (s *BacklogService) List(ctx context.Context, filter domain.ItemFilter, limit, offset int) ([]domain.RecommendationItem, error) {
	return s.DB.ListItemsPage(ctx, filter, limit, offset)
}

// Mutate executes a mutation, receipt insert, and update marker in one SQLite transaction.
func Mutate(ctx context.Context, db *store.Store, updateID int64, nonce, action, entityID string, fn func(*sql.Tx) (any, error)) (any, bool, error) {
	result, replay, err := db.Mutate(ctx, updateID, nonce, action, entityID, func(tx *sql.Tx) (string, error) {
		v, err := fn(tx)
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(v)
		return string(b), err
	})
	if err != nil {
		return nil, replay, err
	}
	if result == "" {
		return nil, replay, nil
	}
	var out any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		return nil, replay, err
	}
	return out, replay, nil
}

func ptr(t time.Time) *time.Time { return &t }
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if errors.Is(err, store.ErrDuplicate) || strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return domain.ErrConflict
	}
	return fmt.Errorf("store: %w", err)
}
