package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/rchmdndy/telegram-backlog-bot/internal/application"
	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
	"github.com/rchmdndy/telegram-backlog-bot/internal/recommendation"
	"github.com/rchmdndy/telegram-backlog-bot/internal/repository"
)

type Handler struct {
	bot        *Bot
	db         *repository.Repositories
	projectSvc *application.ProjectService
	backlog    *application.BacklogService
	notifier   *application.NotificationService
	userID     int64
	limit      int
	loc        *time.Location
	log        *slog.Logger
}

func (h *Handler) now() time.Time { return time.Now().In(h.loc) }

func (h *Handler) message(ctx context.Context, m *tgbotapi.Message, updateID int64) error {
	text := strings.TrimSpace(m.Text)
	switch text {
	case "/start", "/menu":
		return h.bot.send("<b>Backlog Bot</b>\nPilih menu:", menu())
	case "/cancel":
		_ = h.db.ClearState(ctx, h.userID)
		return h.bot.send("Draft dibatalkan.", menu())
	case "/help":
		return h.bot.send("Gunakan tombol menu untuk mengelola project dan backlog. /cancel membatalkan wizard aktif.", menu())
	}
	flow, step, raw, nonce, ver, expires, err := h.db.GetState(ctx, h.userID)
	if err != nil {
		return err
	}
	if flow == "" || time.Now().After(expires) {
		return h.bot.send("Gunakan /menu untuk mulai.", menu())
	}
	var d map[string]string
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return h.db.ClearState(ctx, h.userID)
	}
	if step == "name" && flow == "project" {
		if err := domain.ValidateProjectName(text); err != nil {
			return h.bot.send("Nama project wajib 1–80 karakter. Coba lagi.", cancelKeys())
		}
		d["name"] = domain.NormalizeText(text)
		return h.saveDraft(ctx, flow, "description", d, nonce, ver+1, "Deskripsi project (opsional), atau tekan Lewati.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Lewati", "v2:skip:desc:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}})
	}
	if step == "description" && flow == "project" {
		if len([]rune(text)) > 500 {
			return h.bot.send("Deskripsi maksimal 500 karakter.", cancelKeys())
		}
		d["description"] = domain.NormalizeText(text)
		return h.projectPreview(ctx, d, nonce, ver+1)
	}
	if flow == "edit" && step == "input" {
		return h.editText(ctx, d, text, nonce, ver)
	}
	if flow == "backlog" {

		switch step {
		case "title":
			if err := domain.ValidateTitle(text); err != nil {
				return h.bot.send("Judul wajib 1–160 karakter. Coba lagi.", cancelKeys())
			}
			d["title"] = domain.NormalizeText(text)
			return h.chooseProject(ctx, d, nonce, ver+1)
		case "date":
			date, e := domain.ParseDeadline(text, time.Now().In(h.loc))
			if e != nil {
				return h.bot.send("Tanggal tidak valid. Gunakan YYYY-MM-DD atau DD-MM-YYYY dan jangan gunakan tanggal lampau.", cancelKeys())
			}
			d["deadline"] = date
			return h.askNotes(ctx, d, nonce, ver+1)
		case "notes":
			if err := domain.ValidateNotes(text); err != nil {
				return h.bot.send("Catatan maksimal 2.000 karakter.", cancelKeys())
			}
			d["notes"] = domain.NormalizeText(text)
			return h.backlogPreview(ctx, d, nonce, ver+1)
		}
	}
	return h.bot.send("Masukan itu tidak sesuai langkah aktif. Gunakan tombol atau /cancel.", cancelKeys())
}
func (h *Handler) saveDraft(ctx context.Context, flow, step string, d map[string]string, nonce string, ver int, prompt string, keys [][]tgbotapi.InlineKeyboardButton) error {
	raw, _ := json.Marshal(d)
	if ver == 1 {
		if err := h.db.SaveState(ctx, h.userID, flow, step, string(raw), nonce, ver, time.Now().Add(24*time.Hour)); err != nil {
			return err
		}
	} else {
		ok, err := h.db.SaveStateVersion(ctx, h.userID, flow, step, string(raw), nonce, ver-1, ver, time.Now().Add(24*time.Hour))
		if err != nil {
			return err
		}
		if !ok {
			return h.bot.send("Wizard sudah berubah. Buka menu terbaru.", menu())
		}
	}
	return h.bot.send(prompt, keys)
}
func (h *Handler) startProject(ctx context.Context) error {
	return h.saveDraft(ctx, "project", "name", map[string]string{}, domain.NewID(), 1, "Masukkan nama project baru.", cancelKeys())
}
func (h *Handler) startNestedProject(ctx context.Context) error {
	flow, step, raw, nonce, ver, _, err := h.db.GetState(ctx, h.userID)
	if err != nil || flow != "backlog" {
		return h.startProject(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return h.startProject(ctx)
	}
	d["return_flow"], d["return_step"], d["return_nonce"] = flow, step, nonce
	return h.saveDraft(ctx, "project", "name", d, domain.NewID(), ver+1, "Masukkan nama project baru.", cancelKeys())
}
func (h *Handler) startBacklog(ctx context.Context) error {
	return h.saveDraft(ctx, "backlog", "title", map[string]string{}, domain.NewID(), 1, "Masukkan judul backlog.", cancelKeys())
}
func (h *Handler) chooseProject(ctx context.Context, d map[string]string, nonce string, ver int) error {
	ps, err := h.projectSvc.List(ctx, false)
	if err != nil {
		return err
	}
	keys := make([][]tgbotapi.InlineKeyboardButton, 0, len(ps)+2)
	for _, p := range ps {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData(Escape(p.Name), "v2:pickproject:"+p.ID+":"+nonce)})
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Buat Project Baru", "v2:newproject")}, cancelKeys()[0])
	return h.saveDraft(ctx, "backlog", "project", d, nonce, ver, "Pilih project aktif.", keys)
}
func (h *Handler) chooseQuadrant(ctx context.Context, d map[string]string, nonce string, ver int) error {
	keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("🔴 Q1 Do now", "v2:quadrant:q1:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("🔵 Q2 Schedule", "v2:quadrant:q2:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("🟠 Q3 Minimize", "v2:quadrant:q3:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("⚪ Q4 Reconsider", "v2:quadrant:q4:"+nonce)}, cancelKeys()[0]}
	return h.saveDraft(ctx, "backlog", "quadrant", d, nonce, ver, "Pilih prioritas Eisenhower.", keys)
}
func (h *Handler) askDeadline(ctx context.Context, d map[string]string, nonce string, ver int) error {
	keys := [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Hari ini", "v2:date:today:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Besok", "v2:date:tomorrow:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("7 hari lagi", "v2:date:week:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Masukkan tanggal", "v2:date:custom:"+nonce)}, cancelKeys()[0]}
	return h.saveDraft(ctx, "backlog", "deadline", d, nonce, ver, "Pilih deadline.", keys)
}
func (h *Handler) askNotes(ctx context.Context, d map[string]string, nonce string, ver int) error {
	return h.saveDraft(ctx, "backlog", "notes", d, nonce, ver, "Tambahkan catatan opsional, atau tekan Lewati.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Lewati", "v2:skip:notes:"+nonce)}, cancelKeys()[0]})
}
func (h *Handler) projectPreview(ctx context.Context, d map[string]string, nonce string, ver int) error {
	return h.saveDraft(ctx, "project", "confirm", d, nonce, ver, fmt.Sprintf("<b>Preview project</b>\nNama: %s\nDeskripsi: %s", Escape(d["name"]), Escape(d["description"])), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Simpan", "v2:saveproject:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}})
}
func (h *Handler) backlogPreview(ctx context.Context, d map[string]string, nonce string, ver int) error {
	return h.saveDraft(ctx, "backlog", "confirm", d, nonce, ver, fmt.Sprintf("<b>Preview backlog</b>\nProject: %s\nJudul: %s\nQuadrant: %s\nDeadline: %s\nCatatan: %s", Escape(d["project_name"]), Escape(d["title"]), Escape(d["quadrant"]), Escape(d["deadline"]), Escape(d["notes"])), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Simpan", "v2:savebacklog:"+nonce)}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:cancel")}})
}

func (h *Handler) focus(ctx context.Context) error {
	now := time.Now().In(h.loc)
	items, err := h.backlog.Recommend(ctx, now, h.limit)
	if err != nil {
		return err
	}
	return h.bot.send(recommendation.Render(items, now), nil)
}
func (h *Handler) callbackV2(ctx context.Context, q *tgbotapi.CallbackQuery, updateID int64) error {
	p := strings.Split(q.Data, ":")
	if len(p) < 2 || p[0] != "v2" {
		return h.stale(ctx)
	}
	switch p[1] {
	case "menu":
		return h.bot.send("Pilih menu:", menu())
	case "cancel":
		_ = h.db.ClearState(ctx, h.userID)
		return h.bot.send("Draft dibatalkan.", menu())
	case "add":
		return h.startBacklog(ctx)
	case "help":
		return h.bot.send("Gunakan tombol menu untuk mengelola project dan backlog. /cancel membatalkan wizard.", menu())
	case "focus":
		return h.focus(ctx)
	case "projects":
		return h.projectsPage(ctx, pageValueAt(p, 2), boolAt(p, 3))
	case "project":
		if len(p) != 3 {
			return h.stale(ctx)
		}
		return h.projectDetail(ctx, p[2])
	case "list":
		return h.listPage(ctx, pageValueAt(p, 2), domain.ItemFilter{DeadlineBucket: -1})
	case "notificationitems":
		if len(p) != 3 {
			return h.stale(ctx)
		}
		return h.notificationItems(ctx, p[2])

	case "item":
		if len(p) != 3 {
			return h.stale(ctx)
		}
		return h.itemDetail(ctx, p[2])
	case "newproject":
		return h.startNestedProject(ctx)
	case "saveproject":
		return h.saveV2Project(ctx, p, updateID)
	case "savebacklog":
		return h.saveV2Backlog(ctx, p, updateID)
	case "pickproject":
		return h.pickV2Project(ctx, p)
	case "quadrant":
		return h.pickV2Quadrant(ctx, p)
	case "date":
		return h.pickV2Date(ctx, p)
	case "skip":
		return h.skipV2(ctx, p)
	case "archive", "restore", "complete", "reopen":
		if len(p) != 4 {
			return h.stale(ctx)
		}
		return h.actionV2(ctx, p[1], p, updateID)
	case "archiveconfirm", "restoreconfirm", "completeconfirm", "reopenconfirm":
		if len(p) != 5 {
			return h.stale(ctx)
		}
		expected, e := time.Parse(time.RFC3339Nano, p[4])
		if e != nil {
			return h.stale(ctx)
		}
		var result string
		switch p[1] {
		case "archiveconfirm":
			_, e = h.projectSvc.ArchiveWithMutationVersion(ctx, updateID, p[3], p[2], expected, h.now())
			result = "Project diarsipkan."
		case "restoreconfirm":
			_, e = h.projectSvc.RestoreWithMutationAndClearState(ctx, updateID, p[3], p[2], expected, h.now(), h.userID)

			result = "Project dipulihkan."
		case "completeconfirm", "reopenconfirm":
			if p[1] == "completeconfirm" {
				_, e = h.backlog.CompleteWithMutationVersion(ctx, updateID, p[3], p[2], expected, h.now())
				result = "Backlog ditandai selesai."
			} else {
				_, e = h.backlog.ReopenWithMutationVersion(ctx, updateID, p[3], p[2], expected, h.now())
				result = "Backlog dibuka kembali."
			}
		}
		if e != nil {
			if errors.Is(e, domain.ErrConflict) && p[1] == "restoreconfirm" {
				return h.bot.send("Nama project sudah dipakai. Pilih Rename atau batalkan.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Rename", "v2:editproject:"+p[2]+":name")}, {tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
			}
			return h.bot.send("Operasi ditolak karena data sudah berubah atau project archived.", menu())
		}
		return h.bot.send(result, menu())

	case "editproject", "edititem":
		return h.startEdit(ctx, p)
	case "editvalue":
		return h.editValueCallback(ctx, p)
	case "editdraft":
		return h.editDraftCallback(ctx, p)
	case "moveproject":
		return h.moveProjectCallback(ctx, p)

	case "saveedit":

		return h.saveEdit(ctx, p, updateID)
	case "projectitems":
		if len(p) != 4 {
			return h.stale(ctx)
		}
		return h.projectItems(ctx, p[2], pageValue(p[3]))
	case "filter":
		return h.filterMenu(ctx, p)
	case "listfilter":
		return h.listFilter(ctx, p)

	case "delete":
		return h.deleteV2(ctx, p)
	case "deleteconfirm":
		return h.deleteConfirmV2(ctx, p, updateID)
	}
	return h.stale(ctx)
}
func pageValueAt(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	return pageValue(p[i])
}
func boolAt(p []string, i int) bool { return i < len(p) && p[i] == "1" }
func (h *Handler) stale(ctx context.Context) error {
	return h.bot.send("Tombol sudah kedaluwarsa. Buka menu terbaru.", menu())
}
func (h *Handler) verifyDraft(ctx context.Context, flow, step, n string) (map[string]string, int, error) {
	f, s, raw, got, v, expires, err := h.db.GetState(ctx, h.userID)
	if err != nil || f != flow || s != step || got != n || time.Now().After(expires) {
		return nil, 0, domain.ErrConflict
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return nil, 0, domain.ErrConflict
	}
	return d, v, nil
}
func (h *Handler) skipV2(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	n := p[3]
	if d, v, err := h.verifyDraft(ctx, "project", "description", n); err == nil && p[2] == "desc" {
		d["description"] = ""
		return h.projectPreview(ctx, d, n, v+1)
	}
	if d, v, err := h.verifyDraft(ctx, "backlog", "notes", n); err == nil && p[2] == "notes" {
		d["notes"] = ""
		return h.backlogPreview(ctx, d, n, v+1)
	}
	return h.stale(ctx)
}
func (h *Handler) pickV2Project(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	d, v, e := h.verifyDraft(ctx, "backlog", "project", p[3])
	if e != nil {
		return h.stale(ctx)
	}
	pr, e := h.projectSvc.Get(ctx, p[2])
	if e != nil || pr.Status != domain.ProjectActive {
		return h.stale(ctx)
	}
	d["project_id"], d["project_name"] = pr.ID, pr.Name
	return h.chooseQuadrant(ctx, d, p[3], v+1)
}
func (h *Handler) pickV2Quadrant(ctx context.Context, p []string) error {
	if len(p) != 4 || !domain.ValidQuadrant(domain.Quadrant(p[2])) {
		return h.stale(ctx)
	}
	d, v, e := h.verifyDraft(ctx, "backlog", "quadrant", p[3])
	if e != nil {
		return h.stale(ctx)
	}
	d["quadrant"] = p[2]
	return h.askDeadline(ctx, d, p[3], v+1)
}
func (h *Handler) pickV2Date(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	d, v, e := h.verifyDraft(ctx, "backlog", "deadline", p[3])
	if e != nil {
		return h.stale(ctx)
	}
	now := h.now()
	switch p[2] {
	case "today":
		d["deadline"] = now.Format("2006-01-02")
	case "tomorrow":
		d["deadline"] = now.AddDate(0, 0, 1).Format("2006-01-02")
	case "week":
		d["deadline"] = now.AddDate(0, 0, 7).Format("2006-01-02")
	case "custom":
		return h.saveDraft(ctx, "backlog", "date", d, p[3], v+1, "Masukkan tanggal YYYY-MM-DD atau DD-MM-YYYY.", cancelKeys())
	default:
		return h.stale(ctx)
	}
	return h.askNotes(ctx, d, p[3], v+1)
}
func (h *Handler) saveV2Project(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 3 {
		return h.stale(ctx)
	}
	if receipt, e := h.db.Receipt(ctx, p[2]); e == nil && receipt != "" {
		return h.bot.send("Operasi sudah diproses.", menu())
	}
	flow, step, raw, n, _, _, e := h.db.GetState(ctx, h.userID)
	if e != nil || flow != "project" || step != "confirm" || n != p[2] {
		return h.stale(ctx)
	}
	var d map[string]string
	_ = json.Unmarshal([]byte(raw), &d)
	pr, _, e := h.projectSvc.CreateWithMutationAndClearState(ctx, updateID, n, d["name"], d["description"], h.now(), h.userID)
	if e != nil {
		return h.bot.send("Project gagal disimpan; nama mungkin sudah digunakan.", menu())
	}
	return h.bot.send("Project disimpan: <b>"+Escape(pr.Name)+"</b>", menu())
}
func (h *Handler) saveV2Backlog(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 3 {
		return h.stale(ctx)
	}
	if receipt, e := h.db.Receipt(ctx, p[2]); e == nil && receipt != "" {
		return h.bot.send("Operasi sudah diproses.", menu())
	}
	flow, step, raw, n, _, _, e := h.db.GetState(ctx, h.userID)
	if e != nil || flow != "backlog" || step != "confirm" || n != p[2] {
		return h.stale(ctx)
	}
	var d map[string]string
	_ = json.Unmarshal([]byte(raw), &d)
	_, _, e = h.backlog.CreateWithMutationAndClearState(ctx, updateID, n, d["project_id"], d["title"], d["notes"], domain.Quadrant(d["quadrant"]), d["deadline"], h.now(), h.userID)
	if e != nil {
		return e
	}
	return h.bot.send("Backlog disimpan.", menu())
}
func (h *Handler) projectDetail(ctx context.Context, id string) error {
	p, e := h.projectSvc.Get(ctx, id)
	if e != nil {
		return h.stale(ctx)
	}
	a, d, e := h.projectSvc.Counts(ctx, id)
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
	return h.bot.send(text, keys)
}
func (h *Handler) projectsPage(ctx context.Context, page int, archived bool) error {
	ps, e := h.projectSvc.ListPage(ctx, archived, 8, page*8)
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
	return h.bot.send(s.String(), keys)
}
func (h *Handler) notificationItems(ctx context.Context, date string) error {
	items, err := h.notifier.Items(ctx, date)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return h.bot.send("Tidak ada backlog tersnapshot untuk ditandai.", menu())
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
	return h.bot.send(text.String(), keys)
}

func (h *Handler) listPage(ctx context.Context, page int, filter domain.ItemFilter) error {
	filter.Today = h.now().Format("2006-01-02")
	items, e := h.backlog.ListPage(ctx, filter, 8, page*8)
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
	return h.bot.send(s.String(), keys)
}
func (h *Handler) projectItems(ctx context.Context, projectID string, page int) error {
	p, err := h.projectSvc.Get(ctx, projectID)
	if err != nil {
		return h.stale(ctx)
	}
	items, err := h.backlog.ListPage(ctx, domain.ItemFilter{ProjectID: projectID, IncludeArchived: true, DeadlineBucket: -1}, 8, page*8)
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
	return h.bot.send(s.String(), keys)
}
func (h *Handler) filterMenu(ctx context.Context, p []string) error {
	if len(p) < 3 {
		return h.stale(ctx)
	}
	switch p[2] {
	case "project":
		page := pageValueAt(p, 3)
		ps, e := h.projectSvc.ListPage(ctx, false, 8, page*8)
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
		return h.bot.send("Pilih project:", keys)
	case "status":
		return h.bot.send("Pilih status:", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Aktif", "v2:listfilter:status:active")}, {tgbotapi.NewInlineKeyboardButtonData("Selesai", "v2:listfilter:status:done")}})
	case "deadline":
		return h.bot.send("Pilih deadline:", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Terlambat", "v2:listfilter:deadline:0")}, {tgbotapi.NewInlineKeyboardButtonData("Hari ini", "v2:listfilter:deadline:1")}, {tgbotapi.NewInlineKeyboardButtonData("Mendatang", "v2:listfilter:deadline:2")}})
	case "quadrant":
		return h.bot.send("Pilih prioritas:", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Q1", "v2:listfilter:quadrant:q1")}, {tgbotapi.NewInlineKeyboardButtonData("Q2", "v2:listfilter:quadrant:q2")}, {tgbotapi.NewInlineKeyboardButtonData("Q3", "v2:listfilter:quadrant:q3")}, {tgbotapi.NewInlineKeyboardButtonData("Q4", "v2:listfilter:quadrant:q4")}})
	}
	return h.stale(ctx)
}
func (h *Handler) listFilter(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
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
		return h.stale(ctx)
	}
	if (f.Quadrant != "" && !domain.ValidQuadrant(f.Quadrant)) || (f.Status != "" && f.Status != domain.ItemActive && f.Status != domain.ItemDone) || f.DeadlineBucket < -1 || f.DeadlineBucket > 2 {
		return h.stale(ctx)
	}
	return h.listPage(ctx, 0, f)
}
func (h *Handler) startEdit(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	id, field := p[2], p[3]
	if p[1] == "editproject" {
		pr, err := h.projectSvc.Get(ctx, id)
		if err != nil {
			return h.stale(ctx)
		}
		d := map[string]string{"entity_id": id, "edit_field": field, "project_name": pr.Name, "description": pr.Description, "name": pr.Name, "expected_updated": pr.UpdatedAt.Format(time.RFC3339Nano)}
		n := callbackNonce()
		return h.saveDraft(ctx, "edit", "value", d, n, 1, "Pilih field yang akan diubah.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Nama", "v2:editvalue:name:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Deskripsi", "v2:editvalue:description:"+n)}, cancelKeys()[0]})

	}
	i, err := h.backlog.Get(ctx, id)
	if err != nil {
		return h.stale(ctx)
	}
	pr, err := h.projectSvc.Get(ctx, i.ProjectID)
	if err != nil || pr.Status == domain.ProjectArchived || i.Status == domain.ItemDone {
		return h.stale(ctx)
	}
	d := map[string]string{"entity_id": id, "edit_field": field, "title": i.Title, "notes": i.Notes, "deadline": i.DeadlineDate, "quadrant": string(i.Quadrant), "project_id": i.ProjectID, "project_name": pr.Name, "expected_updated": i.UpdatedAt.Format(time.RFC3339Nano)}
	n := callbackNonce()
	return h.saveDraft(ctx, "edit", "value", d, n, 1, "Pilih field yang akan diubah.", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Judul", "v2:editvalue:title:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Prioritas", "v2:editvalue:quadrant:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Deadline", "v2:editvalue:deadline:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Catatan", "v2:editvalue:notes:"+n)}, {tgbotapi.NewInlineKeyboardButtonData("Pindah Project", "v2:moveproject:"+n)}, cancelKeys()[0]})
}
func (h *Handler) editText(ctx context.Context, d map[string]string, text, n string, v int) error {
	field := d["edit_field"]
	switch field {
	case "name":
		if e := domain.ValidateProjectName(text); e != nil {
			return h.bot.send("Nama project wajib 1–80 karakter.", cancelKeys())
		}
	case "description":
		if e := domain.ValidateDescription(text); e != nil {
			return h.bot.send("Deskripsi maksimal 500 karakter.", cancelKeys())
		}
	case "title":
		if e := domain.ValidateTitle(text); e != nil {
			return h.bot.send("Judul wajib 1–160 karakter.", cancelKeys())
		}
	case "notes":
		if e := domain.ValidateNotes(text); e != nil {
			return h.bot.send("Catatan maksimal 2.000 karakter.", cancelKeys())
		}
	case "deadline":
		x, e := domain.ParseEditableDeadline(text, h.now())
		if e != nil {
			return h.bot.send("Tanggal tidak valid.", cancelKeys())
		}
		d[field] = x
	}
	if field != "deadline" {
		d[field] = domain.NormalizeText(text)
	}
	delete(d, "edit_field")
	return h.saveDraft(ctx, "edit", "confirm", d, n, v+1, editPreview(d), editSelectors(d, n))
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

func (h *Handler) editDraftCallback(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	field, n := p[2], p[3]
	if field != "name" && field != "description" && field != "title" && field != "notes" && field != "deadline" && field != "quadrant" && field != "project" {
		return h.stale(ctx)
	}
	f, s, raw, got, v, ex, err := h.db.GetState(ctx, h.userID)
	if err != nil || f != "edit" || s != "confirm" || got != n || time.Now().After(ex) {
		return h.stale(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return h.stale(ctx)
	}
	d["edit_field"] = field
	return h.saveDraft(ctx, "edit", "input", d, n, v+1, "Masukkan nilai baru.", cancelKeys())
}

func (h *Handler) moveProjectCallback(ctx context.Context, p []string) error {
	if len(p) != 3 {
		return h.stale(ctx)
	}
	f, s, raw, n, _, ex, err := h.db.GetState(ctx, h.userID)
	if err != nil || f != "edit" || s != "value" || n != p[2] || time.Now().After(ex) {
		return h.stale(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil || d["project_id"] == "" {
		return h.stale(ctx)
	}
	projects, err := h.projectSvc.List(ctx, false)
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
	return h.bot.send("Pilih project baru.", keys)
}

func (h *Handler) editValueCallback(ctx context.Context, p []string) error {
	if len(p) != 4 && len(p) != 5 {
		return h.stale(ctx)
	}
	n := p[len(p)-1]
	f, s, raw, _, v, ex, e := h.db.GetState(ctx, h.userID)
	if e != nil || f != "edit" || s != "value" || n != p[len(p)-1] || time.Now().After(ex) {
		return h.stale(ctx)
	}
	var d map[string]string
	if json.Unmarshal([]byte(raw), &d) != nil {
		return h.stale(ctx)
	}
	if len(p) == 5 && p[2] == "project" {
		project, err := h.projectSvc.Get(ctx, p[3])
		if err != nil || project.Status != domain.ProjectActive {
			return h.stale(ctx)
		}
		d["project_id"], d["project_name"] = project.ID, project.Name
		d["edit_field"] = "project"
		return h.editText(ctx, d, project.Name, n, v)
	}
	d["edit_field"] = p[2]
	return h.saveDraft(ctx, "edit", "input", d, n, v+1, "Masukkan nilai baru.", cancelKeys())
}
func (h *Handler) saveEdit(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 3 {
		return h.stale(ctx)
	}
	f, s, raw, n, _, _, e := h.db.GetState(ctx, h.userID)
	if e != nil || f != "edit" || s != "confirm" || n != p[2] {
		return h.stale(ctx)
	}
	var d map[string]string
	_ = json.Unmarshal([]byte(raw), &d)
	id := d["entity_id"]
	if d["project_id"] != "" {
		i, e := h.backlog.Get(ctx, id)
		if e != nil {
			return h.stale(ctx)
		}
		pr, e := h.projectSvc.Get(ctx, i.ProjectID)
		if e != nil {
			return h.stale(ctx)
		}
		i.ProjectID = d["project_id"]
		i.Title, i.Notes, i.DeadlineDate, i.Quadrant = d["title"], d["notes"], d["deadline"], domain.Quadrant(d["quadrant"])
		expected, parseErr := time.Parse(time.RFC3339Nano, d["expected_updated"])
		if parseErr != nil {
			return h.stale(ctx)
		}
		_, e = h.backlog.UpdateWithMutationAndClearState(ctx, updateID, n, i, pr, expected, h.now(), h.userID)
		if e != nil {
			return h.bot.send("Perubahan ditolak karena data sudah berubah.", menu())
		}

	} else {
		pr, e := h.projectSvc.Get(ctx, id)
		if e != nil {
			return h.stale(ctx)
		}
		expected, parseErr := time.Parse(time.RFC3339Nano, d["expected_updated"])
		if parseErr != nil {
			return h.stale(ctx)
		}
		_, _, e = h.projectSvc.UpdateWithMutationAndClearState(ctx, updateID, n, pr, d["name"], d["description"], expected, h.now(), h.userID)
		if e != nil {
			return h.bot.send("Perubahan project ditolak.", menu())
		}
	}
	return h.bot.send("Perubahan disimpan.", menu())
}
func (h *Handler) itemDetail(ctx context.Context, id string) error {
	i, e := h.backlog.Get(ctx, id)
	if e != nil {
		return h.stale(ctx)
	}
	p, e := h.projectSvc.Get(ctx, i.ProjectID)
	if e != nil {
		return h.stale(ctx)
	}
	archived := p.Status == domain.ProjectArchived
	text := fmt.Sprintf("<b>%s</b>\nProject: %s\nQuadrant: %s\nDeadline: %s\nStatus: %s\nCatatan: %s", Escape(i.Title), Escape(p.Name), Escape(string(i.Quadrant)), Escape(i.DeadlineDate), Escape(string(i.Status)), Escape(i.Notes))
	keys := [][]tgbotapi.InlineKeyboardButton{}
	if archived {
		if i.Status == domain.ItemActive {
			keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "✅ Tandai Selesai", CallbackData: strPtr("v2:complete:" + id + ":" + callbackNonce())}})
		}
		keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Kembali ke project", "v2:project:"+p.ID)})
		return h.bot.send(text+"\n\nProject archived: detail read-only; hanya penyelesaian diizinkan.", keys)
	}
	if i.Status == domain.ItemActive {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "✅ Tandai Selesai", CallbackData: strPtr("v2:complete:" + id + ":" + callbackNonce())}})
	} else {
		keys = append(keys, []tgbotapi.InlineKeyboardButton{{Text: "Buka Kembali", CallbackData: strPtr("v2:reopen:" + id + ":" + callbackNonce())}})
	}
	keys = append(keys, []tgbotapi.InlineKeyboardButton{tgbotapi.NewInlineKeyboardButtonData("Edit", "v2:edititem:"+id+":all"), tgbotapi.NewInlineKeyboardButtonData("Hapus", "v2:delete:"+id+":"+callbackNonce())}, menu()[1])
	return h.bot.send(text, keys)
}
func (h *Handler) actionV2(ctx context.Context, action string, p []string, updateID int64) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	id, n := p[2], p[3]
	switch action {
	case "archive":
		pr, e := h.projectSvc.Get(ctx, id)
		if e != nil {
			return h.stale(ctx)
		}
		return h.bot.send(fmt.Sprintf("<b>Preview arsip project</b>\nNama: %s\nBacklog tetap tersimpan dan menjadi read-only.", Escape(pr.Name)), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Ya, arsipkan", "v2:archiveconfirm:"+id+":"+n+":"+pr.UpdatedAt.Format(time.RFC3339Nano)), tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
	case "restore":
		pr, e := h.projectSvc.Get(ctx, id)
		if e != nil {
			return h.stale(ctx)
		}
		return h.bot.send(fmt.Sprintf("<b>Preview pulihkan project</b>\nNama: %s", Escape(pr.Name)), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Ya, pulihkan", "v2:restoreconfirm:"+id+":"+n+":"+pr.UpdatedAt.Format(time.RFC3339Nano)), tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
	case "complete", "reopen":
		i, e := h.backlog.Get(ctx, id)
		if e != nil {
			return h.stale(ctx)
		}
		label := map[string]string{"complete": "Tandai selesai", "reopen": "Buka kembali"}[action]
		return h.bot.send(fmt.Sprintf("<b>Preview perubahan</b>\nItem: %s\nAksi: %s", Escape(i.Title), label), [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Konfirmasi", "v2:"+action+"confirm:"+id+":"+n+":"+i.UpdatedAt.Format(time.RFC3339Nano)), tgbotapi.NewInlineKeyboardButtonData("Batal", "v2:menu")}})
	}
	return h.stale(ctx)
}
func (h *Handler) deleteV2(ctx context.Context, p []string) error {
	if len(p) != 4 {
		return h.stale(ctx)
	}
	i, e := h.backlog.Get(ctx, p[2])
	if e != nil {
		return h.stale(ctx)
	}
	return h.bot.send("Hapus item ini permanen?", [][]tgbotapi.InlineKeyboardButton{{tgbotapi.NewInlineKeyboardButtonData("Ya, hapus permanen", "v2:deleteconfirm:"+p[2]+":"+p[3]+":"+i.UpdatedAt.Format(time.RFC3339Nano))}, menu()[1]})
}
func (h *Handler) deleteConfirmV2(ctx context.Context, p []string, updateID int64) error {
	if len(p) != 5 {
		return h.stale(ctx)
	}
	expected, e := time.Parse(time.RFC3339Nano, p[4])
	if e != nil {
		return h.stale(ctx)
	}
	_, e = h.backlog.DeleteWithMutationAndClearState(ctx, updateID, p[3], p[2], expected, h.userID)
	if e != nil {
		return h.bot.send("Penghapusan ditolak; project mungkin archived atau item sudah berubah.", menu())
	}
	return h.bot.send("Backlog dihapus permanen.", menu())
}
