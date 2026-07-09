package bibframe

import (
	"reflect"
	"strings"
	"testing"
)

// TestAttachments covers tasks/229: statements add and retract editorially
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
	for _, bad := range []string{"", "..", ".hidden", "a/b.pdf", "a\\b.pdf", "con sole.txt", strings.Repeat("x", 101)} {
		if ValidAttachmentName(bad) {
			t.Fatalf("name %q validated", bad)
		}
	}
	for _, good := range []string{"a.pdf", "scan_01-final.PNG", "1"} {
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

	// Blob path shards like grains.
	if got := AttachmentBlobPath("wabc123", "a.pdf"); got != "data/attachments/wa/wabc123/a.pdf" {
		t.Fatalf("blob path = %q", got)
	}
}
