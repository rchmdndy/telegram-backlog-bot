package telegram

import (
	"html"
	"strings"
	"unicode/utf16"
)

func Escape(s string) string { return html.EscapeString(s) }
func utf16Len(s string) int  { return len(utf16.Encode([]rune(s))) }
func LimitMessage(s string, max int) []string {
	if max <= 0 || utf16Len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > 0 {
		cut := s
		if utf16Len(cut) > max {
			cut = cut[:utf16Cut(cut, max)]
		}
		if i := strings.LastIndex(cut, "\n\n"); i > 0 {
			cut = cut[:i]
		}
		out = append(out, cut)
		s = strings.TrimPrefix(s[len(cut):], "\n\n")
	}
	return out
}

func utf16Cut(s string, max int) int {
	units := 0
	for i, r := range s {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > max {
			return i
		}
		units += n
	}
	return len(s)
}
