package imap

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeSubject(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "Re: ping", "Re- ping"},
		{"slash", "budget/q2", "budget-q2"},
		{"backslash", `a\b`, "a-b"},
		{"colon", "file: report.pdf", "file- report.pdf"},
		{"star", "a*b", "a-b"},
		{"question", "a?b", "a-b"},
		{"double_quote", `say "hi"`, "say -hi"},
		{"lt_gt", "a<b>c", "a-b-c"},
		{"pipe", "a|b", "a-b"},
		{"whitespace_only", "   ", "no-subject"},
		{"empty", "", "no-subject"},
		{"collapse_runs", "a///b", "a-b"},
		{"trim_leading_trailing", "--hello--", "hello"},
		{"trim_dots_spaces", " . hello . ", "hello"},
		{"preserve_unicode", "Spec — naming / sanitization", "Spec — naming - sanitization"},
		{"preserve_spaces_case", "Hello World", "Hello World"},
		{"control_char", "a\x01b", "a-b"},
		{"tab", "a\tb", "a-b"},
		{"newline", "a\nb", "a-b"},
		{"del", "a\x7fb", "a-b"},
		{"null", "a\x00b", "a-b"},
		{"only_forbidden", "///", "no-subject"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeSubject(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeSubject(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeSubjectTruncates100Bytes(t *testing.T) {
	in := strings.Repeat("a", 150)
	got := sanitizeSubject(in)
	if len(got) != 100 {
		t.Errorf("len = %d; want 100", len(got))
	}
}

func TestSanitizeSubjectUTF8SafeTruncation(t *testing.T) {
	// Each "あ" is 3 bytes in UTF-8. 34 copies = 102 bytes → must cut to
	// whole codepoints, not mid-sequence.
	in := strings.Repeat("あ", 34)
	got := sanitizeSubject(in)
	if len(got) > 100 {
		t.Errorf("len = %d; want ≤ 100", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("result not valid UTF-8: %q", got)
	}
	// 33 codepoints × 3 bytes = 99 bytes is the largest multiple fit.
	want := strings.Repeat("あ", 33)
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

func TestSanitizeSubjectTruncationReTrims(t *testing.T) {
	// 100-byte truncation lands on a '-'; a second trim must remove it.
	in := strings.Repeat("a", 99) + "-bbbbb"
	got := sanitizeSubject(in)
	if strings.HasSuffix(got, "-") {
		t.Errorf("result has trailing '-' after truncation: %q", got)
	}
	if len(got) != 99 {
		t.Errorf("len = %d; want 99 (trailing '-' trimmed)", len(got))
	}
}
