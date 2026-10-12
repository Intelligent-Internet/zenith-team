package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const localWorkspaceCatalogCompanyID = "01900000-0000-4000-8000-0000000000c1"

func TestLocalWorkspaceAssetListHandlerFiltersSortsAndPaginatesManagedMain(t *testing.T) {
	d, mainDir := newLocalWorkspaceCatalogTestDaemon(t)
	writeLocalWorkspaceCatalogFile(t, mainDir, "docs/alpha.md", []byte("alpha"), time.Unix(10, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "docs/beta.pdf", []byte("beta"), time.Unix(20, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "images/hero.png", []byte("image"), time.Unix(30, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "web/package.json", []byte(`{"name":"web"}`), time.Unix(40, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "web/hidden.pdf", []byte("hidden"), time.Unix(50, 0))

	first := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetListHandler(),
		`{"schemaVersion":1,"companyId":"`+localWorkspaceCatalogCompanyID+`","scope":"engineering","query":"docs","kinds":["document"],"sort":"latest","limit":1}`,
		"Bearer local-capability",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstPage localWorkspaceAssetListResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if firstPage.TotalCount != 2 || len(firstPage.Items) != 1 ||
		firstPage.Items[0].Path != "docs/beta.pdf" || firstPage.NextCursor == nil {
		t.Fatalf("first page = %+v", firstPage)
	}

	secondBody, err := json.Marshal(localWorkspaceAssetListRequest{
		SchemaVersion: 1,
		CompanyID:     localWorkspaceCatalogCompanyID,
		Scope:         "engineering",
		Query:         "docs",
		Kinds:         []localWorkspaceAssetKind{localWorkspaceAssetKindDocument},
		Sort:          localWorkspaceAssetSortLatest,
		Cursor:        firstPage.NextCursor,
		Limit:         1,
	})
	if err != nil {
		t.Fatalf("encode second page: %v", err)
	}
	second := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetListHandler(),
		string(secondBody),
		"Bearer local-capability",
	)
	var secondPage localWorkspaceAssetListResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	if second.Code != http.StatusOK || secondPage.TotalCount != 2 || len(secondPage.Items) != 1 ||
		secondPage.Items[0].Path != "docs/alpha.md" || secondPage.NextCursor != nil {
		t.Fatalf("second page status = %d, page = %+v", second.Code, secondPage)
	}
}

// A page an Agent saved at the workspace root is a deliverable; the source
// of a code project is not. The directory rule tells them apart, so the
// extension alone must not hide the page.
func TestLocalWorkspaceAssetListHandlerListsStandalonePagesButNotCodeProjects(t *testing.T) {
	d, mainDir := newLocalWorkspaceCatalogTestDaemon(t)
	writeLocalWorkspaceCatalogFile(t, mainDir, "cupping-signup.html", []byte("<html></html>"), time.Unix(10, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "site/package.json", []byte(`{"name":"site"}`), time.Unix(20, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "site/index.html", []byte("<html></html>"), time.Unix(30, 0))
	writeLocalWorkspaceCatalogFile(t, mainDir, "notes/app.js", []byte("console.log(1)"), time.Unix(40, 0))

	response := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetListHandler(),
		`{"schemaVersion":1,"companyId":"`+localWorkspaceCatalogCompanyID+`","scope":"engineering","sort":"latest","limit":10}`,
		"Bearer local-capability",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var page localWorkspaceAssetListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.TotalCount != 1 || len(page.Items) != 1 || page.Items[0].Path != "cupping-signup.html" ||
		page.Items[0].Kind != localWorkspaceAssetKindDocument || page.Items[0].MIMEType != "text/html" ||
		page.Items[0].PreviewKind != "text" {
		t.Fatalf("page = %+v", page)
	}
}

func TestLocalWorkspaceMemoryListHandlerReadsOnlySafeMarkdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	d, mainDir := newLocalWorkspaceCatalogTestDaemon(t)
	writeLocalWorkspaceCatalogFile(
		t,
		mainDir,
		".memory/customer-segment.md",
		[]byte("# Customer segment\n\nYoung workers in HCMC.\n"),
		time.Unix(10, 0),
	)
	writeLocalWorkspaceCatalogFile(
		t,
		mainDir,
		".memory/product-scope.md",
		[]byte("# Product scope\n\nA simple website.\n"),
		time.Unix(20, 0),
	)
	writeLocalWorkspaceCatalogFile(t, mainDir, ".memory/.gitkeep", nil, time.Unix(30, 0))
	if err := os.Symlink(
		"customer-segment.md",
		filepath.Join(mainDir, ".memory", "copied.md"),
	); err != nil {
		t.Fatal(err)
	}

	response := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceMemoryListHandler(),
		`{"schemaVersion":1,"companyId":"`+localWorkspaceCatalogCompanyID+`","scope":"engineering"}`,
		"Bearer local-capability",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"filename":"customer-segment.md"`) ||
		!strings.Contains(body, `"filename":"product-scope.md"`) ||
		strings.Contains(body, ".gitkeep") || strings.Contains(body, "copied.md") {
		t.Fatalf("response = %s", body)
	}
}

func TestLocalWorkspaceCatalogHandlersReturnEmptyForMissingManagedMain(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	assetResponse := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceAssetListHandler(),
		`{"schemaVersion":1,"companyId":"`+localWorkspaceCatalogCompanyID+`","scope":"company","query":"","kinds":[],"sort":"latest","limit":100}`,
		"Bearer local-capability",
	)
	memoryResponse := serveLocalWorkspaceRequest(
		t,
		d.localWorkspaceMemoryListHandler(),
		`{"schemaVersion":1,"companyId":"`+localWorkspaceCatalogCompanyID+`","scope":"company"}`,
		"Bearer local-capability",
	)
	if assetResponse.Code != http.StatusOK || !strings.Contains(assetResponse.Body.String(), `"items":[]`) {
		t.Fatalf("asset response = (%d, %s)", assetResponse.Code, assetResponse.Body.String())
	}
	if memoryResponse.Code != http.StatusOK || !strings.Contains(memoryResponse.Body.String(), `"files":[]`) {
		t.Fatalf("memory response = (%d, %s)", memoryResponse.Code, memoryResponse.Body.String())
	}
}

func newLocalWorkspaceCatalogTestDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	d := newTestDaemon(t, "http://127.0.0.1:8080")
	d.localPreviewToken = "local-capability"
	acquired, err := d.workspaces.AcquireScopedScratch(
		t.Context(),
		localWorkspaceCatalogCompanyID,
		"engineering",
		"01900000-0000-4000-8000-0000000000c2",
		uuid.MustParse("00000000-0000-4000-8000-000000001601"),
		0,
	)
	if err != nil {
		t.Fatalf("AcquireScopedScratch(, 0) error = %v", err)
	}
	return d, filepath.Join(filepath.Dir(filepath.Dir(acquired.Root)), "main")
}

func writeLocalWorkspaceCatalogFile(
	t *testing.T,
	root string,
	path string,
	content []byte,
	modifiedAt time.Time,
) {
	t.Helper()
	absolute := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(absolute, modifiedAt, modifiedAt); err != nil {
		t.Fatal(err)
	}
}
