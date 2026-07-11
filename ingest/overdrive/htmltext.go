package overdrive

import (
	"html"
	"strings"
)

// htmlText renders an OverDrive description as plain text fit for
// bf:summary: block boundaries become blank lines, <br> a line break,
// inline tags drop, entities decode, and whitespace normalizes to at most
// one blank line between paragraphs. The qllpoc Thunder page cache shows
// three encoding shapes in the wild -- a plain HTML fragment,
// HTML with double-escaped entities ("&amp;#160;"), and a fully
// entity-escaped fragment whose tags only appear after decoding -- so
// stripping and unescaping run to a fixpoint (bounded; observed depth ≤ 2).
func htmlText(s string) string {
	text := s
	for range 3 {
		next := html.UnescapeString(stripTags(text))
		if next == text {
			break
		}
		text = next
	}
	return normalizeText(text)
}

// stripTags drops HTML tags, writing paragraph/line separators at block
// boundaries per tagBreak. A '<' that does not open a tag ("3 < 5") stays
// literal text; an unterminated trailing tag start is a feed truncation
// artifact ("...&#8212;Author<BR" cut mid-tag) and drops.
func stripTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				b.WriteString(s[i:])
				break
			}
			b.WriteString(s[i : i+j])
			i += j
			continue
		}
		if !tagStart(s[i:]) {
			b.WriteByte('<')
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			break
		}
		b.WriteString(tagBreak(s[i+1 : i+end]))
		i += end + 1
	}
	return b.String()
}

// tagStart reports whether s (beginning at '<') opens an HTML tag: the next
// character is a tag-name letter, optionally after a closing slash.
func tagStart(s string) bool {
	rest := s[1:]
	rest = strings.TrimPrefix(rest, "/")
	return rest != "" && (rest[0] >= 'a' && rest[0] <= 'z' || rest[0] >= 'A' && rest[0] <= 'Z')
}

// tagBreak returns the plain-text separator a tag boundary contributes: a
// blank line for paragraph-level blocks (open or close), a newline for line
// breaks and at the start of list items and rows, nothing for inline
// elements (a closing <li>/<tr> stays silent so adjacent items read as
// consecutive lines, not paragraphs).
func tagBreak(tag string) string {
	closing := strings.HasPrefix(tag, "/")
	name := strings.ToLower(strings.TrimLeft(tag, "/"))
	if j := strings.IndexAny(name, " \t\n/"); j >= 0 {
		name = name[:j]
	}
	switch name {
	case "p", "div", "ul", "ol", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6":
		return "\n\n"
	case "br":
		return "\n"
	case "li", "tr":
		if closing {
			return ""
		}
		return "\n"
	}
	return ""
}

// normalizeText collapses intra-line whitespace runs (including no-break
// spaces) to single spaces and blank-line runs to one blank line, trimming
// the ends.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.ReplaceAll(s, "\r", "")
	var out []string
	pendingBlank := false
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			pendingBlank = len(out) > 0
			continue
		}
		if pendingBlank {
			out = append(out, "")
			pendingBlank = false
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
