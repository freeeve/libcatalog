package overdrive

import (
	"strings"
	"testing"
)

// TestHTMLText covers the shapes measured over the qllpoc Thunder page cache
// : <p>/<br> blocks, <b>/<i>/<strong> inline runs, named and
// numeric entities, and whitespace debris.
func TestHTMLText(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain text passes through": {"Just words.", "Just words."},
		"empty":                     {"", ""},
		"inline tags drop": {
			"<p><b>From</b> <i><b>USA Today</b></i> <b>bestselling author</b></p>",
			"From USA Today bestselling author",
		},
		"paragraphs become blank lines": {
			"<p>First para.</p><p>Second para.</p>",
			"First para.\n\nSecond para.",
		},
		"empty paragraph collapses": {
			"<p>One.</p><p></p><p>Two.</p>",
			"One.\n\nTwo.",
		},
		"br is a single line break": {
			"<p><i>Every story begins somewhere.</i><br />On the banks of the river.</p>",
			"Every story begins somewhere.\nOn the banks of the river.",
		},
		"entities decode": {
			"Ya-l&#233; River&#8212;a &ldquo;quiet&rdquo; place&nbsp;here",
			"Ya-lé River—a “quiet” place here",
		},
		"list items break lines": {
			"<ul><li>one</li><li>two</li></ul>",
			"one\ntwo",
		},
		"attributes and self-closing tags": {
			`<p class="x">a</p><br/><P>b</P>`,
			"a\n\nb",
		},
		"stray angle bracket is text": {"3 < 5 and 7 > 2", "3 < 5 and 7 > 2"},
		"newline entity normalizes":   {"a&#10;&#10;&#10;b", "a\n\nb"},
		"truncated trailing tag drops": {
			`"Funny, smart" &#8212;Abby Jimenez<BR`,
			`"Funny, smart" —Abby Jimenez`,
		},
		"double-escaped entities decode": {
			"Sawkill Girls meets&amp;#160;The Hazel Wood&amp;#8212;a debut",
			"Sawkill Girls meets The Hazel Wood—a debut",
		},
		"fully escaped fragment decodes and strips": {
			"&lt;p style=&quot;text-align:center&quot;&gt;&lt;strong&gt;«En 1969 yo te quise.»&lt;/strong&gt;&lt;/p&gt;&lt;p&gt;Second.&lt;/p&gt;",
			"«En 1969 yo te quise.»\n\nSecond.",
		},
	} {
		if got := htmlText(tc.in); got != tc.want {
			t.Errorf("%s: htmlText(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}

// FuzzHTMLText asserts the normalizer's invariants: no panics, no carriage
// returns, no runs of more than one blank line, no leading/trailing space.
func FuzzHTMLText(f *testing.F) {
	for _, seed := range []string{
		"<p>a</p><p>b</p>", "<b>x</b>&#8212;y", "a<br/>b", "< p >", "&nbsp;", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := htmlText(in)
		if strings.Contains(out, "\r") || strings.Contains(out, "\n\n\n") {
			t.Errorf("unnormalized output %q", out)
		}
		if out != strings.TrimSpace(out) {
			t.Errorf("untrimmed output %q", out)
		}
	})
}
