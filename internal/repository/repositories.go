package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
)

type Repositories struct {
	Project      *ProjectRepository
	Backlog      *BacklogRepository
	Conversation *ConversationRepository
	Mutation     *MutationRepository
	Notification *NotificationRepository
}

func New(db *sql.DB) *Repositories {
	return &Repositories{
		Project: NewProjectRepository(db), Backlog: NewBacklogRepository(db),
		Conversation: NewConversationRepository(db), Mutation: NewMutationRepository(db),
		Notification: NewNotificationRepository(db),
	}
}

func (r *Repositories) GetAuthorizedChat(ctx context.Context, userID int64) (int64, error) {
	return r.Conversation.GetAuthorizedChat(ctx, userID)
}
func (r *Repositories) BindAuthorizedChat(ctx context.Context, userID, chatID int64) (int64, error) {
	return r.Conversation.BindAuthorizedChat(ctx, userID, chatID)
}
func (r *Repositories) CreateProject(ctx context.Context, p domain.Project) error {
	return r.Project.CreateProject(ctx, p)
}
func (r *Repositories) CreateProjectTx(ctx context.Context, tx *sql.Tx, p domain.Project) error {
	return r.Project.CreateProjectTx(ctx, tx, p)
}
func (r *Repositories) ActiveProjectNameConflict(ctx context.Context, n, id string) (bool, error) {
	return r.Project.ActiveProjectNameConflict(ctx, n, id)
}
func (r *Repositories) ActiveProjectNameConflictTx(ctx context.Context, tx *sql.Tx, n, id string) (bool, error) {
	return r.Project.ActiveProjectNameConflictTx(ctx, tx, n, id)
}
func (r *Repositories) ListProjects(ctx context.Context, archived bool) ([]domain.Project, error) {
	return r.Project.ListProjects(ctx, archived)
}
func (r *Repositories) ListProjectsPage(ctx context.Context, archived bool, limit, offset int) ([]domain.Project, error) {
	return r.Project.ListProjectsPage(ctx, archived, limit, offset)
}
func (r *Repositories) GetProject(ctx context.Context, id string) (domain.Project, error) {
	return r.Project.GetProject(ctx, id)
}
func (r *Repositories) GetProjectTx(ctx context.Context, tx *sql.Tx, id string) (domain.Project, error) {
	return r.Project.GetProjectTx(ctx, tx, id)
}
func (r *Repositories) UpdateProject(ctx context.Context, p domain.Project) error {
	return r.Project.UpdateProject(ctx, p)
}
func (r *Repositories) UpdateProjectIfVersion(ctx context.Context, p domain.Project, expected time.Time) error {
	return r.Project.UpdateProjectIfVersion(ctx, p, expected)
}
func (r *Repositories) UpdateProjectTx(ctx context.Context, tx *sql.Tx, p domain.Project) error {
	return r.Project.UpdateProjectTx(ctx, tx, p)
}
func (r *Repositories) UpdateProjectTxIfVersion(ctx context.Context, tx *sql.Tx, p domain.Project, expected time.Time) error {
	return r.Project.UpdateProjectTxIfVersion(ctx, tx, p, expected)
}
func (r *Repositories) ProjectCounts(ctx context.Context, id string) (int, int, error) {
	return r.Backlog.ProjectCounts(ctx, id)
}
func (r *Repositories) CreateItem(ctx context.Context, i domain.BacklogItem) error {
	return r.Backlog.CreateItem(ctx, i)
}
func (r *Repositories) CreateItemTx(ctx context.Context, tx *sql.Tx, i domain.BacklogItem) error {
	return r.Backlog.CreateItemTx(ctx, tx, i)
}
func (r *Repositories) ListItems(ctx context.Context, done bool, project string) ([]domain.RecommendationItem, error) {
	return r.Backlog.ListItems(ctx, done, project)
}
func (r *Repositories) ListItemsPage(ctx context.Context, f domain.ItemFilter, limit, offset int) ([]domain.RecommendationItem, error) {
	return r.Backlog.ListItemsPage(ctx, f, limit, offset)
}
func (r *Repositories) GetItem(ctx context.Context, id string) (domain.BacklogItem, error) {
	return r.Backlog.GetItem(ctx, id)
}
func (r *Repositories) GetItemTx(ctx context.Context, tx *sql.Tx, id string) (domain.BacklogItem, error) {
	return r.Backlog.GetItemTx(ctx, tx, id)
}
func (r *Repositories) UpdateItem(ctx context.Context, i domain.BacklogItem) error {
	return r.Backlog.UpdateItem(ctx, i)
}
func (r *Repositories) UpdateItemIfVersion(ctx context.Context, i domain.BacklogItem, expected time.Time) error {
	return r.Backlog.UpdateItemIfVersion(ctx, i, expected)
}
func (r *Repositories) UpdateItemTx(ctx context.Context, tx *sql.Tx, i domain.BacklogItem) error {
	return r.Backlog.UpdateItemTx(ctx, tx, i)
}
func (r *Repositories) UpdateItemTxIfVersion(ctx context.Context, tx *sql.Tx, i domain.BacklogItem, expected time.Time) error {
	return r.Backlog.UpdateItemTxIfVersion(ctx, tx, i, expected)
}
func (r *Repositories) DeleteItem(ctx context.Context, id string) error {
	return r.Backlog.DeleteItem(ctx, id)
}
func (r *Repositories) DeleteItemTx(ctx context.Context, tx *sql.Tx, id string) error {
	return r.Backlog.DeleteItemTx(ctx, tx, id)
}
func (r *Repositories) SaveState(ctx context.Context, user int64, flow, step, draft, nonce string, version int, expires time.Time) error {
	return r.Conversation.SaveState(ctx, user, flow, step, draft, nonce, version, expires)
}
func (r *Repositories) SaveStateVersion(ctx context.Context, user int64, flow, step, draft, nonce string, expected, next int, expires time.Time) (bool, error) {
	return r.Conversation.SaveStateVersion(ctx, user, flow, step, draft, nonce, expected, next, expires)
}
func (r *Repositories) ClearState(ctx context.Context, user int64) error {
	return r.Conversation.ClearState(ctx, user)
}
func (r *Repositories) GetState(ctx context.Context, user int64) (string, string, string, string, int, time.Time, error) {
	return r.Conversation.GetState(ctx, user)
}
func (r *Repositories) SaveCallbackToken(ctx context.Context, token string, user int64, payload string, expires time.Time) error {
	return r.Conversation.SaveCallbackToken(ctx, token, user, payload, expires)
}
func (r *Repositories) ResolveCallbackToken(ctx context.Context, token string, user int64, now time.Time) (string, error) {
	return r.Conversation.ResolveCallbackToken(ctx, token, user, now)
}
func (r *Repositories) IsProcessed(ctx context.Context, id int64) (bool, error) {
	return r.Conversation.IsProcessed(ctx, id)
}
func (r *Repositories) MarkProcessed(ctx context.Context, id int64) (bool, error) {
	return r.Conversation.MarkProcessed(ctx, id)
}
func (r *Repositories) Receipt(ctx context.Context, nonce string) (string, error) {
	return r.Conversation.Receipt(ctx, nonce)
}
func (r *Repositories) Mutate(ctx context.Context, updateID int64, nonce, action, entityID string, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	return r.Mutation.Mutate(ctx, updateID, nonce, action, entityID, fn)
}
func (r *Repositories) MutateAndClearState(ctx context.Context, updateID int64, nonce, action, entityID string, userID int64, fn func(*sql.Tx) (string, error)) (string, bool, error) {
	return r.Mutation.MutateAndClearState(ctx, updateID, nonce, action, entityID, userID, fn)
}
func (r *Repositories) RecoverStaleNotifications(ctx context.Context, before time.Time) error {
	return r.Notification.RecoverStaleNotifications(ctx, before)
}
func (r *Repositories) RetryFailedNotifications(ctx context.Context, date string, now time.Time) error {
	return r.Notification.RetryFailedNotifications(ctx, date, now)
}
func (r *Repositories) GetNotificationRun(ctx context.Context, date string) (NotificationRun, error) {
	return r.Notification.GetNotificationRun(ctx, date)
}
func (r *Repositories) EnsureNotificationRun(ctx context.Context, date string, scheduled, now time.Time) error {
	return r.Notification.EnsureNotificationRun(ctx, date, scheduled, now)
}
func (r *Repositories) SnapshotNotification(ctx context.Context, date string, items []domain.RecommendationItem, parts []NotificationPart, now time.Time) error {
	return r.Notification.SnapshotNotification(ctx, date, items, parts, now)
}
func (r *Repositories) NotificationSnapshotExists(ctx context.Context, date string) (bool, error) {
	return r.Notification.NotificationSnapshotExists(ctx, date)
}
func (r *Repositories) NotificationItems(ctx context.Context, date string) ([]NotificationItem, error) {
	return r.Notification.NotificationItems(ctx, date)
}
func (r *Repositories) PendingNotificationParts(ctx context.Context, date string) ([]NotificationPart, error) {
	return r.Notification.PendingNotificationParts(ctx, date)
}
func (r *Repositories) MarkNotificationPartSent(ctx context.Context, date string, index, messageID int, now time.Time) error {
	return r.Notification.MarkNotificationPartSent(ctx, date, index, messageID, now)
}
func (r *Repositories) MarkNotificationSent(ctx context.Context, date string, now time.Time) error {
	return r.Notification.MarkNotificationSent(ctx, date, now)
}
func (r *Repositories) FailNotificationRun(ctx context.Context, date, reason string, now time.Time) error {
	return r.Notification.FailNotificationRun(ctx, date, reason, now)
}
