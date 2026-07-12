package bibframe

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// TestAttachments covers statements add and retract editorially
// with the describes-guard and filename validation; clones drop them.
func TestAttachments(t *testing.T) {
	grain := sampleGrain(t) // describes w1
	grain, err := SetAttachment(grain, "w1", "invoice-2026.pdf", true)
	if err != nil {
		t.Fatal(err)
	}
	grain, err = SetAttachment(grain, "w1", "cover-scan.png", true)
	if err != nil {
		t.Fatal(err)
	}
	names, err := AttachmentsOf(grain, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"cover-scan.png", "invoice-2026.pdf"}) {
		t.Fatalf("attachments = %v", names)
	}

	grain, err = SetAttachment(grain, "w1", "cover-scan.png", false)
	if err != nil {
		t.Fatal(err)
	}
	names, _ = AttachmentsOf(grain, "w1")
	if !reflect.DeepEqual(names, []string{"invoice-2026.pdf"}) {
		t.Fatalf("after remove = %v", names)
	}

	// Guards: phantom work, hostile names.
	if _, err := SetAttachment(grain, "wzzz999zzz999z", "x.pdf", true); err == nil {
		t.Fatal("undescribed work accepted an attachment")
	}
	for _, bad := range []string{"", ".", "..", "a/b.pdf", "a\\b.pdf", strings.Repeat("x", 101)} {
		if ValidAttachmentName(bad) {
			t.Fatalf("name %q validated", bad)
		}
	}
	// Every script is a legal display name; so are spaces and
	// leading dots, which the encoding -- not the name -- makes path-safe.
	for _, good := range []string{"a.pdf", "scan_01-final.PNG", "1", "文書.pdf", "Тест.pdf", "con sole.txt", ".hidden"} {
		if !ValidAttachmentName(good) {
			t.Fatalf("name %q rejected", good)
		}
	}

	// Clones leave attachments with the source (lcat statements drop).
	cloned, _, err := CloneGrain(grain, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cloned), "invoice-2026.pdf") {
		t.Fatalf("clone carried an attachment:\n%s", cloned)
	}

	// Blob path shards like grains, and the safe-character case stays legible.
	got, err := AttachmentBlobPath("wabc123", "a.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if got != "data/attachments/wa/wabc123/aa.pdf" {
		t.Fatalf("blob path = %q", got)
	}
}

// TestAttachmentSegmentIsInjective covers two different filenames
// must never address the same bytes. The non-Latin cases are the ones that
// collapsed onto a shared "pdf" before; "文" vs "x文" is the case a variable
// leading-prefix scheme would still have collided.
func TestAttachmentSegmentIsInjective(t *testing.T) {
	names := []string{
		"文書.pdf", "資料.pdf", "報告書.pdf", "Тест.pdf", "صورة.jpg",
		"文", "x文", "x_5F", "x", "_", "__", "a.pdf", "a-pdf", "a_pdf",
		"scan.pdf", "scan_01-final.PNG", "con sole.txt", ".hidden", "..hidden",
		"Acquisition invoice (2024).pdf", "Acquisition-invoice-2024-.pdf",
	}
	seen := map[string]string{}
	for _, name := range names {
		seg, err := AttachmentSegment(name)
		if err != nil {
			t.Fatalf("AttachmentSegment(%q) = %v", name, err)
		}
		if !attachmentSegment.MatchString(seg) {
			t.Errorf("AttachmentSegment(%q) = %q, not a safe segment", name, seg)
		}
		if prev, dup := seen[seg]; dup {
			t.Errorf("collision: %q and %q both encode to %q", prev, name, seg)
		}
		seen[seg] = name
	}
	if _, err := AttachmentSegment("../../grain"); err == nil {
		t.Fatal("a traversal name encoded to a segment")
	}
}

// FuzzAttachmentSegment proves injectivity constructively rather than by
// example: every valid display name encodes to a safe segment that decodes
// back to exactly that name. A decodable encoding cannot collide, which is
// the property needed and the old sanitizer lacked.
func FuzzAttachmentSegment(f *testing.F) {
	for _, seed := range []string{"a.pdf", "文書.pdf", "x文", "_", "..hidden", "con sole.txt", "Тест.pdf"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if !ValidAttachmentName(name) {
			return
		}
		seg, err := AttachmentSegment(name)
		if err != nil {
			t.Fatalf("AttachmentSegment(%q) = %v", name, err)
		}
		if !attachmentSegment.MatchString(seg) {
			t.Fatalf("AttachmentSegment(%q) = %q, not a safe segment", name, seg)
		}
		if got := decodeAttachmentSegment(t, seg); got != name {
			t.Fatalf("segment %q decoded to %q, want %q", seg, got, name)
		}
	})
}

// decodeAttachmentSegment inverts AttachmentSegment. It exists only to prove
// the encoding is lossless; production never needs it, because the grain
// carries the display name.
func decodeAttachmentSegment(t *testing.T, seg string) string {
	t.Helper()
	if seg == "" || seg[0] != 'a' {
		t.Fatalf("segment %q lacks the constant prefix", seg)
	}
	var out []byte
	for i := 1; i < len(seg); {
		if seg[i] != '_' {
			out = append(out, seg[i])
			i++
			continue
		}
		if i+2 >= len(seg) {
			t.Fatalf("segment %q has a truncated escape", seg)
		}
		var b byte
		if _, err := fmt.Sscanf(seg[i+1:i+3], "%02X", &b); err != nil {
			t.Fatalf("segment %q has a bad escape: %v", seg, err)
		}
		out = append(out, b)
		i += 3
	}
	return string(out)
}

// TestLegacyAttachmentBlobPath covers the read fallback: attachments stored
// under the old layout used the display name as the segment, and must not be
// orphaned by the new encoding.
func TestLegacyAttachmentBlobPath(t *testing.T) {
	if got := LegacyAttachmentBlobPath("wabc123", "scan.pdf"); got != "data/attachments/wa/wabc123/scan.pdf" {
		t.Fatalf("legacy path = %q", got)
	}
	// A name that could never have been a pre-236 segment has no legacy home.
	for _, name := range []string{"文書.pdf", ".hidden", "con sole.txt"} {
		if got := LegacyAttachmentBlobPath("wabc123", name); got != "" {
			t.Errorf("LegacyAttachmentBlobPath(%q) = %q, want none", name, got)
		}
	}
}
