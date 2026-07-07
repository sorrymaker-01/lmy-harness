package knowledge

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	statedb "code.byted.org/ai/lmy/apps/server/internal/infra/db"
	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

func TestStoreImportAndList(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	item, err := store.Import("../notes.md", "text/markdown", strings.NewReader("# Notes\nhello"))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if item.ID == "" {
		t.Fatal("Import() returned empty item ID")
	}
	if item.Name != "notes.md" {
		t.Fatalf("item.Name = %q, want %q", item.Name, "notes.md")
	}
	if item.ContentType != "text/markdown" {
		t.Fatalf("item.ContentType = %q, want text/markdown", item.ContentType)
	}
	if item.Size != int64(len("# Notes\nhello")) {
		t.Fatalf("item.Size = %d, want %d", item.Size, len("# Notes\nhello"))
	}
	if filepath.Base(item.Path) == "notes.md" {
		t.Fatalf("stored file name should include generated ID, got %q", item.Path)
	}
	data, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", item.Path, err)
	}
	if string(data) != "# Notes\nhello" {
		t.Fatalf("stored content = %q", string(data))
	}

	items, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List() returned %d items, want 1", len(items))
	}
	if items[0].ID != item.ID || items[0].Name != "notes.md" {
		t.Fatalf("List()[0] = %+v, want imported item", items[0])
	}

	deleted, err := store.Delete(item.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !deleted {
		t.Fatal("Delete() returned false, want true")
	}
	if _, err := os.Stat(item.Path); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat error = %v, want not exist", err)
	}
	items, err = store.List()
	if err != nil {
		t.Fatalf("List() after delete error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List() after delete returned %d items, want 0", len(items))
	}
}

func TestStoreImportRetrieveAndDeleteWithSQLite(t *testing.T) {
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	store, err := NewStoreWithDB(filepath.Join(root, "knowledge"), database)
	if err != nil {
		t.Fatalf("NewStoreWithDB() error = %v", err)
	}

	item, err := store.Import("rag.md", "text/markdown", strings.NewReader("# RAG\n\nSQLite FTS5 handles keyword recall. sqlite-vec handles vector recall. Parent chunks preserve context."))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if item.ChunkCount == 0 {
		t.Fatalf("item.ChunkCount = 0, want indexed child chunks")
	}
	if item.ChunkCount != item.ChildChunks+item.ParentChunks {
		t.Fatalf("chunk counts = total %d child %d parent %d, want total=child+parent", item.ChunkCount, item.ChildChunks, item.ParentChunks)
	}
	if item.ChildChunks == 0 || item.ParentChunks == 0 {
		t.Fatalf("child/parent chunks = %d/%d, want both indexed", item.ChildChunks, item.ParentChunks)
	}
	if item.Path != "" {
		t.Fatalf("item.Path = %q, want raw file path cleared after indexing", item.Path)
	}
	files, err := filepath.Glob(filepath.Join(root, "knowledge", "files", item.ID+"_*"))
	if err != nil {
		t.Fatalf("glob raw files: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("raw files = %v, want deleted after indexing", files)
	}
	parsedFiles, err := filepath.Glob(filepath.Join(root, "knowledge", "parsed", item.ID+"_v*.txt"))
	if err != nil {
		t.Fatalf("glob parsed files: %v", err)
	}
	if len(parsedFiles) != 0 {
		t.Fatalf("parsed files = %v, want deleted after indexing", parsedFiles)
	}
	var sourceURI, rawPath, parsedPath string
	if err := database.QueryRow(
		`SELECT d.source_uri, v.raw_path, v.parsed_path
		 FROM documents d
		 JOIN document_versions v ON v.doc_id = d.id AND v.version = d.active_version
		 WHERE d.id = ?`,
		item.ID,
	).Scan(&sourceURI, &rawPath, &parsedPath); err != nil {
		t.Fatalf("query document paths: %v", err)
	}
	if sourceURI != "" || rawPath != "" {
		t.Fatalf("source_uri=%q raw_path=%q, want both cleared", sourceURI, rawPath)
	}
	if parsedPath != "" {
		t.Fatalf("parsed_path=%q, want cleared", parsedPath)
	}

	result, err := store.Retrieve(testContext(), "FTS5 vector recall", RetrievalOptions{TopK: 3})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.RerankedResults) == 0 {
		t.Fatalf("Retrieve() returned no reranked results")
	}
	if !strings.Contains(result.RerankedResults[0].Content, "sqlite-vec handles vector recall") {
		t.Fatalf("retrieved content = %q, want imported content", result.RerankedResults[0].Content)
	}

	deleted, err := store.Delete(item.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !deleted {
		t.Fatal("Delete() returned false, want true")
	}
	afterDelete, err := store.Retrieve(testContext(), "FTS5 vector recall", RetrievalOptions{TopK: 3})
	if err != nil {
		t.Fatalf("Retrieve() after delete error = %v", err)
	}
	if len(afterDelete.RerankedResults) != 0 {
		t.Fatalf("Retrieve() after delete returned %d results, want 0", len(afterDelete.RerankedResults))
	}
}

func TestRetrieveFiltersByKnowledgeBase(t *testing.T) {
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	store, err := NewStoreWithDB(filepath.Join(root, "knowledge"), database)
	if err != nil {
		t.Fatalf("NewStoreWithDB() error = %v", err)
	}

	left, err := store.CreateKnowledgeBase(testContext(), "Left", "")
	if err != nil {
		t.Fatalf("CreateKnowledgeBase(left) error = %v", err)
	}
	right, err := store.CreateKnowledgeBase(testContext(), "Right", "")
	if err != nil {
		t.Fatalf("CreateKnowledgeBase(right) error = %v", err)
	}
	if _, err := store.ImportToKnowledgeBase(testContext(), left.ID, "left.md", "text/markdown", strings.NewReader("shared retrieval token only left knowledge base should appear")); err != nil {
		t.Fatalf("ImportToKnowledgeBase(left) error = %v", err)
	}
	if _, err := store.ImportToKnowledgeBase(testContext(), right.ID, "right.md", "text/markdown", strings.NewReader("shared retrieval token only right knowledge base should not appear")); err != nil {
		t.Fatalf("ImportToKnowledgeBase(right) error = %v", err)
	}

	result, err := store.Retrieve(testContext(), "shared retrieval token", RetrievalOptions{
		TopK: 8,
		Filter: RetrievalFilter{
			KnowledgeBaseIDs: []string{" " + left.ID + " "},
		},
	})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.RerankedResults) == 0 {
		t.Fatal("Retrieve() returned no results")
	}
	for _, chunk := range result.RerankedResults {
		if chunk.KnowledgeBaseID != left.ID {
			t.Fatalf("retrieved chunk from knowledge base %q, want %q", chunk.KnowledgeBaseID, left.ID)
		}
	}
	if len(result.Filter.KnowledgeBaseIDs) != 1 || result.Filter.KnowledgeBaseIDs[0] != left.ID {
		t.Fatalf("normalized filter = %+v, want only %q", result.Filter, left.ID)
	}
}

func TestDeleteDocumentMarksVectorRowsDeleted(t *testing.T) {
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	store, err := NewStoreWithDB(filepath.Join(root, "knowledge"), database)
	if err != nil {
		t.Fatalf("NewStoreWithDB() error = %v", err)
	}
	item, err := store.Import("delete-vector.md", "text/markdown", strings.NewReader("delete vector row after document deletion"))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	insertVectorRowsForDocument(t, database, item.ID)
	deleted, err := store.Delete(item.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !deleted {
		t.Fatal("Delete() returned false, want true")
	}
	assertActiveVectorRows(t, database, "doc_id = ?", []any{item.ID}, 0)
}

func TestDeleteKnowledgeBaseMarksVectorRowsDeleted(t *testing.T) {
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	store, err := NewStoreWithDB(filepath.Join(root, "knowledge"), database)
	if err != nil {
		t.Fatalf("NewStoreWithDB() error = %v", err)
	}
	base, err := store.CreateKnowledgeBase(context.Background(), "Archive", "")
	if err != nil {
		t.Fatalf("CreateKnowledgeBase() error = %v", err)
	}
	item, err := store.ImportToKnowledgeBase(context.Background(), base.ID, "archive.md", "text/markdown", strings.NewReader("archive vector row after knowledge base deletion"))
	if err != nil {
		t.Fatalf("ImportToKnowledgeBase() error = %v", err)
	}
	insertVectorRowsForDocument(t, database, item.ID)
	deleted, err := store.DeleteKnowledgeBase(context.Background(), base.ID)
	if err != nil {
		t.Fatalf("DeleteKnowledgeBase() error = %v", err)
	}
	if !deleted {
		t.Fatal("DeleteKnowledgeBase() returned false, want true")
	}
	assertActiveVectorRows(t, database, "knowledge_base_id = ?", []any{base.ID}, 0)
}

func TestExtractTextContentFromPDFActualText(t *testing.T) {
	pdf := testPDFWithStream(t, `BT /Span<</ActualText <FEFF4F60597D4E16754C> >> BDC <01> Tj EMC ET`)
	text, err := extractTextContent(context.Background(), pdf, "application/pdf", "hello.pdf", "")
	if err != nil {
		t.Fatalf("extractTextContent() error = %v", err)
	}
	if !strings.Contains(text, "你好世界") {
		t.Fatalf("extracted text = %q, want ActualText content", text)
	}
}

func TestSQLiteVecIndexUpsertSearchAndDelete(t *testing.T) {
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	index := NewSQLiteVecIndex(database)
	err = index.Upsert(context.Background(), []VectorPoint{
		{
			ID:              "chunk-alpha",
			ChunkID:         "chunk-alpha",
			DocID:           "doc-alpha",
			DocVersion:      1,
			KnowledgeBaseID: "kb-alpha",
			Title:           "Alpha",
			ContentHash:     "hash-alpha",
			SourceType:      "upload",
			HeadingPath:     "Alpha",
			Status:          "active",
			Vector:          []float32{1, 0, 0, 0},
			Metadata:        map[string]any{"file_name": "alpha.md"},
		},
		{
			ID:              "chunk-beta",
			ChunkID:         "chunk-beta",
			DocID:           "doc-beta",
			DocVersion:      1,
			KnowledgeBaseID: "kb-beta",
			Title:           "Beta",
			ContentHash:     "hash-beta",
			SourceType:      "upload",
			HeadingPath:     "Beta",
			Status:          "active",
			Vector:          []float32{0, 1, 0, 0},
			Metadata:        map[string]any{"file_name": "beta.md"},
		},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	hits, err := index.Search(context.Background(), []float32{1, 0, 0, 0}, RetrievalFilter{KnowledgeBaseIDs: []string{"kb-alpha"}}, 2)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkID != "chunk-alpha" {
		t.Fatalf("Search() hits = %+v, want chunk-alpha only", hits)
	}
	if err := index.DeleteDocument(context.Background(), "doc-alpha", 1); err != nil {
		t.Fatalf("DeleteDocument() error = %v", err)
	}
	hits, err = index.Search(context.Background(), []float32{1, 0, 0, 0}, RetrievalFilter{}, 5)
	if err != nil {
		t.Fatalf("Search() after delete error = %v", err)
	}
	for _, hit := range hits {
		if hit.ChunkID == "chunk-alpha" {
			t.Fatalf("Search() after delete returned deleted chunk: %+v", hits)
		}
	}
}

func TestExtractTextContentFromPDFRejectsGarbledStreamText(t *testing.T) {
	pdf := testPDFWithStream(t, "BT (��{�֤x��=շdDH����) Tj ET")
	text, err := extractTextContent(context.Background(), pdf, "application/pdf", "scan.pdf", "")
	if err != nil {
		t.Fatalf("extractTextContent() error = %v", err)
	}
	if strings.Contains(text, "��") || strings.Contains(text, "֤x") {
		t.Fatalf("extracted text = %q, want garbled stream text rejected", text)
	}
	if !strings.Contains(text, "needs OCR") {
		t.Fatalf("extracted text = %q, want OCR notice", text)
	}
}

func TestExtractTextContentFromPDFRejectsASCIIStreamNoise(t *testing.T) {
	pdf := testPDFWithStream(t, "BT (DNNNNNNN. dKNN5 C}]N` .Al*L+O .bJ>M_;G iX DeICNN/b) Tj ET")
	text, err := extractTextContent(context.Background(), pdf, "application/pdf", "scan.pdf", "")
	if err != nil {
		t.Fatalf("extractTextContent() error = %v", err)
	}
	if strings.Contains(text, "DNNNNNNN") || strings.Contains(text, "dKNN5") {
		t.Fatalf("extracted text = %q, want ASCII stream noise rejected", text)
	}
	if !strings.Contains(text, "needs OCR") {
		t.Fatalf("extracted text = %q, want OCR notice", text)
	}
}

func TestExtractTextContentFromOfficeOpenXML(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		parts       map[string]string
		want        []string
	}{
		{
			name:        "仓南里字节同学优惠停车申请表.docx",
			contentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			parts: map[string]string{
				"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>仓南里字节同学优惠停车申请表</w:t></w:r></w:p><w:p><w:r><w:t>车牌号</w:t></w:r><w:r><w:tab/><w:t>京A12345</w:t></w:r></w:p></w:body></w:document>`,
			},
			want: []string{"仓南里字节同学优惠停车申请表", "车牌号", "京A12345"},
		},
		{
			name:        "停车名单.xlsx",
			contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			parts: map[string]string{
				"xl/sharedStrings.xml":     `<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>姓名</t></si><si><t>张三</t></si></sst>`,
				"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row><c t="s"><v>0</v></c><c t="s"><v>1</v></c><c><v>42</v></c></row></sheetData></worksheet>`,
			},
			want: []string{"姓名", "张三", "42"},
		},
		{
			name:        "停车说明.pptx",
			contentType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
			parts: map[string]string{
				"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8"?><p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>优惠停车申请流程</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`,
			},
			want: []string{"优惠停车申请流程"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := extractTextContent(context.Background(), testZipFile(t, tt.parts), tt.contentType, tt.name, "")
			if err != nil {
				t.Fatalf("extractTextContent() error = %v", err)
			}
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Fatalf("extracted text = %q, want %q", text, want)
				}
			}
		})
	}
}

func TestExtractTextContentFromLegacyOfficeReturnsActionableError(t *testing.T) {
	_, err := extractTextContent(context.Background(), []byte{0xd0, 0xcf, 0x11, 0xe0, 0x00}, "application/msword", "old.doc", "")
	if err == nil {
		t.Fatal("extractTextContent() error = nil, want legacy Office error")
	}
	if !strings.Contains(err.Error(), "legacy Microsoft Office binary document") {
		t.Fatalf("error = %q, want legacy Office guidance", err.Error())
	}
}

func TestStoreImportPDFWithLimitedTextDoesNotFail(t *testing.T) {
	root := t.TempDir()
	database, err := statedb.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer database.Close()
	store, err := NewStoreWithDB(filepath.Join(root, "knowledge"), database)
	if err != nil {
		t.Fatalf("NewStoreWithDB() error = %v", err)
	}
	item, err := store.Import("交底书.pdf", "application/pdf", bytes.NewReader([]byte("%PDF-1.7\n1 0 obj\n<</Type/Catalog>>\nendobj\n%%EOF")))
	if err != nil {
		t.Fatalf("Import() PDF error = %v", err)
	}
	if item.ID == "" || item.ChunkCount == 0 {
		t.Fatalf("imported item = %+v, want indexed PDF fallback", item)
	}
	result, err := store.Retrieve(testContext(), "交底书", RetrievalOptions{TopK: 3})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.RerankedResults) == 0 {
		t.Fatalf("Retrieve() returned no result for PDF title fallback")
	}
}

func testPDFWithStream(t *testing.T, content string) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.7\n1 0 obj\n<</Filter/FlateDecode/Length ")
	pdf.WriteString(strconv.Itoa(compressed.Len()))
	pdf.WriteString(">>\nstream\n")
	pdf.Write(compressed.Bytes())
	pdf.WriteString("\nendstream\nendobj\n%%EOF")
	return pdf.Bytes()
}

func testZipFile(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := file.Write([]byte(parts[name])); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return out.Bytes()
}

func insertVectorRowsForDocument(t *testing.T, database *sql.DB, docID string) {
	t.Helper()
	now := formatSQLiteTime(shared.Now())
	_, err := database.Exec(
		`INSERT INTO document_chunk_vector_rows(
			chunk_id, doc_id, doc_version, knowledge_base_id, title, content_hash, source_type,
			heading_path, status, metadata_json, vector_dimension, created_at, updated_at
		)
		SELECT c.id, c.doc_id, c.doc_version, c.knowledge_base_id, c.title, c.content_hash, d.source_type,
			c.heading_path, 'active', '{}', 3, ?, ?
		FROM document_chunks c
		JOIN documents d ON d.id = c.doc_id
		WHERE c.doc_id = ? AND c.chunk_type = 'child' AND c.status = 'active'`,
		now,
		now,
		docID,
	)
	if err != nil {
		t.Fatalf("insert vector rows: %v", err)
	}
}

func assertActiveVectorRows(t *testing.T, database *sql.DB, where string, args []any, want int) {
	t.Helper()
	queryArgs := append([]any{}, args...)
	var got int
	if err := database.QueryRow(`SELECT COUNT(*) FROM document_chunk_vector_rows WHERE status = 'active' AND `+where, queryArgs...).Scan(&got); err != nil {
		t.Fatalf("count active vector rows: %v", err)
	}
	if got != want {
		t.Fatalf("active vector rows = %d, want %d", got, want)
	}
}

func testContext() context.Context {
	return context.Background()
}
