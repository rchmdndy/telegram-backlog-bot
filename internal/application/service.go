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
	"github.com/rchmdndy/telegram-backlog-bot/internal/recommendation"
	"github.com/rchmdndy/telegram-backlog-bot/internal/repository"
)

// Services contain the business rules shared by Telegram and scheduled work.
type ProjectService struct{ DB *repository.Repositories }
type BacklogService struct{ DB *repository.Repositories }

func NewProjectService(db *repository.Repositories) *ProjectService { return &ProjectService{DB: db} }
func NewBacklogService(db *repository.Repositories) *BacklogService { return &BacklogService{DB: db} }

func (s *ProjectService) Get(ctx context.Context, id string) (domain.Project, error) {
	return s.DB.GetProject(ctx, id)
}

func (s *ProjectService) List(ctx context.Context, archived bool) ([]domain.Project, error) {
	return s.DB.ListProjects(ctx, archived)
}

func (s *ProjectService) ListPage(ctx context.Context, archived bool, limit, offset int) ([]domain.Project, error) {
	return s.DB.ListProjectsPage(ctx, archived, limit, offset)
}

func (s *ProjectService) Counts(ctx context.Context, id string) (active, done int, err error) {
	return s.DB.ProjectCounts(ctx, id)
}

func (s *BacklogService) Get(ctx context.Context, id string) (domain.BacklogItem, error) {
	return s.DB.GetItem(ctx, id)
}

func (s *BacklogService) List(ctx context.Context, includeDone bool, projectID string) ([]domain.RecommendationItem, error) {
	return s.DB.ListItems(ctx, includeDone, projectID)
}

func (s *BacklogService) ListPage(ctx context.Context, filter domain.ItemFilter, limit, offset int) ([]domain.RecommendationItem, error) {
	return s.DB.ListItemsPage(ctx, filter, limit, offset)
}

func (s *BacklogService) Recommend(ctx context.Context, now time.Time, limit int) ([]domain.RecommendationItem, error) {
	items, err := s.List(ctx, false, "")
	if err != nil {
		return nil, err
	}
	return recommendation.Select(items, now, limit), nil
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

func (s *ProjectService) ArchiveWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "project.archive", id, func(tx *sql.Tx) (string, error) {
		p, err := s.DB.GetProjectTx(ctx, tx, id)
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

func (s *ProjectService) CreateWithMutationAndClearState(ctx context.Context, updateID int64, nonce, name, description string, now time.Time, userID int64) (domain.Project, bool, error) {
	p := domain.Project{ID: domain.NewID(), Name: domain.NormalizeText(name), Description: domain.NormalizeText(description), Status: domain.ProjectActive, CreatedAt: now, UpdatedAt: now}
	p.NormalizedName = domain.NormalizeProjectName(p.Name)
	if err := domain.ValidateProject(p); err != nil {
		return p, false, err
	}
	result, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "project.create", p.ID, userID, func(tx *sql.Tx) (string, error) {
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

func (s *ProjectService) RestoreWithMutationAndClearState(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time, userID int64) (bool, error) {
	_, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "project.restore", id, userID, func(tx *sql.Tx) (string, error) {
		return s.restoreTx(ctx, tx, id, expected, now)
	})
	return replay, err
}

func (s *ProjectService) restoreTx(ctx context.Context, tx *sql.Tx, id string, expected, now time.Time) (string, error) {
	p, err := s.DB.GetProjectTx(ctx, tx, id)
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

func (s *BacklogService) CreateWithMutationAndClearState(ctx context.Context, updateID int64, nonce, projectID, title, notes string, q domain.Quadrant, deadline string, now time.Time, userID int64) (domain.BacklogItem, bool, error) {
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
	result, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "backlog.create", i.ID, userID, func(tx *sql.Tx) (string, error) {
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
		current, err := s.DB.GetItemTx(ctx, tx, i.ID)
		if err != nil {
			return "", mapStoreError(err)
		}
		source, err := s.DB.GetProjectTx(ctx, tx, current.ProjectID)
		if err != nil {
			return "", mapStoreError(err)
		}
		destination, err := s.DB.GetProjectTx(ctx, tx, i.ProjectID)
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

func (s *BacklogService) CompleteWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "backlog.complete", id, func(tx *sql.Tx) (string, error) {
		i, err := s.DB.GetItemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := s.DB.GetProjectTx(ctx, tx, i.ProjectID)
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

func (s *BacklogService) ReopenWithMutationVersion(ctx context.Context, updateID int64, nonce, id string, expected, now time.Time) (bool, error) {
	_, replay, err := s.DB.Mutate(ctx, updateID, nonce, "backlog.reopen", id, func(tx *sql.Tx) (string, error) {
		i, err := s.DB.GetItemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := s.DB.GetProjectTx(ctx, tx, i.ProjectID)
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

func (s *BacklogService) DeleteWithMutationAndClearState(ctx context.Context, updateID int64, nonce, id string, expected time.Time, userID int64) (bool, error) {
	_, replay, err := s.DB.MutateAndClearState(ctx, updateID, nonce, "backlog.delete", id, userID, func(tx *sql.Tx) (string, error) {
		i, err := s.DB.GetItemTx(ctx, tx, id)
		if err != nil {
			return "", mapStoreError(err)
		}
		p, err := s.DB.GetProjectTx(ctx, tx, i.ProjectID)
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

func ptr(t time.Time) *time.Time { return &t }
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if errors.Is(err, repository.ErrDuplicate) || strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return domain.ErrConflict
	}
	return fmt.Errorf("store: %w", err)
}
