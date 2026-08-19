package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rchmdndy/telegram-backlog-bot/internal/application"
	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/recommendation"
	"github.com/rchmdndy/telegram-backlog-bot/internal/store"
)

type TelegramAPI interface {
	Send(tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	StopReceivingUpdates()
}

type Bot struct {
	api            TelegramAPI
	db             *store.Store
	projectSvc     *application.ProjectService
	backlog        *application.BacklogService
	userID, chatID int64
	limit          int
	loc            *time.Location
	log            *slog.Logger
	Alive          func()
	mu             sync.Mutex
}

func New(api TelegramAPI, db *store.Store, userID, chatID int64, limit int, loc *time.Location, log *slog.Logger) *Bot {
	return &Bot{api: api, db: db, projectSvc: application.NewProjectService(db), backlog: application.NewBacklogService(db), userID: userID, chatID: chatID, limit: limit, loc: loc, log: log}
}
func (b *Bot) Poll(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := b.api.GetUpdatesChan(u)
	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return nil
		case update, ok := <-updates:
			if b.Alive != nil {
				b.Alive()
			}
			if !ok {

				return nil
			}
			b.mu.Lock()
			err := b.handle(ctx, update)
			b.mu.Unlock()
			if err != nil {
				b.log.Error("update failed", "err", err)
			}
		}
	}
}
func (b *Bot) authorized(user, chat int64) bool { return user == b.userID && chat == b.chatID }
func (b *Bot) handle(ctx context.Context, u tgbotapi.Update) error {
	if u.UpdateID == 0 {
		return nil
	}
	if q := u.CallbackQuery; q != nil {
		if q.Message == nil || q.Message.Chat == nil || q.Message.Chat.Type != "private" || !b.authorized(q.From.ID, q.Message.Chat.ID) {
			return nil
		}
		_, _ = b.api.Request(tgbotapi.NewCallback(q.ID, ""))
		if err := b.callback(ctx, q, int64(u.UpdateID)); err != nil {
			return err
		}
		_, err := b.db.MarkProcessed(ctx, int64(u.UpdateID))
		return err
	}

	if u.Message == nil || u.Message.From == nil || u.Message.Chat == nil || u.Message.Chat.Type != "private" || !b.authorized(u.Message.From.ID, u.Message.Chat.ID) {
		return nil
	}
	processed, err := b.db.IsProcessed(ctx, int64(u.UpdateID))
	if err != nil || processed {
		return err
	}
	if err := b.message(ctx, u.Message, int64(u.UpdateID)); err != nil {
		return err
	}
	_, err = b.db.MarkProcessed(ctx, int64(u.UpdateID))
	return err

}
func (b *Bot) Send(ctx context.Context, text string) (int, error) {
	return b.sendNotification(ctx, text, nil)
}

func (b *Bot) SendNotification(ctx context.Context, text string, keys [][]tgbotapi.InlineKeyboardButton) (int, error) {
	return b.sendNotification(ctx, text, keys)
}

func (b *Bot) sendNotification(ctx context.Context, text string, keys [][]tgbotapi.InlineKeyboardButton) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	keys, err := b.opaqueKeys(keys)
	if err != nil {
		return 0, err
	}
	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = "HTML"
	if len(keys) > 0 {
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(keys...)
	}
	returned, err := b.api.Send(msg)
	return returned.MessageID, err
}

func (b *Bot) send(text string, keys [][]tgbotapi.InlineKeyboardButton) error {
	keys, err := b.opaqueKeys(keys)
	if err != nil {
		return err
	}
	parts := LimitMessage(text, 4096)
	for n, part := range parts {
		m := tgbotapi.NewMessage(b.chatID, part)
		m.ParseMode = "HTML"
		if n == len(parts)-1 && len(keys) > 0 {
			m.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(keys...)
		}
		if _, err := b.api.Send(m); err != nil {
			return err
		}
	}
	return nil
}

// opaqueKeys keeps Telegram callback_data below its 64-byte limit while retaining
// the full action, entity id, nonce, and expected version in SQLite.
func (b *Bot) opaqueKeys(keys [][]tgbotapi.InlineKeyboardButton) ([][]tgbotapi.InlineKeyboardButton, error) {
	for row := range keys {
		for col := range keys[row] {
			data := keys[row][col].CallbackData
			if data == nil || !strings.HasPrefix(*data, "v2:") || len(*data) <= 64 {
				continue
			}
			var raw [6]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return nil, err
			}
			token := "t:" + hex.EncodeToString(raw[:])
			if err := b.db.SaveCallbackToken(context.Background(), token, b.userID, *data, time.Now().Add(24*time.Hour)); err != nil {
				return nil, err
			}
			keys[row][col].CallbackData = strPtr(token)
		}
	}
	return keys, nil
}
func menu() [][]tgbotapi.InlineKeyboardButton {
	return [][]tgbotapi.InlineKeyboardButton{
		{tgbotapi.NewInlineKeyboardButtonData("➕ Tambah Backlog", "v2:add")},
		{tgbotapi.NewInlineKeyboardButtonData("📋 Daftar Backlog", "v2:list:0")},
		{tgbotapi.NewInlineKeyboardButtonData("📁 Projects", "v2:projects:0:0")},
		{tgbotapi.NewInlineKeyboardButtonData("☀️ Fokus Hari Ini", "v2:focus")},
		{tgbotapi.NewInlineKeyboardButtonData("❓ Bantuan", "v2:help")},
	}
}
func cancelKeys() [][]tgbotapi.InlineKeyboardButton {
	return [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}}
}

func callbackNonce() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return domain.NewID()
	}
	return hex.EncodeToString(raw[:])
}
func pageValue(s string) int {
	n, _ := strconv.Atoi(s)
	if n < 0 {
		return 0
	}
	return n
}
func callbackParts(data string) []string { return strings.Split(data, ":") }
func (b *Bot) now() time.Time            { return time.Now().In(b.loc) }
func strPtr(v string) *string            { return &v }

func (b *Bot) message(ctx context.Context, m *tgbotapi.Message, updateID int64) error {
	text := strings.TrimSpace(m.Text)
	switch text {
	case "/start", "/menu":
		return b.send("<b>Backlog Bot</b>\nPilih menu:", menu())
	case "/cancel":
		_ = b.db.ClearState(ctx, b.userID)
		return b.send("Draft dibatalkan.", menu())
	case "/help":
		return b.send("Gunakan tombol menu untuk mengelola project dan backlog. /cancel membatalkan wizard aktif.", menu())
	}
	flow, step, raw, nonce, ver, expires, err := b.db.GetState(ctx, b.userID)
	if err != nil {
		return err
	}
	if flow == "" || time.Now().After(expires) {
		return b.send("Gunakan /menu untuk mulai.", menu())
	}
	var d map[string]string
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return b.db.ClearState(ctx, b.userID)
	}
	if step == "name" && flow == "project" {
		if err := domain.ValidateProjectName(text); err != nil {
			return b.send("Nama project wajib 1–80 karakter. Coba lagi.", cancelKeys())
		}
		d["name"] = domain.NormalizeText(text)
		return b.saveDraft(ctx, flow, "description", d, nonce, ver+1, "Deskripsi project (opsional), atau tekan Lewati.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Lewati", "v2:skip:desc:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}})
	}
	if step == "description" && flow == "project" {
		if len([]rune(text)) > 500 {
			return b.send("Deskripsi maksimal 500 karakter.", cancelKeys())
		}
		d["description"] = domain.NormalizeText(text)
		return b.projectPreview(ctx, d, nonce, ver+1)
	}
	if flow == "edit" && step == "input" {
		return b.editText(ctx, d, text, nonce, ver)
	}
	if flow == "backlog" {

		switch step {
		case "title":
			if err := domain.ValidateTitle(text); err != nil {
				return b.send("Judul wajib 1–160 karakter. Coba lagi.", cancelKeys())
			}
			d["title"] = domain.NormalizeText(text)
			return b.chooseProject(ctx, d, nonce, ver+1)
		case "date":
			date, e := domain.ParseDeadline(text, time.Now().In(b.loc))
			if e != nil {
				return b.send("Tanggal tidak valid. Gunakan YYYY-MM-DD atau DD-MM-YYYY dan jangan gunakan tanggal lampau.", cancelKeys())
			}
			d["deadline"] = date
			return b.askNotes(ctx, d, nonce, ver+1)
		case "notes":
			if err := domain.ValidateNotes(text); err != nil {
				return b.send("Catatan maksimal 2.000 karakter.", cancelKeys())
			}
			d["notes"] = domain.NormalizeText(text)
			return b.backlogPreview(ctx, d, nonce, ver+1)
		}
	}
	return b.send("Masukan itu tidak sesuai langkah aktif. Gunakan tombol atau /cancel.", cancelKeys())
}
func (b *Bot) saveDraft(ctx context.Context, flow, step string, d map[string]string, nonce string, ver int, prompt string, keys [][]tgbotapi.InlineKeyboardButton) error {
	raw, _ := json.Marshal(d)
	if ver == 1 {
		if err := b.db.SaveState(ctx, b.userID, flow, step, string(raw), nonce, ver, time.Now().Add(24*time.Hour)); err != nil {
			return err
		}
	} else {
		ok, err := b.db.SaveStateVersion(ctx, b.userID, flow, step, string(raw), nonce, ver-1, ver, time.Now().Add(24*time.Hour))
		if err != nil {
			return err
		}
		if !ok {
			return b.send("Wizard sudah berubah. Buka menu terbaru.", menu())
		}
	}
	return b.send(prompt, keys)
}
func (b *Bot) startProject(ctx context.Context) error {
	return b.saveDraft(ctx, "project", "name", map[string]string{}, domain.NewID(), 1, "Masukkan nama project baru.", cancelKeys())
}
func (b *Bot) startNestedProject(ctx context.Context) error {
	flow, step, raw, nonce, ver, _, err := b.db.GetState(ctx, b.userID)
	if err != nil || flow != "backlog" {
		return b.startProject(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return b.startProject(ctx)
	}
	d["return_flow"], d["return_step"], d["return_nonce"] = flow, step, nonce
	return b.saveDraft(ctx, "project", "name", d, domain.NewID(), ver+1, "Masukkan nama project baru.", cancelKeys())
}
func (b *Bot) startBacklog(ctx context.Context) error {
	return b.saveDraft(ctx, "backlog", "title", map[string]string{}, domain.NewID(), 1, "Masukkan judul backlog.", cancelKeys())
}
func (b *Bot) chooseProject(ctx context.Context, d map[string]string, nonce string, ver int) error {
	ps, err := b.db.ListProjects(ctx, false)
	if err != nil {
		return err
	}
	keys := make([][]tgbotapi.InlineKeyboardButton, 0, len(ps)+2)
	for _, p := range ps {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData(Escape(p.Name), "v2:pickproject:"+p.ID+":"+nonce)})
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Buat Project Baru", "v2:newproject")}, cancelKeys()[0])
	return b.saveDraft(ctx, "backlog", "project", d, nonce, ver, "Pilih project aktif.", keys)
}
func (b *Bot) chooseQuadrant(ctx context.Context, d map[string]string, nonce string, ver int) error {
	keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("🔴 Q1 Do now", "v2:quadrant:q1:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("🔵 Q2 Schedule", "v2:quadrant:q2:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("🟠 Q3 Minimize", "v2:quadrant:q3:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("⚪ Q4 Reconsider", "v2:quadrant:q4:"+nonce)}, cancelKeys()[0]}
	return b.saveDraft(ctx, "backlog", "quadrant", d, nonce, ver, "Pilih prioritas Eisenhower.", keys)
}
func (b *Bot) askDeadline(ctx context.Context, d map[string]string, nonce string, ver int) error {
	keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Hari ini", "v2:date:today:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Besok", "v2:date:tomorrow:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("7 hari lagi", "v2:date:week:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Masukkan tanggal", "v2:date:custom:"+nonce)}, cancelKeys()[0]}
	return b.saveDraft(ctx, "backlog", "deadline", d, nonce, ver, "Pilih deadline.", keys)
}
func (b *Bot) askNotes(ctx context.Context, d map[string]string, nonce string, ver int) error {
	return b.saveDraft(ctx, "backlog", "notes", d, nonce, ver, "Tambahkan catatan opsional, atau tekan Lewati.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Lewati", "v2:skip:notes:"+nonce)}, cancelKeys()[0]})
}
func (b *Bot) projectPreview(ctx context.Context, d map[string]string, nonce string, ver int) error {
	return b.saveDraft(ctx, "project", "confirm", d, nonce, ver, fmt.Sprintf("<b>Preview project</b>\nNama: %s\nDeskripsi: %s", Escape(d["name"]), Escape(d["description"])), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Simpan", "v2:saveproject:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}})
}
func (b *Bot) backlogPreview(ctx context.Context, d map[string]string, nonce string, ver int) error {
	return b.saveDraft(ctx, "backlog", "confirm", d, nonce, ver, fmt.Sprintf("<b>Preview backlog</b>\nProject: %s\nJudul: %s\nQuadrant: %s\nDeadline: %s\nCatatan: %s", Escape(d["project_name"]), Escape(d["title"]), Escape(d["quadrant"]), Escape(d["deadline"]), Escape(d["notes"])), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Simpan", "v2:savebacklog:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}})
}

func (b *Bot) callback(ctx context.Context, q *tgbotapi.CallbackQuery, updateID int64) error {
	data := q.Data
	if strings.HasPrefix(data, "t:") {
		resolved, err := b.db.ResolveCallbackToken(ctx, data, b.userID, b.now())
		if err != nil {
			return b.stale(ctx)
		}
		q.Data = resolved
		data = resolved
	}
	if !strings.HasPrefix(data, "v2:") {
		return b.stale(ctx)
	}
	return b.callbackV2(ctx, q, updateID)
}
func (b *Bot) focus(ctx context.Context) error {
	items, err := b.db.ListItems(ctx, false, "")
	if err != nil {
		return err
	}
	now := time.Now().In(b.loc)
	return b.send(recommendation.Render(recommendation.Select(items, now, b.limit), now), nil)
}
func (b *Bot) callbackV2(ctx context.Context, q *tgbotapi.CallbackQuery, updateID int64) error {
	p := callbackParts(q.Data)
	if len(p) < 2 || p[0] != "v2" {
		return b.stale(ctx)
	}
	switch p[1] {
	case "menu":
		return b.send("Pilih menu:", menu())
	case "cancel":
		_ = b.db.ClearState(ctx, b.userID)
		return b.send("Draft dibatalkan.", menu())
	case "add":
		return b.startBacklog(ctx)
	case "help":
		return b.send("Gunakan tombol menu untuk mengelola project dan backlog. /cancel membatalkan wizard.", menu())
	case "focus":
		return b.focus(ctx)
	case "projects":
		return b.projectsPage(ctx, pageValueAt(p, 2), boolAt(p, 3))
	case "project":
		if len(p) != 3 {
			return b.stale(ctx)
		}
		return b.projectDetail(ctx, p[2])
	case "list":
		return b.listPage(ctx, pageValueAt(p, 2), domain.ItemFilter{DeadlineBucket: -1})
	case "notificationitems":
		if len(p) != 3 {
			return b.stale(ctx)
		}
		return b.notificationItems(ctx, p[2])

	case "item":
		if len(p) != 3 {
			return b.stale(ctx)
		}
		return b.itemDetail(ctx, p[2])
	case "newproject":
		return b.startNestedProject(ctx)
	case "saveproject":
		return b.saveV2Project(ctx, p, updateID)
	case "savebacklog":
		return b.saveV2Backlog(ctx, p, updateID)
	case "pickproject":
		return b.pickV2Project(ctx, p)
	case "quadrant":
		return b.pickV2Quadrant(ctx, p)
	case "date":
		return b.pickV2Date(ctx, p)
	case "skip":
		return b.skipV2(ctx, p)
	case "archive", "restore", "complete", "reopen":
		if len(p) != 4 {
			return b.stale(ctx)
		}
		return b.actionV2(ctx, p[1], p, updateID)
	case "archiveconfirm", "restoreconfirm", "completeconfirm", "reopenconfirm":
		if len(p) != 5 {
			return b.stale(ctx)
		}
		expected, e := time.Parse(time.RFC3339Nano, p[4])
		if e != nil {
			return b.stale(ctx)
		}
		var result string
		switch p[1] {
		case "archiveconfirm":
			_, e = b.projectSvc.ArchiveWithMutationVersion(ctx, updateID, p[3], p[2], expected, b.now())
			result = "Project diarsipkan."
		case "restoreconfirm":
			_, e = b.projectSvc.RestoreWithMutationAndClearState(ctx, updateID, p[3], p[2], expected, b.now(), b.userID)

			result = "Project dipulihkan."
		case "completeconfirm", "reopenconfirm":
			if p[1] == "completeconfirm" {
				_, e = b.backlog.CompleteWithMutationVersion(ctx, updateID, p[3], p[2], expected, b.now())
				result = "Backlog ditandai selesai."
			} else {
				_, e = b.backlog.ReopenWithMutationVersion(ctx, updateID, p[3], p[2], expected, b.now())
				result = "Backlog dibuka kembali."
			}
		}
		if e != nil {
			if errors.Is(e, domain.ErrConflict) && p[1] == "restoreconfirm" {
				return b.send("Nama project sudah dipakai. Pilih Rename atau batalkan.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Rename", "v2:editproject:"+p[2]+":name")}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
			}
			return b.send("Operasi ditolak karena data sudah berubah atau project archived.", menu())
		}
		return b.send(result, menu())

	case "editproject", "edititem":
		return b.startEdit(ctx, p)
	case "editvalue":
		return b.editValueCallback(ctx, p)
	case "editdraft":
		return b.editDraftCallback(ctx, p)
	case "moveproject":
		return b.moveProjectCallback(ctx, p)

	case "saveedit":

		return b.saveEdit(ctx, p, updateID)
	case "projectitems":
		if len(p) != 4 {
			return b.stale(ctx)
		}
		return b.projectItems(ctx, p[2], pageValue(p[3]))
	case "filter":
		return b.filterMenu(ctx, p)
	case "listfilter":
		return b.listFilter(ctx, p)

	case "delete":
		return b.deleteV2(ctx, p)
	case "deleteconfirm":
		return b.deleteConfirmV2(ctx, p, updateID)
	}
	return b.stale(ctx)
}
func pageValueAt(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	return pageValue(p[i])
}
func boolAt(p []string, i int) bool { return i < len(p) && p[i] == "1" }
func (b *Bot) stale(ctx context.Context) error {
	return b.send("Tombol sudah kedaluwarsa. Buka menu terbaru.", menu())
}
func (b *Bot) verifyDraft(ctx context.Context, flow, step, n string) (map[string]string, int, error) {
	f, s, raw, got, v, expires, err := b.db.GetState(ctx, b.userID)
	if err != nil || f != flow || s != step || got != n || time.Now().After(expires) {
		return nil, 0, domain.ErrConflict
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return nil, 0, domain.ErrConflict
	}
	return d, v, nil
}
func (b *Bot) skipV2(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	n := p[3]
	if d, v, err := b.verifyDraft(ctx, "project", "description", n); err == nil && p[2] == "desc" {
		d["description"] = ""
		return b.projectPreview(ctx, d, n, v+1)
	}
	if d, v, err := b.verifyDraft(ctx, "backlog", "notes", n); err == nil && p[2] == "notes" {
		d["notes"] = ""
		return b.backlogPreview(ctx, d, n, v+1)
	}
	return b.stale(ctx)
}
func (b *Bot) pickV2Project(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	d, v, e := b.verifyDraft(ctx, "backlog", "project", p[3])
	if e != nil {
		return b.stale(ctx)
	}
	pr, e := b.db.GetProject(ctx, p[2])
	if e != nil || pr.Status != domain.ProjectActive {
		return b.stale(ctx)
	}
	d["project_id"], d["project_name"] = pr.ID, pr.Name
	return b.chooseQuadrant(ctx, d, p[3], v+1)
}
func (b *Bot) pickV2Quadrant(ctx context.Context, p []string) error {
	if len(p) != 4 || !domain.ValidQuadrant(domain.Quadrant(p[2])) {
		return b.stale(ctx)
	}
	d, v, e := b.verifyDraft(ctx, "backlog", "quadrant", p[3])
	if e != nil {
		return b.stale(ctx)
	}
	d["quadrant"] = p[2]
	return b.askDeadline(ctx, d, p[3], v+1)
}
func (b *Bot) pickV2Date(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	d, v, e := b.verifyDraft(ctx, "backlog", "deadline", p[3])
	if e != nil {
		return b.stale(ctx)
	}
	now := b.now()
	switch p[2] {
	case "today":
		d["deadline"] = now.Format("2006-01-02")
	case "tomorrow":
		d["deadline"] = now.AddDate(0, 0, 1).Format("2006-01-02")
	case "week":
		d["deadline"] = now.AddDate(0, 0, 7).Format("2006-01-02")
	case "custom":
		return b.saveDraft(ctx, "backlog", "date", d, p[3], v+1, "Masukkan tanggal YYYY-MM-DD atau DD-MM-YYYY.", cancelKeys())
	default:
		return b.stale(ctx)
	}
	return b.askNotes(ctx, d, p[3], v+1)
}
func (b *Bot) saveV2Project(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 3 {
		return b.stale(ctx)
	}
	if receipt, e := b.db.Receipt(ctx, p[2]); e == nil && receipt != "" {
		return b.send("Operasi sudah diproses.", menu())
	}
	flow, step, raw, n, _, _, e := b.db.GetState(ctx, b.userID)
	if e != nil || flow != "project" || step != "confirm" || n != p[2] {
		return b.stale(ctx)
	}
	var d map[string]string
	_ = json.Unmarshal([]byte(raw), &d)
	pr, _, e := b.projectSvc.CreateWithMutationAndClearState(ctx, updateID, n, d["name"], d["description"], b.now(), b.userID)
	if e != nil {
		return b.send("Project gagal disimpan; nama mungkin sudah digunakan.", menu())
	}
	return b.send("Project disimpan: <b>"+Escape(pr.Name)+"</b>", menu())
}
func (b *Bot) saveV2Backlog(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 3 {
		return b.stale(ctx)
	}
	if receipt, e := b.db.Receipt(ctx, p[2]); e == nil && receipt != "" {
		return b.send("Operasi sudah diproses.", menu())
	}
	flow, step, raw, n, _, _, e := b.db.GetState(ctx, b.userID)
	if e != nil || flow != "backlog" || step != "confirm" || n != p[2] {
		return b.stale(ctx)
	}
	var d map[string]string
	_ = json.Unmarshal([]byte(raw), &d)
	_, _, e = b.backlog.CreateWithMutationAndClearState(ctx, updateID, n, d["project_id"], d["title"], d["notes"], domain.Quadrant(d["quadrant"]), d["deadline"], b.now(), b.userID)
	if e != nil {
		return e
	}
	return b.send("Backlog disimpan.", menu())
}
func (b *Bot) projectDetail(ctx context.Context, id string) error {
	p, e := b.db.GetProject(ctx, id)
	if e != nil {
		return b.stale(ctx)
	}
	a, d, e := b.db.ProjectCounts(ctx, id)
	if e != nil {
		return e
	}
	text := fmt.Sprintf("<b>Project: %s</b>\n%s\nStatus: %s\nAktif: %d · Selesai: %d", Escape(p.Name), Escape(p.Description), Escape(string(p.Status)), a, d)
	keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Lihat Backlog", "v2:projectitems:"+id+":0")}}
	if p.Status == domain.ProjectActive {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Ubah Nama", "v2:editproject:"+id+":name"), tgbotapi.NewInlineKeyboardButtonData("Ubah Deskripsi", "v2:editproject:"+id+":description")}, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Arsipkan", "v2:archive:"+id+":"+callbackNonce())})
	} else {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Pulihkan", "v2:restore:"+id+":"+callbackNonce())})
	}
	keys = append(keys, menu()[2])
	return b.send(text, keys)
}
func (b *Bot) projectsPage(ctx context.Context, page int, archived bool) error {
	ps, e := b.db.ListProjectsPage(ctx, archived, 8, page*8)
	if e != nil {
		return e
	}
	var s strings.Builder
	s.WriteString("<b>Projects")
	if archived {
		s.WriteString(" archived")
	}
	s.WriteString("</b>\n")
	keys := [][]tgbotapi.InlineKeyboardButton{}
	for _, p := range ps {
		s.WriteString("• " + Escape(p.Name) + "\n")
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: p.Name, CallbackData: strPtr("v2:project:" + p.ID)}})
	}
	if len(ps) == 0 {
		s.WriteString("Belum ada project.\n")
	}
	nav := []tgbotapi.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("‹", "v2:projects:"+strconv.Itoa(page-1)+":"+map[bool]string{true: "1", false: "0"}[archived]))
	}
	if len(ps) == 8 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("›", "v2:projects:"+strconv.Itoa(page+1)+":"+map[bool]string{true: "1", false: "0"}[archived]))
	}
	if len(nav) > 0 {
		keys = append(keys, nav)
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Archived / Aktif", "v2:projects:0:"+map[bool]string{true: "0", false: "1"}[archived])}, menu()[0], menu()[2])
	return b.send(s.String(), keys)
}
func (b *Bot) notificationItems(ctx context.Context, date string) error {
	items, err := b.db.NotificationItems(ctx, date)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return b.send("Tidak ada backlog tersnapshot untuk ditandai.", menu())
	}
	var text strings.Builder
	text.WriteString("<b>Tandai Selesai — snapshot " + Escape(date) + "</b>\n")
	keys := make([][]tgbotapi.InlineKeyboardButton, 0, len(items)+1)
	for _, item := range items {
		_, _ = fmt.Fprintf(&text, "%d. %s — %s\n", item.Ordinal, Escape(item.ProjectName), Escape(item.Title))
		if item.BacklogItemID != "" {
			keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("✅ "+item.Title, "v2:complete:"+item.BacklogItemID+":"+callbackNonce())})
		}
	}
	keys = append(keys, menu()[1])
	return b.send(text.String(), keys)
}

func (b *Bot) listPage(ctx context.Context, page int, filter domain.ItemFilter) error {
	filter.Today = b.now().Format("2006-01-02")
	items, e := b.db.ListItemsPage(ctx, filter, 8, page*8)
	if e != nil {
		return e
	}
	var s strings.Builder
	s.WriteString("<b>Backlog aktif</b>\n")
	keys := [][]tgbotapi.InlineKeyboardButton{}
	for _, r := range items {
		_, _ = fmt.Fprintf(&s, "• %s — %s\n", Escape(r.ProjectName), Escape(r.Item.Title))
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "Buka " + r.Item.Title, CallbackData: strPtr("v2:item:" + r.Item.ID)}})
	}
	if len(items) == 0 {
		s.WriteString("Belum ada backlog aktif.\n")
	}
	nav := []tgbotapi.InlineKeyboardButton{}
	if page > 0 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("‹", "v2:list:"+strconv.Itoa(page-1)))
	}
	if len(items) == 8 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("›", "v2:list:"+strconv.Itoa(page+1)))
	}
	if len(nav) > 0 {
		keys = append(keys, nav)
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Filter Project", "v2:filter:project:0"), tgbotapi.NewInlineKeyboardButtonData("Filter Status", "v2:filter:status"), tgbotapi.NewInlineKeyboardButtonData("Filter Deadline", "v2:filter:deadline")})
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Filter Prioritas", "v2:filter:quadrant")})
	keys = append(keys, menu()[1], menu()[2])
	return b.send(s.String(), keys)
}
func (b *Bot) projectItems(ctx context.Context, projectID string, page int) error {
	p, err := b.db.GetProject(ctx, projectID)
	if err != nil {
		return b.stale(ctx)
	}
	items, err := b.db.ListItemsPage(ctx, domain.ItemFilter{ProjectID: projectID, IncludeArchived: true, DeadlineBucket: -1}, 8, page*8)
	if err != nil {
		return err
	}
	var s strings.Builder
	s.WriteString("<b>Riwayat: " + Escape(p.Name) + "</b>\n")
	keys := [][]tgbotapi.InlineKeyboardButton{}
	for _, r := range items {
		s.WriteString("• " + Escape(r.Item.Title) + " (" + Escape(string(r.Item.Status)) + ")\n")
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "Buka " + r.Item.Title, CallbackData: strPtr("v2:item:" + r.Item.ID)}})
	}
	if len(items) == 0 {
		s.WriteString("Belum ada riwayat backlog.\n")
	}
	if page > 0 {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("‹", "v2:projectitems:"+projectID+":"+strconv.Itoa(page-1))})
	}
	if len(items) == 8 {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("›", "v2:projectitems:"+projectID+":"+strconv.Itoa(page+1))})
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Kembali", "v2:project:"+projectID)})
	return b.send(s.String(), keys)
}
func (b *Bot) filterMenu(ctx context.Context, p []string) error {
	if len(p) < 3 {
		return b.stale(ctx)
	}
	switch p[2] {
	case "project":
		page := pageValueAt(p, 3)
		ps, e := b.db.ListProjectsPage(ctx, false, 8, page*8)
		if e != nil {
			return e
		}
		keys := [][]tgbotapi.InlineKeyboardButton{}
		for _, x := range ps {
			keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData(x.Name, "v2:listfilter:project:"+x.ID)})
		}
		nav := []tgbotapi.InlineKeyboardButton{}
		if page > 0 {
			nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("‹", "v2:filter:project:"+strconv.Itoa(page-1)))
		}
		if len(ps) == 8 {
			nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("›", "v2:filter:project:"+strconv.Itoa(page+1)))
		}
		if len(nav) > 0 {
			keys = append(keys, nav)
		}
		return b.send("Pilih project:", keys)
	case "status":
		return b.send("Pilih status:", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Aktif", "v2:listfilter:status:active")}, {tgbotapi.NewInlineKeyboardButtonData("Selesai", "v2:listfilter:status:done")}})
	case "deadline":
		return b.send("Pilih deadline:", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Terlambat", "v2:listfilter:deadline:0")}, {tgbotapi.NewInlineKeyboardButtonData("Hari ini", "v2:listfilter:deadline:1")}, {tgbotapi.NewInlineKeyboardButtonData("Mendatang", "v2:listfilter:deadline:2")}})
	case "quadrant":
		return b.send("Pilih prioritas:", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Q1", "v2:listfilter:quadrant:q1")}, {tgbotapi.NewInlineKeyboardButtonData("Q2", "v2:listfilter:quadrant:q2")}, {tgbotapi.NewInlineKeyboardButtonData("Q3", "v2:listfilter:quadrant:q3")}, {tgbotapi.NewInlineKeyboardButtonData("Q4", "v2:listfilter:quadrant:q4")}})
	}
	return b.stale(ctx)
}
func (b *Bot) listFilter(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	f := domain.ItemFilter{DeadlineBucket: -1}
	switch p[2] {
	case "project":
		f.ProjectID = p[3]
	case "quadrant":
		f.Quadrant = domain.Quadrant(p[3])
		f.Status = domain.ItemActive
	case "status":
		f.Status = domain.ItemStatus(p[3])
	case "deadline":
		f.Status = domain.ItemActive
		f.DeadlineBucket, _ = strconv.Atoi(p[3])
	default:
		return b.stale(ctx)
	}
	if (f.Quadrant != "" && !domain.ValidQuadrant(f.Quadrant)) || (f.Status != "" && f.Status != domain.ItemActive && f.Status != domain.ItemDone) || f.DeadlineBucket < -1 || f.DeadlineBucket > 2 {
		return b.stale(ctx)
	}
	return b.listPage(ctx, 0, f)
}
func (b *Bot) startEdit(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	id, field := p[2], p[3]
	i, e := b.db.GetItem(ctx, id)
	if p[1] == "editproject" {
		pr, er := b.db.GetProject(ctx, id)
		if er != nil {
			return b.stale(ctx)
		}
		d := map[string]string{"entity_id": id, "edit_field": field, "project_name": pr.Name, "description": pr.Description, "name": pr.Name, "expected_updated": pr.UpdatedAt.Format(time.RFC3339Nano)}
		_ = i
		n := callbackNonce()
		return b.saveDraft(ctx, "edit", "value", d, n, 1, "Pilih field yang akan diubah.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Nama", "v2:editvalue:name:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Deskripsi", "v2:editvalue:description:"+n)}, cancelKeys()[0]})

	}
	if e != nil {
		return b.stale(ctx)
	}
	pr, e := b.db.GetProject(ctx, i.ProjectID)
	if e != nil || pr.Status == domain.ProjectArchived || i.Status == domain.ItemDone {
		return b.stale(ctx)
	}
	d := map[string]string{"entity_id": id, "edit_field": field, "title": i.Title, "notes": i.Notes, "deadline": i.DeadlineDate, "quadrant": string(i.Quadrant), "project_id": i.ProjectID, "project_name": pr.Name, "expected_updated": i.UpdatedAt.Format(time.RFC3339Nano)}
	n := callbackNonce()
	return b.saveDraft(ctx, "edit", "value", d, n, 1, "Pilih field yang akan diubah.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Judul", "v2:editvalue:title:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Prioritas", "v2:editvalue:quadrant:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Deadline", "v2:editvalue:deadline:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Catatan", "v2:editvalue:notes:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Pindah Project", "v2:moveproject:"+n)}, cancelKeys()[0]})
}
func (b *Bot) editText(ctx context.Context, d map[string]string, text, n string, v int) error {
	field := d["edit_field"]
	switch field {
	case "name":
		if e := domain.ValidateProjectName(text); e != nil {
			return b.send("Nama project wajib 1–80 karakter.", cancelKeys())
		}
	case "description":
		if e := domain.ValidateDescription(text); e != nil {
			return b.send("Deskripsi maksimal 500 karakter.", cancelKeys())
		}
	case "title":
		if e := domain.ValidateTitle(text); e != nil {
			return b.send("Judul wajib 1–160 karakter.", cancelKeys())
		}
	case "notes":
		if e := domain.ValidateNotes(text); e != nil {
			return b.send("Catatan maksimal 2.000 karakter.", cancelKeys())
		}
	case "deadline":
		x, e := domain.ParseEditableDeadline(text, b.now())
		if e != nil {
			return b.send("Tanggal tidak valid.", cancelKeys())
		}
		d[field] = x
	}
	if field != "deadline" {
		d[field] = domain.NormalizeText(text)
	}
	delete(d, "edit_field")
	return b.saveDraft(ctx, "edit", "confirm", d, n, v+1, editPreview(d), editSelectors(d, n))
}
func editPreview(d map[string]string) string {
	if d["project_id"] == "" {
		return fmt.Sprintf("<b>Preview perubahan</b>\nProject: %s\nJudul: %s\nQuadrant: %s\nDeadline: %s\nCatatan: %s", Escape(d["project_name"]), Escape(d["title"]), Escape(d["quadrant"]), Escape(d["deadline"]), Escape(d["notes"]))
	}
	return fmt.Sprintf("<b>Preview perubahan</b>\nNama: %s\nDeskripsi: %s", Escape(d["name"]), Escape(d["description"]))
}
func editSelectors(d map[string]string, nonce string) [][]tgbotapi.InlineKeyboardButton {
	keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Simpan Perubahan", "v2:saveedit:"+nonce)}}
	if d["project_id"] == "" {
		keys = append(keys,
			[]tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Judul", "v2:editdraft:title:"+nonce), tgbotapi.NewInlineKeyboardButtonData("Quadrant", "v2:editdraft:quadrant:"+nonce)},
			[]tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Deadline", "v2:editdraft:deadline:"+nonce), tgbotapi.NewInlineKeyboardButtonData("Catatan", "v2:editdraft:notes:"+nonce)},
			[]tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Pindah Project", "v2:moveproject:"+nonce)})
	} else {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Nama", "v2:editdraft:name:"+nonce), tgbotapi.NewInlineKeyboardButtonData("Deskripsi", "v2:editdraft:description:"+nonce)})
	}
	return append(keys, cancelKeys()[0])
}

func (b *Bot) editDraftCallback(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	field, n := p[2], p[3]
	if field != "name" && field != "description" && field != "title" && field != "notes" && field != "deadline" && field != "quadrant" && field != "project" {
		return b.stale(ctx)
	}
	f, s, raw, got, v, ex, err := b.db.GetState(ctx, b.userID)
	if err != nil || f != "edit" || s != "confirm" || got != n || time.Now().After(ex) {
		return b.stale(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return b.stale(ctx)
	}
	d["edit_field"] = field
	return b.saveDraft(ctx, "edit", "input", d, n, v+1, "Masukkan nilai baru.", cancelKeys())
}

func (b *Bot) moveProjectCallback(ctx context.Context, p []string) error {
	if len(p) != 3 {
		return b.stale(ctx)
	}
	f, s, raw, n, _, ex, err := b.db.GetState(ctx, b.userID)
	if err != nil || f != "edit" || s != "value" || n != p[2] || time.Now().After(ex) {
		return b.stale(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil || d["project_id"] == "" {
		return b.stale(ctx)
	}
	projects, err := b.db.ListProjects(ctx, false)
	if err != nil {
		return err
	}
	keys := make([][]tgbotapi.InlineKeyboardButton, 0, len(projects)+1)
	for _, project := range projects {
		if project.ID != d["project_id"] {
			keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData(project.Name, "v2:editvalue:project:"+project.ID+":"+n)})
		}
	}
	keys = append(keys, cancelKeys()[0])
	return b.send("Pilih project baru.", keys)
}

func (b *Bot) editValueCallback(ctx context.Context, p []string) error {
	if len(p) != 4 && len(p) != 5 {
		return b.stale(ctx)
	}
	n := p[len(p)-1]
	f, s, raw, _, v, ex, e := b.db.GetState(ctx, b.userID)
	if e != nil || f != "edit" || s != "value" || n != p[len(p)-1] || time.Now().After(ex) {
		return b.stale(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return b.stale(ctx)
	}
	if len(p) == 5 && p[2] == "project" {
		project, err := b.db.GetProject(ctx, p[3])
		if err != nil || project.Status != domain.ProjectActive {
			return b.stale(ctx)
		}
		d["project_id"], d["project_name"] = project.ID, project.Name
		d["edit_field"] = "project"
		return b.editText(ctx, d, project.Name, n, v)
	}
	d["edit_field"] = p[2]
	return b.saveDraft(ctx, "edit", "input", d, n, v+1, "Masukkan nilai baru.", cancelKeys())
}
func (b *Bot) saveEdit(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 3 {
		return b.stale(ctx)
	}
	f, s, raw, n, _, _, e := b.db.GetState(ctx, b.userID)
	if e != nil || f != "edit" || s != "confirm" || n != p[2] {
		return b.stale(ctx)
	}
	var d map[string]string
	_ = json.Unmarshal([]byte(raw), &d)
	id := d["entity_id"]
	if d["project_id"] != "" {
		i, e := b.db.GetItem(ctx, id)
		if e != nil {
			return b.stale(ctx)
		}
		pr, e := b.db.GetProject(ctx, i.ProjectID)
		if e != nil {
			return b.stale(ctx)
		}
		i.ProjectID = d["project_id"]
		i.Title, i.Notes, i.DeadlineDate, i.Quadrant = d["title"], d["notes"], d["deadline"], domain.Quadrant(d["quadrant"])
		expected, parseErr := time.Parse(time.RFC3339Nano, d["expected_updated"])
		if parseErr != nil {
			return b.stale(ctx)
		}
		_, e = b.backlog.UpdateWithMutationAndClearState(ctx, updateID, n, i, pr, expected, b.now(), b.userID)
		if e != nil {
			return b.send("Perubahan ditolak karena data sudah berubah.", menu())
		}

	} else {
		pr, e := b.db.GetProject(ctx, id)
		if e != nil {
			return b.stale(ctx)
		}
		expected, parseErr := time.Parse(time.RFC3339Nano, d["expected_updated"])
		if parseErr != nil {
			return b.stale(ctx)
		}
		_, _, e = b.projectSvc.UpdateWithMutationAndClearState(ctx, updateID, n, pr, d["name"], d["description"], expected, b.now(), b.userID)
		if e != nil {
			return b.send("Perubahan project ditolak.", menu())
		}
	}
	return b.send("Perubahan disimpan.", menu())
}
func (b *Bot) itemDetail(ctx context.Context, id string) error {
	i, e := b.db.GetItem(ctx, id)
	if e != nil {
		return b.stale(ctx)
	}
	p, e := b.db.GetProject(ctx, i.ProjectID)
	if e != nil {
		return b.stale(ctx)
	}
	archived := p.Status == domain.ProjectArchived
	text := fmt.Sprintf("<b>%s</b>\nProject: %s\nQuadrant: %s\nDeadline: %s\nStatus: %s\nCatatan: %s", Escape(i.Title), Escape(p.Name), Escape(string(i.Quadrant)), Escape(i.DeadlineDate), Escape(string(i.Status)), Escape(i.Notes))
	keys := [][]tgbotapi.InlineKeyboardButton{}
	if archived {
		if i.Status == domain.ItemActive {
			keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "✅ Tandai Selesai", CallbackData: strPtr("v2:complete:" + id + ":" + callbackNonce())}})
		}
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Kembali ke project", "v2:project:"+p.ID)})
		return b.send(text+"\n\nProject archived: detail read-only; hanya penyelesaian diizinkan.", keys)
	}
	if i.Status == domain.ItemActive {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "✅ Tandai Selesai", CallbackData: strPtr("v2:complete:" + id + ":" + callbackNonce())}})
	} else {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "Buka Kembali", CallbackData: strPtr("v2:reopen:" + id + ":" + callbackNonce())}})
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Edit", "v2:edititem:"+id+":all"), tgbotapi.NewInlineKeyboardButtonData("Hapus", "v2:delete:"+id+":"+callbackNonce())}, menu()[1])
	return b.send(text, keys)
}
func (b *Bot) actionV2(ctx context.Context, action string, p []string, updateID int64) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	id, n := p[2], p[3]
	switch action {
	case "archive":
		pr, e := b.db.GetProject(ctx, id)
		if e != nil {
			return b.stale(ctx)
		}
		return b.send(fmt.Sprintf("<b>Preview arsip project</b>\nNama: %s\nBacklog tetap tersimpan dan menjadi read-only.", Escape(pr.Name)), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Ya, arsipkan", "v2:archiveconfirm:"+id+":"+n+":"+pr.UpdatedAt.Format(time.RFC3339Nano)), tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
	case "restore":
		pr, e := b.db.GetProject(ctx, id)
		if e != nil {
			return b.stale(ctx)
		}
		return b.send(fmt.Sprintf("<b>Preview pulihkan project</b>\nNama: %s", Escape(pr.Name)), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Ya, pulihkan", "v2:restoreconfirm:"+id+":"+n+":"+pr.UpdatedAt.Format(time.RFC3339Nano)), tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
	case "complete", "reopen":
		i, e := b.db.GetItem(ctx, id)
		if e != nil {
			return b.stale(ctx)
		}
		label := map[string]string{"complete": "Tandai selesai", "reopen": "Buka kembali"}[action]
		return b.send(fmt.Sprintf("<b>Preview perubahan</b>\nItem: %s\nAksi: %s", Escape(i.Title), label), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Konfirmasi", "v2:"+action+"confirm:"+id+":"+n+":"+i.UpdatedAt.Format(time.RFC3339Nano)), tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
	}
	return b.stale(ctx)
}
func (b *Bot) deleteV2(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return b.stale(ctx)
	}
	i, e := b.db.GetItem(ctx, p[2])
	if e != nil {
		return b.stale(ctx)
	}
	return b.send("Hapus item ini permanen?", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Ya, hapus permanen", "v2:deleteconfirm:"+p[2]+":"+p[3]+":"+i.UpdatedAt.Format(time.RFC3339Nano))}, menu()[1]})
}
func (b *Bot) deleteConfirmV2(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 5 {
		return b.stale(ctx)
	}
	expected, e := time.Parse(time.RFC3339Nano, p[4])
	if e != nil {
		return b.stale(ctx)
	}
	_, e = b.backlog.DeleteWithMutationAndClearState(ctx, updateID, p[3], p[2], expected, b.userID)
	if e != nil {
		return b.send("Penghapusan ditolak; project mungkin archived atau item sudah berubah.", menu())
	}
	return b.send("Backlog dihapus permanen.", menu())
}
