package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

const vectorIndexBatchSize = 64

type documentVersionRef struct {
	id      string
	version int
}

func (s *Store) ensureDefaultKnowledgeBase(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	now := formatSQLiteTime(shared.Now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO knowledge_bases(id, name, description, status, created_at, updated_at)
		 VALUES (?, ?, '', 'active', ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			status = 'active',
			updated_at = CASE
				WHEN knowledge_bases.name != excluded.name OR knowledge_bases.status != 'active' OR knowledge_bases.deleted_at IS NOT NULL
				THEN excluded.updated_at
				ELSE knowledge_bases.updated_at
			END,
			deleted_at = NULL`,
		defaultKnowledgeBaseID,
		defaultKnowledgeBaseName,
		now,
		now,
	)
	return err
}

func (s *Store) ListKnowledgeBases(ctx context.Context) ([]KnowledgeBase, error) {
	if s == nil || s.db == nil {
		return []KnowledgeBase{{ID: defaultKnowledgeBaseID, Name: defaultKnowledgeBaseName, Status: "active"}}, nil
	}
	if err := s.ensureDefaultKnowledgeBase(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kb.id, kb.name, kb.description, kb.status, kb.created_at, kb.updated_at,
			COUNT(DISTINCT CASE WHEN d.status = 'active' THEN d.id END) AS document_count,
			COUNT(CASE WHEN d.status = 'active' AND c.status = 'active' THEN 1 END) AS chunk_count,
			COUNT(CASE WHEN d.status = 'active' AND c.status = 'active' AND c.chunk_type = 'child' THEN 1 END) AS child_chunk_count,
			COUNT(CASE WHEN d.status = 'active' AND c.status = 'active' AND c.chunk_type = 'parent' THEN 1 END) AS parent_chunk_count
		 FROM knowledge_bases kb
		 LEFT JOIN documents d ON d.knowledge_base_id = kb.id
		 LEFT JOIN document_chunks c ON c.doc_id = d.id AND c.doc_version = d.active_version
		 WHERE kb.status != 'deleted'
		 GROUP BY kb.id, kb.name, kb.description, kb.status, kb.created_at, kb.updated_at
		 ORDER BY CASE WHEN kb.id = ? THEN 0 ELSE 1 END, kb.updated_at DESC`,
		defaultKnowledgeBaseID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bases := []KnowledgeBase{}
	for rows.Next() {
		var base KnowledgeBase
		if err := rows.Scan(&base.ID, &base.Name, &base.Description, &base.Status, &base.CreatedAt, &base.UpdatedAt, &base.DocumentCount, &base.ChunkCount, &base.ChildChunks, &base.ParentChunks); err != nil {
			return nil, err
		}
		bases = append(bases, base)
	}
	return bases, rows.Err()
}

func (s *Store) CreateKnowledgeBase(ctx context.Context, name string, description string) (KnowledgeBase, error) {
	if s == nil || s.db == nil {
		return KnowledgeBase{}, fmt.Errorf("state database is unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return KnowledgeBase{}, fmt.Errorf("knowledge base name is required")
	}
	id := shared.NewID("kbbase")
	now := formatSQLiteTime(shared.Now())
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO knowledge_bases(id, name, description, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', ?, ?)`,
		id,
		name,
		strings.TrimSpace(description),
		now,
		now,
	); err != nil {
		return KnowledgeBase{}, err
	}
	return s.knowledgeBase(ctx, id)
}

func (s *Store) DeleteKnowledgeBase(ctx context.Context, id string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	id = normalizeKnowledgeBaseID(id)
	if id == defaultKnowledgeBaseID {
		return false, fmt.Errorf("default knowledge base cannot be deleted")
	}
	if _, err := s.knowledgeBase(ctx, id); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	documents, err := s.activeDocumentVersionsForKnowledgeBase(ctx, id)
	if err != nil {
		return false, err
	}
	now := formatSQLiteTime(shared.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollbackQuietly(tx)
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_bases SET status = 'deleted', deleted_at = ?, updated_at = ? WHERE id = ?`, now, now, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = 'deleted', deleted_at = ?, updated_at = ? WHERE knowledge_base_id = ?`, now, now, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_versions SET status = 'deleted' WHERE doc_id IN (SELECT id FROM documents WHERE knowledge_base_id = ?)`, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_chunks SET status = 'deleted', deleted_at = ?, updated_at = ? WHERE knowledge_base_id = ?`, now, now, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_chunk_vector_rows SET status = 'deleted', updated_at = ? WHERE knowledge_base_id = ?`, now, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO index_outbox(id, event_type, doc_id, doc_version, payload_json, status, retry_count, error, created_at, updated_at)
		 VALUES (?, 'knowledge_base.deleted', '', 0, ?, 'done', 0, '', ?, ?)`,
		shared.NewID("outbox"),
		mustKnowledgeJSON(map[string]any{"knowledge_base_id": id}),
		now,
		now,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	for _, document := range documents {
		s.deleteVectorsAsync(document.id, document.version)
	}
	return true, nil
}

func (s *Store) activeDocumentVersionsForKnowledgeBase(ctx context.Context, knowledgeBaseID string) ([]documentVersionRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, active_version FROM documents WHERE knowledge_base_id = ? AND status != 'deleted'`, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	documents := []documentVersionRef{}
	for rows.Next() {
		var document documentVersionRef
		if err := rows.Scan(&document.id, &document.version); err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	return documents, rows.Err()
}

func (s *Store) knowledgeBase(ctx context.Context, id string) (KnowledgeBase, error) {
	id = normalizeKnowledgeBaseID(id)
	var base KnowledgeBase
	err := s.db.QueryRowContext(ctx,
		`SELECT kb.id, kb.name, kb.description, kb.status, kb.created_at, kb.updated_at,
			COUNT(DISTINCT CASE WHEN d.status = 'active' THEN d.id END) AS document_count,
			COUNT(CASE WHEN d.status = 'active' AND c.status = 'active' THEN 1 END) AS chunk_count,
			COUNT(CASE WHEN d.status = 'active' AND c.status = 'active' AND c.chunk_type = 'child' THEN 1 END) AS child_chunk_count,
			COUNT(CASE WHEN d.status = 'active' AND c.status = 'active' AND c.chunk_type = 'parent' THEN 1 END) AS parent_chunk_count
		 FROM knowledge_bases kb
		 LEFT JOIN documents d ON d.knowledge_base_id = kb.id
		 LEFT JOIN document_chunks c ON c.doc_id = d.id AND c.doc_version = d.active_version
		 WHERE kb.id = ? AND kb.status != 'deleted'
		 GROUP BY kb.id, kb.name, kb.description, kb.status, kb.created_at, kb.updated_at`,
		id,
	).Scan(&base.ID, &base.Name, &base.Description, &base.Status, &base.CreatedAt, &base.UpdatedAt, &base.DocumentCount, &base.ChunkCount, &base.ChildChunks, &base.ParentChunks)
	return base, err
}

func (s *Store) ensureKnowledgeBaseExists(ctx context.Context, id string) error {
	id = normalizeKnowledgeBaseID(id)
	if id == defaultKnowledgeBaseID {
		return s.ensureDefaultKnowledgeBase(ctx)
	}
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM knowledge_bases WHERE id = ? AND status != 'deleted'`, id).Scan(&existing)
	if err == sql.ErrNoRows {
		return fmt.Errorf("knowledge base %q not found", id)
	}
	return err
}

func (s *Store) listSQLite(ctx context.Context, knowledgeBaseID string) ([]Item, error) {
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	where := "WHERE d.status != 'deleted'"
	args := []any{}
	if knowledgeBaseID != "" {
		where += " AND d.knowledge_base_id = ?"
		args = append(args, normalizeKnowledgeBaseID(knowledgeBaseID))
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.knowledge_base_id, d.title, d.size, d.content_type, d.source_uri, d.created_at, d.status,
			COUNT(CASE WHEN c.status = 'active' THEN 1 END) AS chunks,
			COUNT(CASE WHEN c.chunk_type = 'child' AND c.status = 'active' THEN 1 END) AS child_chunks,
			COUNT(CASE WHEN c.chunk_type = 'parent' AND c.status = 'active' THEN 1 END) AS parent_chunks
		 FROM documents d
		 LEFT JOIN document_chunks c ON c.doc_id = d.id AND c.doc_version = d.active_version
		 `+where+`
		 GROUP BY d.id, d.knowledge_base_id, d.title, d.size, d.content_type, d.source_uri, d.created_at, d.status
		 ORDER BY d.created_at DESC`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.KnowledgeBaseID, &item.Name, &item.Size, &item.ContentType, &item.Path, &item.ImportedAt, &item.Status, &item.ChunkCount, &item.ChildChunks, &item.ParentChunks); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) itemSQLite(ctx context.Context, id string) (Item, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Item{}, false, nil
	}
	var item Item
	err := s.db.QueryRowContext(ctx,
		`SELECT d.id, d.knowledge_base_id, d.title, d.size, d.content_type, d.source_uri, d.created_at, d.status,
			COUNT(CASE WHEN c.status = 'active' THEN 1 END) AS chunks,
			COUNT(CASE WHEN c.chunk_type = 'child' AND c.status = 'active' THEN 1 END) AS child_chunks,
			COUNT(CASE WHEN c.chunk_type = 'parent' AND c.status = 'active' THEN 1 END) AS parent_chunks
		 FROM documents d
		 LEFT JOIN document_chunks c ON c.doc_id = d.id AND c.doc_version = d.active_version
		 WHERE d.id = ? AND d.status != 'deleted'
		 GROUP BY d.id, d.knowledge_base_id, d.title, d.size, d.content_type, d.source_uri, d.created_at, d.status`,
		id,
	).Scan(&item.ID, &item.KnowledgeBaseID, &item.Name, &item.Size, &item.ContentType, &item.Path, &item.ImportedAt, &item.Status, &item.ChunkCount, &item.ChildChunks, &item.ParentChunks)
	if err == sql.ErrNoRows {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	return item, true, nil
}

func (s *Store) migrateLegacyIndex(ctx context.Context) error {
	index, err := s.loadIndex()
	if err != nil {
		return err
	}
	for _, item := range index.Items {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		if exists, err := s.documentExists(ctx, item.ID); err != nil {
			return err
		} else if exists {
			continue
		}
		path, err := s.resolveLegacyItemPath(item)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		item.Path = path
		item.Size = info.Size()
		if strings.TrimSpace(item.ContentType) == "" {
			item.ContentType = contentTypeFromName(item.Name)
		}
		if err := s.indexImportedItem(ctx, item); err != nil {
			_ = s.recordOutbox(ctx, "legacy.import", item.ID, 1, "", map[string]any{"error": err.Error(), "path": path}, "failed", err.Error())
			continue
		}
	}
	return nil
}

func (s *Store) repairLimitedPDFExtractions(ctx context.Context) error {
	if s.db == nil || !pdftotextAvailable() {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.knowledge_base_id, d.title, d.size, d.content_type, d.source_uri, d.created_at, d.status,
			v.version, v.parsed_path
		 FROM documents d
		 JOIN document_versions v ON v.doc_id = d.id AND v.version = d.active_version
		 WHERE d.status = 'active'
		   AND v.status = 'active'
		   AND (lower(d.content_type) = 'application/pdf' OR lower(d.title) LIKE '%.pdf')`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	type repairCandidate struct {
		item       Item
		version    int
		parsedPath string
	}
	candidates := []repairCandidate{}
	for rows.Next() {
		var candidate repairCandidate
		if err := rows.Scan(
			&candidate.item.ID,
			&candidate.item.KnowledgeBaseID,
			&candidate.item.Name,
			&candidate.item.Size,
			&candidate.item.ContentType,
			&candidate.item.Path,
			&candidate.item.ImportedAt,
			&candidate.item.Status,
			&candidate.version,
			&candidate.parsedPath,
		); err != nil {
			return err
		}
		parsed, err := os.ReadFile(candidate.parsedPath)
		if err != nil || !strings.Contains(string(parsed), limitedPDFExtractionNotice) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := s.reindexDocumentVersion(ctx, candidate.item, candidate.version, candidate.parsedPath); err != nil {
			_ = s.recordOutbox(ctx, "pdf.reindex", candidate.item.ID, candidate.version, "", map[string]any{"error": err.Error()}, "failed", err.Error())
		}
	}
	return nil
}

func (s *Store) cleanupIndexedFiles(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.active_version, v.raw_path, v.parsed_path
		 FROM documents d
		 JOIN document_versions v ON v.doc_id = d.id AND v.version = d.active_version
		 WHERE d.status = 'active'
		   AND v.status = 'active'
		   AND v.parsed_path != ''`,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	type candidate struct {
		docID      string
		version    int
		rawPath    string
		parsedPath string
	}
	candidates := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.docID, &item.version, &item.rawPath, &item.parsedPath); err != nil {
			return err
		}
		parsed, err := os.ReadFile(item.parsedPath)
		if err != nil || !shouldDeleteStoredFilesAfterIndex(string(parsed)) {
			continue
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range candidates {
		_ = s.cleanupStoredFiles(ctx, item.docID, item.version, item.rawPath, item.parsedPath)
	}
	return nil
}

func shouldDeleteStoredFilesAfterIndex(parsed string) bool {
	parsed = strings.TrimSpace(parsed)
	return parsed != "" && !strings.Contains(parsed, limitedPDFExtractionNotice)
}

func (s *Store) cleanupStoredFiles(ctx context.Context, docID string, version int, rawPath string, parsedPath string) error {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath != "" && s.isKnowledgeRawFile(rawPath) {
		if err := os.Remove(rawPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	parsedPath = strings.TrimSpace(parsedPath)
	if parsedPath != "" && s.isKnowledgeParsedFile(parsedPath) {
		if err := os.Remove(parsedPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if s.db == nil {
		return nil
	}
	if rawPath != "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE documents SET source_uri = '' WHERE id = ? AND source_uri = ?`, docID, rawPath); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE document_versions SET raw_path = '' WHERE doc_id = ? AND version = ? AND raw_path = ?`, docID, version, rawPath); err != nil {
			return err
		}
	}
	if parsedPath != "" {
		if _, err := s.db.ExecContext(ctx, `UPDATE document_versions SET parsed_path = '' WHERE doc_id = ? AND version = ? AND parsed_path = ?`, docID, version, parsedPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) isKnowledgeRawFile(path string) bool {
	return s.isPathUnder(path, s.filesDir())
}

func (s *Store) isKnowledgeParsedFile(path string) bool {
	return s.isPathUnder(path, s.parsedDir())
}

func (s *Store) isPathUnder(path string, dir string) bool {
	baseDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rawPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(rawPath, baseDir+string(os.PathSeparator))
}

func (s *Store) reindexDocumentVersion(ctx context.Context, item Item, version int, parsedPath string) error {
	item.KnowledgeBaseID = normalizeKnowledgeBaseID(item.KnowledgeBaseID)
	if err := s.ensureKnowledgeBaseExists(ctx, item.KnowledgeBaseID); err != nil {
		return err
	}
	raw, err := os.ReadFile(item.Path)
	if err != nil {
		return err
	}
	parsed, err := extractTextContent(ctx, raw, item.ContentType, item.Name, item.Path)
	if err != nil {
		return err
	}
	parsed = strings.TrimSpace(parsed)
	if parsed == "" || strings.Contains(parsed, limitedPDFExtractionNotice) {
		return nil
	}
	if err := os.MkdirAll(s.parsedDir(), 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(parsedPath) == "" {
		parsedPath = filepath.Join(s.parsedDir(), fmt.Sprintf("%s_v%d.txt", item.ID, version))
	}
	if err := os.WriteFile(parsedPath, []byte(parsed), 0o644); err != nil {
		return err
	}
	chunks := chunkDocument(item.Name, parsed)
	if len(chunks) == 0 {
		chunks = chunkDocument(item.Name, item.Name)
	}
	now := formatSQLiteTime(shared.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackQuietly(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE doc_id = ? AND doc_version = ?`, item.ID, version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE document_versions
		 SET content_hash = ?, raw_path = ?, parsed_path = ?, status = 'active', error = '', activated_at = ?
		 WHERE doc_id = ? AND version = ?`,
		contentHash(parsed),
		item.Path,
		parsedPath,
		now,
		item.ID,
		version,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE documents
		 SET knowledge_base_id = ?, title = ?, source_uri = ?, content_type = ?, size = ?, status = 'active', updated_at = ?
		 WHERE id = ?`,
		item.KnowledgeBaseID,
		item.Name,
		item.Path,
		item.ContentType,
		item.Size,
		now,
		item.ID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ingestion_jobs(id, doc_id, doc_version, status, stage, error, created_at, started_at, finished_at)
		 VALUES (?, ?, ?, 'done', 'pdf_reindex', '', ?, ?, ?)`,
		shared.NewID("ingest"),
		item.ID,
		version,
		now,
		now,
		now,
	); err != nil {
		return err
	}
	if err := insertChunks(ctx, tx, item, version, chunks, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO index_outbox(id, event_type, doc_id, doc_version, payload_json, status, retry_count, error, created_at, updated_at)
		 VALUES (?, 'document.reindexed', ?, ?, ?, 'done', 0, '', ?, ?)`,
		shared.NewID("outbox"),
		item.ID,
		version,
		mustKnowledgeJSON(map[string]any{"chunks": len(chunks), "extractor": "pdftotext"}),
		now,
		now,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if shouldDeleteStoredFilesAfterIndex(parsed) {
		_ = s.cleanupStoredFiles(context.Background(), item.ID, version, item.Path, parsedPath)
	}
	s.deleteVectorsAsync(item.ID, version)
	s.indexVectorsAsync(item, version, chunks)
	return nil
}

func (s *Store) documentExists(ctx context.Context, id string) (bool, error) {
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM documents WHERE id = ?`, id).Scan(&existing)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) resolveLegacyItemPath(item Item) (string, error) {
	if matches, err := filepath.Glob(filepath.Join(s.filesDir(), item.ID+"_*")); err == nil && len(matches) > 0 {
		return matches[0], nil
	}
	if strings.TrimSpace(item.Path) == "" {
		return "", os.ErrNotExist
	}
	if _, err := os.Stat(item.Path); err != nil {
		return "", err
	}
	filesDir, err := filepath.Abs(s.filesDir())
	if err != nil {
		return "", err
	}
	itemPath, err := filepath.Abs(item.Path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(itemPath, filesDir+string(os.PathSeparator)) {
		return itemPath, nil
	}
	if err := os.MkdirAll(s.filesDir(), 0o755); err != nil {
		return "", err
	}
	fileName := item.ID + "_" + sanitizeFileName(item.Name)
	if fileName == item.ID+"_" {
		fileName = item.ID + "_document"
	}
	dest := filepath.Join(s.filesDir(), fileName)
	data, err := os.ReadFile(itemPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

func (s *Store) indexImportedItem(ctx context.Context, item Item) error {
	item.KnowledgeBaseID = normalizeKnowledgeBaseID(item.KnowledgeBaseID)
	if err := s.ensureKnowledgeBaseExists(ctx, item.KnowledgeBaseID); err != nil {
		return err
	}
	raw, err := os.ReadFile(item.Path)
	if err != nil {
		return err
	}
	parsed, err := extractTextContent(ctx, raw, item.ContentType, item.Name, item.Path)
	if err != nil {
		return err
	}
	parsed = strings.TrimSpace(parsed)
	if parsed == "" {
		parsed = item.Name
	}
	if err := os.MkdirAll(s.parsedDir(), 0o755); err != nil {
		return err
	}
	version := 1
	versionID := shared.NewID("docver")
	parsedPath := filepath.Join(s.parsedDir(), item.ID+"_v1.txt")
	if err := os.WriteFile(parsedPath, []byte(parsed), 0o644); err != nil {
		return err
	}
	chunks := chunkDocument(item.Name, parsed)
	if len(chunks) == 0 {
		chunks = chunkDocument(item.Name, item.Name)
	}
	now := formatSQLiteTime(shared.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackQuietly(tx)

	jobID := shared.NewID("ingest")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO documents(id, knowledge_base_id, title, source_type, source_uri, content_type, size, status, active_version, metadata_json, created_at, updated_at)
		 VALUES (?, ?, ?, 'upload', ?, ?, ?, 'active', ?, ?, ?, ?)`,
		item.ID,
		item.KnowledgeBaseID,
		item.Name,
		item.Path,
		item.ContentType,
		item.Size,
		version,
		mustKnowledgeJSON(map[string]any{"file_name": item.Name}),
		now,
		now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO document_versions(id, doc_id, version, content_hash, raw_path, parsed_path, status, error, created_at, activated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', '', ?, ?)`,
		versionID,
		item.ID,
		version,
		contentHash(parsed),
		item.Path,
		parsedPath,
		now,
		now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO ingestion_jobs(id, doc_id, doc_version, status, stage, error, created_at, started_at, finished_at)
		 VALUES (?, ?, ?, 'done', 'index', '', ?, ?, ?)`,
		jobID,
		item.ID,
		version,
		now,
		now,
		now,
	); err != nil {
		return err
	}
	if err := insertChunks(ctx, tx, item, version, chunks, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO index_outbox(id, event_type, doc_id, doc_version, payload_json, status, retry_count, error, created_at, updated_at)
		 VALUES (?, 'document.indexed', ?, ?, ?, 'done', 0, '', ?, ?)`,
		shared.NewID("outbox"),
		item.ID,
		version,
		mustKnowledgeJSON(map[string]any{"chunks": len(chunks)}),
		now,
		now,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if shouldDeleteStoredFilesAfterIndex(parsed) {
		_ = s.cleanupStoredFiles(context.Background(), item.ID, version, item.Path, parsedPath)
	}
	s.indexVectorsAsync(item, version, chunks)
	return nil
}

func contentTypeFromName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}

const insertChunkSQL = `INSERT INTO document_chunks(
	id, doc_id, doc_version, knowledge_base_id, parent_chunk_id, chunk_type, chunk_index,
	title, content, content_hash, token_count, heading_path, start_offset, end_offset,
	prev_chunk_id, next_chunk_id, status, metadata_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`

func insertChunks(ctx context.Context, tx *sql.Tx, item Item, version int, chunks []chunkDraft, now string) error {
	stmt, err := tx.PrepareContext(ctx, insertChunkSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, chunk := range chunks {
		if err := insertChunkWithStatement(ctx, stmt, item, version, chunk, now); err != nil {
			return err
		}
	}
	return nil
}

func insertChunkWithStatement(ctx context.Context, stmt *sql.Stmt, item Item, version int, chunk chunkDraft, now string) error {
	if chunk.Metadata == nil {
		chunk.Metadata = map[string]any{}
	}
	_, err := stmt.ExecContext(ctx,
		chunk.ID,
		item.ID,
		version,
		item.KnowledgeBaseID,
		chunk.ParentID,
		chunk.Type,
		chunk.Index,
		chunk.Title,
		chunk.Content,
		chunk.ContentHash,
		chunk.TokenCount,
		chunk.HeadingPath,
		chunk.StartOffset,
		chunk.EndOffset,
		chunk.PrevID,
		chunk.NextID,
		mustKnowledgeJSON(chunk.Metadata),
		now,
		now,
	)
	return err
}

func (s *Store) deleteSQLite(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	var activeVersion int
	err := s.db.QueryRowContext(ctx, `SELECT active_version FROM documents WHERE id = ? AND status != 'deleted'`, id).Scan(&activeVersion)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := formatSQLiteTime(shared.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollbackQuietly(tx)
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET status = 'deleted', deleted_at = ?, updated_at = ? WHERE id = ?`, now, now, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_versions SET status = 'deleted' WHERE doc_id = ?`, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_chunks SET status = 'deleted', deleted_at = ?, updated_at = ? WHERE doc_id = ?`, now, now, id); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_chunk_vector_rows SET status = 'deleted', updated_at = ? WHERE doc_id = ? AND doc_version = ?`, now, id, activeVersion); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO index_outbox(id, event_type, doc_id, doc_version, payload_json, status, retry_count, error, created_at, updated_at)
		 VALUES (?, 'document.deleted', ?, ?, '{}', 'done', 0, '', ?, ?)`,
		shared.NewID("outbox"),
		id,
		activeVersion,
		now,
		now,
	); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if vector := s.vectorIndex(); vector != nil {
		if err := vector.DeleteDocument(ctx, id, activeVersion); err != nil {
			_ = s.recordOutbox(ctx, "vector.delete", id, activeVersion, "", map[string]any{"error": err.Error()}, "failed", err.Error())
		}
	}
	return true, nil
}

func (s *Store) indexVectors(ctx context.Context, item Item, version int, chunks []chunkDraft, vector VectorIndex, embedder EmbeddingProvider) error {
	if vector == nil || embedder == nil {
		return nil
	}
	children := []chunkDraft{}
	for _, chunk := range chunks {
		if chunk.Type != "child" || strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		children = append(children, chunk)
	}
	if len(children) == 0 {
		return nil
	}
	totalPoints := 0
	for start := 0; start < len(children); start += vectorIndexBatchSize {
		end := start + vectorIndexBatchSize
		if end > len(children) {
			end = len(children)
		}
		batch := children[start:end]
		texts := make([]string, 0, len(batch))
		for _, chunk := range batch {
			texts = append(texts, chunk.Content)
		}
		vectors, err := embedder.Embed(ctx, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(batch) {
			return fmt.Errorf("embedding count mismatch: got %d, want %d", len(vectors), len(batch))
		}
		points := make([]VectorPoint, 0, len(batch))
		for i, chunk := range batch {
			points = append(points, VectorPoint{
				ID:              chunk.ID,
				Vector:          vectors[i],
				ChunkID:         chunk.ID,
				DocID:           item.ID,
				DocVersion:      version,
				KnowledgeBaseID: item.KnowledgeBaseID,
				Title:           chunk.Title,
				ContentHash:     chunk.ContentHash,
				SourceType:      "upload",
				HeadingPath:     chunk.HeadingPath,
				Status:          "active",
				Metadata: map[string]any{
					"file_name": item.Name,
				},
			})
		}
		if err := vector.Upsert(ctx, points); err != nil {
			return err
		}
		totalPoints += len(points)
	}
	return s.recordOutbox(ctx, "vector.upsert", item.ID, version, "", map[string]any{"points": totalPoints}, "done", "")
}

func (s *Store) indexVectorsAsync(item Item, version int, chunks []chunkDraft) {
	vector, embedder := s.vectorDependencies()
	if vector == nil || embedder == nil {
		return
	}
	chunksCopy := append([]chunkDraft(nil), chunks...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := s.indexVectors(ctx, item, version, chunksCopy, vector, embedder); err != nil {
			_ = s.recordOutbox(ctx, "vector.upsert", item.ID, version, "", map[string]any{"error": err.Error()}, "failed", err.Error())
		}
	}()
}

func (s *Store) BackfillMissingVectorsAsync() {
	if s == nil || s.db == nil {
		return
	}
	vector, embedder := s.vectorDependencies()
	if vector == nil || embedder == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := s.backfillMissingVectors(ctx, vector, embedder); err != nil {
			_ = s.recordOutbox(ctx, "vector.backfill", "", 0, "", map[string]any{"error": err.Error()}, "failed", err.Error())
		}
	}()
}

func (s *Store) backfillMissingVectors(ctx context.Context, vector VectorIndex, embedder EmbeddingProvider) error {
	totalPoints := 0
	for {
		chunks, err := s.missingVectorChunks(ctx, vectorIndexBatchSize)
		if err != nil {
			return err
		}
		if len(chunks) == 0 {
			if totalPoints > 0 {
				return s.recordOutbox(ctx, "vector.backfill", "", 0, "", map[string]any{"points": totalPoints}, "done", "")
			}
			return nil
		}
		texts := make([]string, 0, len(chunks))
		for _, chunk := range chunks {
			texts = append(texts, chunk.Content)
		}
		vectors, err := embedder.Embed(ctx, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(chunks) {
			return fmt.Errorf("embedding count mismatch: got %d, want %d", len(vectors), len(chunks))
		}
		points := make([]VectorPoint, 0, len(chunks))
		for i, chunk := range chunks {
			points = append(points, VectorPoint{
				ID:              chunk.ID,
				Vector:          vectors[i],
				ChunkID:         chunk.ID,
				DocID:           chunk.DocID,
				DocVersion:      chunk.DocVersion,
				KnowledgeBaseID: chunk.KnowledgeBaseID,
				Title:           chunk.Title,
				ContentHash:     chunk.ContentHash,
				SourceType:      chunk.SourceType,
				HeadingPath:     chunk.HeadingPath,
				Status:          "active",
				Metadata:        chunk.Metadata,
			})
		}
		if err := vector.Upsert(ctx, points); err != nil {
			return err
		}
		totalPoints += len(points)
	}
}

func (s *Store) missingVectorChunks(ctx context.Context, limit int) ([]vectorBackfillChunk, error) {
	if limit <= 0 {
		limit = vectorIndexBatchSize
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.doc_id, c.doc_version, c.knowledge_base_id, c.title, c.content,
			c.content_hash, d.source_type, c.heading_path, c.metadata_json
		 FROM document_chunks c
		 JOIN documents d ON d.id = c.doc_id
		 LEFT JOIN document_chunk_vector_rows v ON v.chunk_id = c.id AND v.status = 'active'
		 WHERE c.status = 'active'
		   AND c.chunk_type = 'child'
		   AND d.status = 'active'
		   AND v.chunk_id IS NULL
		 ORDER BY d.updated_at DESC, c.chunk_index
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chunks := []vectorBackfillChunk{}
	for rows.Next() {
		var chunk vectorBackfillChunk
		var metadataJSON string
		if err := rows.Scan(
			&chunk.ID,
			&chunk.DocID,
			&chunk.DocVersion,
			&chunk.KnowledgeBaseID,
			&chunk.Title,
			&chunk.Content,
			&chunk.ContentHash,
			&chunk.SourceType,
			&chunk.HeadingPath,
			&metadataJSON,
		); err != nil {
			return nil, err
		}
		chunk.Metadata = map[string]any{}
		_ = json.Unmarshal([]byte(metadataJSON), &chunk.Metadata)
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func (s *Store) deleteVectorsAsync(docID string, version int) {
	vector := s.vectorIndex()
	if vector == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := vector.DeleteDocument(ctx, docID, version); err != nil {
			_ = s.recordOutbox(ctx, "vector.delete", docID, version, "", map[string]any{"error": err.Error()}, "failed", err.Error())
		}
	}()
}

func (s *Store) recordOutbox(ctx context.Context, eventType string, docID string, docVersion int, chunkID string, payload map[string]any, status string, message string) error {
	if s.db == nil {
		return nil
	}
	now := formatSQLiteTime(shared.Now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO index_outbox(id, event_type, doc_id, doc_version, chunk_id, payload_json, status, retry_count, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		shared.NewID("outbox"),
		eventType,
		docID,
		docVersion,
		chunkID,
		mustKnowledgeJSON(payload),
		status,
		message,
		now,
		now,
	)
	return err
}

func extractTextContent(ctx context.Context, data []byte, contentType string, name string, path string) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	ext := strings.ToLower(filepath.Ext(name))
	if contentType == "application/pdf" || ext == ".pdf" {
		return extractPDFText(ctx, data, name, path), nil
	}
	if isOfficeOpenXML(contentType, ext) {
		return extractOfficeOpenXMLText(data, name, contentType)
	}
	if isLegacyOfficeDocument(contentType, ext) {
		return "", fmt.Errorf("file %s is a legacy Microsoft Office binary document; convert it to docx, xlsx, pptx, PDF, or text before importing", name)
	}
	if strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		ext == ".md" || ext == ".markdown" || ext == ".txt" || ext == ".json" || ext == ".csv" || ext == ".yaml" || ext == ".yml" || ext == ".xml" {
		if !isLikelyText(data) {
			return "", fmt.Errorf("file %s is not valid text content", name)
		}
		return string(data), nil
	}
	if isLikelyText(data) {
		return string(data), nil
	}
	return "", fmt.Errorf("file %s cannot be indexed as text yet; supported formats include PDF with a text layer, docx, xlsx, pptx, markdown, txt, json, csv, yaml, xml, and other plain text files", name)
}

func isLikelyText(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func rollbackQuietly(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func normalizeKnowledgeBaseID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultKnowledgeBaseID
	}
	return value
}

func mustKnowledgeJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
