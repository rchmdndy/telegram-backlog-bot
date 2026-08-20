package repository

import (
	"database/sql"
	"errors"
	"time"
)

var ErrDuplicate = errors.New("duplicate")
var ErrReceiptConflict = errors.New("mutation receipt conflict")
var ErrAuthorizedChatMismatch = errors.New("authorized chat binding mismatch")

func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func micros(t time.Time) int64     { return t.UTC().UnixMicro() }
func fromMicros(v int64) time.Time { return time.UnixMicro(v).UTC() }
func nullableTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMicros(v.Int64)
	return &t
}
func nullableMicros(t *time.Time) any {
	if t == nil {
		return nil
	}
	return micros(*t)
}

type ProjectRepository struct{ db *sql.DB }
type BacklogRepository struct{ db *sql.DB }
type ConversationRepository struct{ db *sql.DB }
type MutationRepository struct{ db *sql.DB }
type NotificationRepository struct{ db *sql.DB }

func NewProjectRepository(db *sql.DB) *ProjectRepository { return &ProjectRepository{db: db} }
func NewBacklogRepository(db *sql.DB) *BacklogRepository { return &BacklogRepository{db: db} }
func NewConversationRepository(db *sql.DB) *ConversationRepository {
	return &ConversationRepository{db: db}
}
func NewMutationRepository(db *sql.DB) *MutationRepository { return &MutationRepository{db: db} }
func NewNotificationRepository(db *sql.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}
