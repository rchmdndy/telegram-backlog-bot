package recommendation

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/rchmdndy/telegram-backlog-bot/internal/domain"
)

func Select(items []domain.RecommendationItem, now time.Time, limit int) []domain.RecommendationItem {
	if limit < 0 {
		limit = 0
	}
	domain.SortRecommendations(items, now.Format("2006-01-02"))
	if len(items) > limit {
		items = items[:limit]
	}
	for i := range items {
		items[i].Ordinal = i + 1
	}
	return items
}

// Group keeps global ordinals while ordering projects by their best-ranked item.
func Group(items []domain.RecommendationItem) [][]domain.RecommendationItem {
	groups := make(map[string][]domain.RecommendationItem)
	first := make(map[string]int)
	for _, item := range items {
		groups[item.ProjectName] = append(groups[item.ProjectName], item)
		if _, ok := first[item.ProjectName]; !ok {
			first[item.ProjectName] = item.Ordinal
		}
	}
	projects := make([]string, 0, len(groups))
	for project := range groups {
		projects = append(projects, project)
	}
	sort.SliceStable(projects, func(i, j int) bool { return first[projects[i]] < first[projects[j]] })
	out := make([][]domain.RecommendationItem, 0, len(projects))
	for _, project := range projects {
		out = append(out, groups[project])
	}
	return out
}

func Render(items []domain.RecommendationItem, now time.Time) string {
	var b strings.Builder
	b.WriteString("☀️ <b>Selamat pagi! Fokus hari ini</b>\n")
	b.WriteString(html.EscapeString(now.Format("02 January 2006")))
	b.WriteString(fmt.Sprintf(" · %d backlog dipilih\n\n", len(items)))
	if len(items) == 0 {
		return b.String() + "Belum ada backlog aktif yang direncanakan.\nTambahkan backlog atau buka daftar project untuk mulai.\n"
	}
	for _, group := range Group(items) {
		b.WriteString("<b>📁 " + html.EscapeString(group[0].ProjectName) + "</b>\n")
		for _, r := range group {
			b.WriteString(fmt.Sprintf("%d. %s %s\n   %s\n", r.Ordinal, quadrantIcon(r.Item.Quadrant), html.EscapeString(r.Item.Title), deadlineLabel(r.Item.DeadlineDate, now)))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func RenderParts(items []domain.RecommendationItem, now time.Time, max int) []string {
	if max <= 0 {
		return nil
	}
	groups := Group(items)
	if len(groups) == 0 {
		return []string{Render(items, now)}
	}
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		candidate := Render(group, now)
		if utf16Len(candidate) <= max {
			parts = append(parts, candidate)
			continue
		}
		for _, item := range group {
			one := Render([]domain.RecommendationItem{item}, now)
			if len([]rune(one)) <= max {
				parts = append(parts, one)
			} else {
				parts = append(parts, LimitText(one, max)...)
			}
		}
	}
	return parts
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func LimitText(s string, max int) []string {
	if max <= 0 {
		return nil
	}
	runes := []rune(s)
	var out []string
	for len(runes) > 0 {
		n := max
		if len(runes) < n {
			n = len(runes)
		}
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}

func quadrantIcon(q domain.Quadrant) string {
	switch q {
	case domain.Q1:
		return "🔴"
	case domain.Q2:
		return "🔵"
	case domain.Q3:
		return "🟠"
	default:
		return "⚪"
	}
}
func deadlineLabel(date string, now time.Time) string {
	today := now.Format("2006-01-02")
	if date < today {
		return "⚠️ Terlambat"
	}
	if date == today {
		return "📅 Hari ini"
	}
	t, _ := time.ParseInLocation("2006-01-02", date, now.Location())
	return "📅 " + t.Format("02 Jan")
}
