package daemon

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractProjectDOCX(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "brief.docx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(
		[]byte(
			`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Project brief</w:t></w:r></w:p></w:body></w:document>`,
		),
	); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	text, err := extractProjectDocument(t.Context(), path)
	if err != nil || !strings.Contains(string(text), "Project brief") {
		t.Fatalf("extract = %q, %v", text, err)
	}
}

func TestExtractProjectDocumentRejectsInvalidDOCX(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bad.docx")
	if err := os.WriteFile(path, []byte("not a document"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := extractProjectDocument(t.Context(), path); err == nil {
		t.Fatal("invalid DOCX accepted")
	}
}
