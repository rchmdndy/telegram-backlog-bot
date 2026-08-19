package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type ProjectStatus string

const (
	ProjectActive   ProjectStatus = "active"
	ProjectArchived ProjectStatus = "archived"
)

type ItemStatus string

const (
	ItemActive ItemStatus = "active"
	ItemDone   ItemStatus = "done"
)

type Quadrant string

const (
	Q1 Quadrant = "q1"
	Q2 Quadrant = "q2"
	Q3 Quadrant = "q3"
	Q4 Quadrant = "q4"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("state conflict")
	ErrInvalid  = errors.New("invalid input")
	ErrArchived = errors.New("project is archived")
	ErrDone     = errors.New("item is completed")
)

type Project struct {
	ID, Name, Description string
	NormalizedName        string
	Status                ProjectStatus
	CreatedAt, UpdatedAt  time.Time
	ArchivedAt            *time.Time
}
type BacklogItem struct {
	ID, ProjectID, Title, Notes, DeadlineDate string
	Quadrant                                  Quadrant
	Status                                    ItemStatus
	CreatedAt, UpdatedAt                      time.Time
	CompletedAt                               *time.Time
}
type RecommendationItem struct {
	Item        BacklogItem
	ProjectName string
	Ordinal     int
}
type ItemFilter struct {
	ProjectID       string
	Quadrant        Quadrant
	Status          ItemStatus
	DeadlineBucket  int    // -1 means any; 0 overdue, 1 today, 2 future
	Today           string // local YYYY-MM-DD used for deadline comparisons
	IncludeArchived bool
}

func NewID() string { return uuid.NewString() }
func NormalizeText(s string) string {
	return norm.NFC.String(strings.TrimSpace(strings.ToValidUTF8(s, "�")))
}
func ValidateProjectName(s string) error { return validateRequired(s, 80, "project name") }
func ValidateDescription(s string) error { return validateOptional(s, 500, "description") }
func ValidateTitle(s string) error       { return validateRequired(s, 160, "title") }
func ValidateNotes(s string) error       { return validateOptional(s, 2000, "notes") }
func validateRequired(s string, max int, label string) error {
	s = NormalizeText(s)
	if s == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, label)
	}
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%w: %s must be at most %d characters", ErrInvalid, label, max)
	}
	return nil
}
func validateOptional(s string, max int, label string) error {
	s = NormalizeText(s)
	if utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%w: %s must be at most %d characters", ErrInvalid, label, max)
	}
	return nil
}
func NormalizeProjectName(s string) string { return cases.Fold().String(NormalizeText(s)) }
func ValidQuadrant(q Quadrant) bool        { return q == Q1 || q == Q2 || q == Q3 || q == Q4 }
func ParseDeadline(input string, now time.Time) (string, error) {
	input = NormalizeText(input)
	for _, layout := range []string{"2006-01-02", "02-01-2006"} {
		if d, err := time.ParseInLocation(layout, input, now.Location()); err == nil {
			if d.Format("2006-01-02") < now.Format("2006-01-02") {
				return "", fmt.Errorf("%w: deadline cannot be in the past", ErrInvalid)
			}
			return d.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("%w: deadline must use YYYY-MM-DD or DD-MM-YYYY", ErrInvalid)
}
func ParseEditableDeadline(input string, now time.Time) (string, error) {
	input = NormalizeText(input)
	for _, layout := range []string{"2006-01-02", "02-01-2006"} {
		if d, err := time.ParseInLocation(layout, input, now.Location()); err == nil {
			return d.Format("2006-01-02"), nil
		}
	}
	return "", fmt.Errorf("%w: deadline must use YYYY-MM-DD or DD-MM-YYYY", ErrInvalid)
}
func DeadlineBucket(date, today string) int {
	if date < today {
		return 0
	}
	if date == today {
		return 1
	}
	return 2
}
func QuadrantRank(q Quadrant) int {
	switch q {
	case Q1:
		return 1
	case Q2:
		return 2
	case Q3:
		return 3
	case Q4:
		return 4
	}
	return 99
}
func SortRecommendations(items []RecommendationItem, today string) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Item, items[j].Item
		ak, bk := DeadlineBucket(a.DeadlineDate, today), DeadlineBucket(b.DeadlineDate, today)
		if ak != bk {
			return ak < bk
		}
		if QuadrantRank(a.Quadrant) != QuadrantRank(b.Quadrant) {
			return QuadrantRank(a.Quadrant) < QuadrantRank(b.Quadrant)
		}
		if a.DeadlineDate != b.DeadlineDate {
			return a.DeadlineDate < b.DeadlineDate
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}
func ValidateProject(p Project) error {
	if err := ValidateProjectName(p.Name); err != nil {
		return err
	}
	if err := ValidateDescription(p.Description); err != nil {
		return err
	}
	if p.Status != ProjectActive && p.Status != ProjectArchived {
		return fmt.Errorf("%w: invalid project status", ErrInvalid)
	}
	if (p.Status == ProjectArchived) != (p.ArchivedAt != nil) {
		return fmt.Errorf("%w: archive invariant violated", ErrInvalid)
	}
	return nil
}
func ValidateItem(i BacklogItem, now time.Time, allowPast bool) error {
	if err := ValidateTitle(i.Title); err != nil {
		return err
	}
	if err := ValidateNotes(i.Notes); err != nil {
		return err
	}
	if !ValidQuadrant(i.Quadrant) {
		return fmt.Errorf("%w: invalid quadrant", ErrInvalid)
	}
	if _, err := time.Parse("2006-01-02", i.DeadlineDate); err != nil {
		return fmt.Errorf("%w: invalid deadline", ErrInvalid)
	}
	if !allowPast && i.DeadlineDate < now.Format("2006-01-02") {
		return fmt.Errorf("%w: deadline cannot be in the past", ErrInvalid)
	}
	if i.Status != ItemActive && i.Status != ItemDone {
		return fmt.Errorf("%w: invalid item status", ErrInvalid)
	}
	if (i.Status == ItemDone) != (i.CompletedAt != nil) {
		return fmt.Errorf("%w: completion invariant violated", ErrInvalid)
	}
	return nil
}
func Complete(i *BacklogItem, now time.Time) error {
	if i.Status == ItemDone {
		return nil
	}
	if i.Status != ItemActive {
		return ErrConflict
	}
	i.Status = ItemDone
	i.CompletedAt = &now
	i.UpdatedAt = now
	return nil
}
func Reopen(i *BacklogItem, project Project, now time.Time) error {
	if project.Status != ProjectActive {
		return ErrArchived
	}
	if i.Status == ItemActive {
		return nil
	}
	i.Status = ItemActive
	i.CompletedAt = nil
	i.UpdatedAt = now
	return nil
}
