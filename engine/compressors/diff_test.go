package compressors

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestDiffKeepsMetadata(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, tc := range []struct{ name, metadata string }{
			{"no-newline", " final line without a newline\n\\ No newline at end of file"},
			{"rename", "diff --git a/old.txt b/new.txt\nsimilarity index 100%\nrename from old.txt\nrename to new.txt"},
			{"copy", "diff --git a/old.txt b/copy.txt\nsimilarity index 100%\ncopy from old.txt\ncopy to copy.txt"},
			{"mode", "diff --git a/app b/app\nold mode 100644\nnew mode 100755"},
			{"rewrite", "diff --git a/app.txt b/app.txt\ndissimilarity index 100%\nold mode 100644\nnew mode 100755"},
			{"delete", "diff --git a/old.txt b/old.txt\ndeleted file mode 100644\nindex 111..000\nBinary files a/old.txt and /dev/null differ"},
			{"create", "diff --git a/new.txt b/new.txt\nnew file mode 100644\nindex 000..222\nBinary files /dev/null and b/new.txt differ"},
			{"binary-patch-header", "diff --git a/image.png b/image.png\nindex 111..222 100644\nGIT binary patch"},
		} {
			t.Run(fmt.Sprintf("%s/%q", tc.name, ending), func(t *testing.T) {
				// Keep the metadata beyond the last change's context window,
				// with enough ordinary context to ensure compression still runs.
				lines := 21
				if tc.name == "no-newline" {
					lines++
				}
				input := fmt.Sprintf("diff --git a/app.txt b/app.txt\n--- a/app.txt\n+++ b/app.txt\n@@ -1,%d +1,%d @@\n-old\n+new\n", lines, lines) +
					strings.Repeat(" unchanged context\n", 20) + tc.metadata + "\n"
				input = strings.ReplaceAll(input, "\n", ending)
				out, ok := NewDiff().Compress([]byte(input))
				if !ok {
					t.Fatal("expected diff compression")
				}
				want := strings.ReplaceAll(tc.metadata+"\n", "\n", ending)
				if !bytes.HasSuffix(out, []byte(want)) {
					t.Fatalf("metadata changed or elided; want suffix %q, got:\n%s", want, out)
				}
				if !bytes.Contains(out, []byte("context lines elided (caveman)")) {
					t.Fatalf("expected ordinary context to be elided:\n%s", out)
				}
			})
		}
	}
}
