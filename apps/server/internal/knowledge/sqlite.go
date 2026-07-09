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

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// vectorIndexBatchSize 是 embedding/向量写入的批大小：每批最多 64 个 chunk 一起
// 调 Embed 与 Upsert，兼顾 embedding API 的批量效率与单请求体积限制。
const vectorIndexBatchSize = 64

// documentVersionRef 是“文档 ID + 当前激活版本号”的轻量引用，用于批量删除向量时定位范围。
type documentVersionRef struct {
	id      string
	version int
}

// ensureDefaultKnowledgeBase 幂等地保证默认知识库存在且处于 active 状态：
// 若曾被改名/软删除则恢复；仅在确有变化时才更新 updated_at，避免每次启动都刷新时间戳。
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

// ListKnowledgeBases 列出所有未删除的知识库，并联表统计每个库下 active 文档数、
// chunk 总数及父/子 chunk 数（只统计文档当前激活版本 active_version 的 chunk）。
// 排序规则：默认知识库永远排第一，其余按更新时间倒序。
// 无 SQLite 时返回一个内存中的默认知识库占位，保证上层 UI 不出错。
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

// CreateKnowledgeBase 新建一个知识库（ID 前缀 "kbbase"），名称必填。
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

// DeleteKnowledgeBase 删除一个知识库（默认知识库禁止删除）。
// 删除采用“软删除 + 异步清理向量”的两段式设计：
//  1. 在一个事务里把知识库、其下文档、文档版本、chunk、向量元数据行全部置为 deleted，
//     并往 index_outbox 写一条 knowledge_base.deleted 事件留痕；
//  2. 事务提交后，对每个文档异步调用 deleteVectorsAsync 真正清理 sqlite-vec 中的向量
//     （向量删除失败不影响主流程，仅记 outbox；检索侧靠 status='active' 过滤兜底）。
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

// activeDocumentVersionsForKnowledgeBase 取出某知识库下所有未删除文档的
// (id, active_version) 列表，供删除知识库后逐个清理向量使用。
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

// knowledgeBase 查询单个知识库详情（含 chunk 统计），不存在时返回 sql.ErrNoRows。
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

// ensureKnowledgeBaseExists 校验目标知识库存在且未删除；若目标是默认知识库
// 则顺手自愈（自动创建/恢复），其他知识库不存在时报错，防止文档写进悬空的库。
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

// listSQLite 是 SQLite 模式下的文档列表实现：联表 document_chunks 统计每个文档
// 当前激活版本的 active chunk 数（总数/子/父），按导入时间倒序返回。
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

// itemSQLite 查询单个文档详情（含 chunk 统计）；第二个返回值表示是否存在。
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

// migrateLegacyIndex 把旧版 index.json 清单里、但 documents 表中不存在的文档
// 迁移进 SQLite（重新走抽取→切块→索引全流程）。启动时执行一次。
// 单个文档迁移失败只记录 outbox（status=failed），不阻断其余文档与启动流程。
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

// repairLimitedPDFExtractions 修复历史上抽取失败的 PDF：
// 之前本机没有 pdftotext 时，无文本层的 PDF 只能写入 limitedPDFExtractionNotice 占位提示；
// 如果现在 pdftotext 可用了，就扫描所有 active 的 PDF 文档，检查其 parsed 文本里
// 是否仍含占位提示，命中则调用 reindexDocumentVersion 重新抽取并重建索引。
// 注意先把候选收集完再逐个修复，避免在遍历 rows 的同时执行写事务。
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

// cleanupIndexedFiles 在启动时清理“已成功索引”的文档的原始文件与解析文本：
// chunk 内容已完整存在 document_chunks 表里，原文件只在重抽取时才有用，
// 因此对抽取质量合格（不含 PDF 占位提示）的文档可以安全删除磁盘副本以节省空间。
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

// shouldDeleteStoredFilesAfterIndex 判断索引完成后能否删除磁盘文件：
// 抽取文本非空、且不含“PDF 无文本层”占位提示才允许删除——
// 含占位提示的 PDF 需要保留原文件，等 pdftotext 可用后重新抽取。
func shouldDeleteStoredFilesAfterIndex(parsed string) bool {
	parsed = strings.TrimSpace(parsed)
	return parsed != "" && !strings.Contains(parsed, limitedPDFExtractionNotice)
}

// cleanupStoredFiles 删除某文档版本的原始文件与解析文本，并把数据库里指向它们的
// 路径字段清空。安全约束：只删除位于本模块 files/、parsed/ 目录之下的文件
//（isKnowledgeRawFile / isKnowledgeParsedFile 校验），绝不触碰用户目录里的外部文件。
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

// isKnowledgeRawFile 判断路径是否位于本模块的原始文件目录（files/）内。
func (s *Store) isKnowledgeRawFile(path string) bool {
	return s.isPathUnder(path, s.filesDir())
}

// isKnowledgeParsedFile 判断路径是否位于本模块的解析文本目录（parsed/）内。
func (s *Store) isKnowledgeParsedFile(path string) bool {
	return s.isPathUnder(path, s.parsedDir())
}

// isPathUnder 通过绝对路径前缀比较判断 path 是否位于 dir 目录之下（防路径逃逸）。
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

// reindexDocumentVersion 对某个已存在的文档版本做“原地重建索引”（目前用于 PDF 修复）：
// 重新抽取文本→重新切块→在一个事务里删旧 chunk、更新版本/文档元信息、写入新 chunk，
// 并记录 ingestion_jobs（stage=pdf_reindex）与 outbox 事件（document.reindexed）。
// 若新抽取结果仍是空或仍含占位提示则直接放弃，保持旧数据不动。
// 提交后：符合条件则清理磁盘文件，并异步“先删旧向量、再建新向量”。
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
		// 兜底：正文切不出 chunk 时，至少用文件名生成一个 chunk，保证文档可被检索到。
		chunks = chunkDocument(item.Name, item.Name)
	}
	now := formatSQLiteTime(shared.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackQuietly(tx)
	// 物理删除该版本的旧 chunk（FTS5 索引由 document_chunks 上的 AFTER DELETE 触发器同步清理）。
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
	// 向量索引采用“先删后建”而非原地更新：chunk ID 已全部换新，旧向量必须整体清掉。
	s.deleteVectorsAsync(item.ID, version)
	s.indexVectorsAsync(item, version, chunks)
	return nil
}

// documentExists 判断 documents 表中是否已有该 ID（含已删除的），用于旧清单迁移防重。
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

// resolveLegacyItemPath 为旧清单条目定位原始文件：
// 优先按 "<docID>_*" 前缀在 files/ 目录里 glob；找不到再看清单里记录的 Path——
// 若该 Path 在 files/ 之外（老版本可能存在别处），则把文件拷贝进 files/ 统一管理后返回新路径。
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

// indexImportedItem 是导入后的“建索引”主流程（导入管线的核心）：
//  1. 读原始文件并按格式抽取纯文本（extractTextContent：PDF/OOXML/纯文本）；
//     抽取结果为空时退化为用文件名当内容，保证至少标题可检索；
//  2. 把解析文本写入 parsed/<docID>_v1.txt（新导入固定为版本 1）；
//  3. chunkDocument 做父子分层切块；
//  4. 单事务写入：documents、document_versions、ingestion_jobs（stage=index, status=done）、
//     全部 chunk（FTS5 由触发器自动同步）、以及 outbox 事件 document.indexed；
//  5. 提交后：若抽取质量合格则清理磁盘文件，并异步生成 embedding 写入向量索引
//     （向量化不阻塞导入请求，失败只记 outbox，可由 Backfill 兜底重建）。
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

// contentTypeFromName 按扩展名推断 MIME 类型，用于旧清单迁移时补全缺失的 content_type。
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

// insertChunkSQL 是写入 document_chunks 表的插入语句。
// document_chunks 是 chunk 的权威存储：父/子 chunk 同表存放（chunk_type 区分），
// 其 title/content/heading_path 三列通过触发器同步进 FTS5 外部内容表 document_chunks_fts。
const insertChunkSQL = `INSERT INTO document_chunks(
	id, doc_id, doc_version, knowledge_base_id, parent_chunk_id, chunk_type, chunk_index,
	title, content, content_hash, token_count, heading_path, start_offset, end_offset,
	prev_chunk_id, next_chunk_id, status, metadata_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`

// insertChunks 在事务内用预编译语句批量插入一批 chunk（父 + 子）。
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

// insertChunkWithStatement 插入单个 chunk，Metadata 序列化为 JSON 存入 metadata_json 列。
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

// deleteSQLite 删除单个文档：单事务内软删除 documents / document_versions /
// document_chunks / document_chunk_vector_rows，并写入 document.deleted 事件；
// 提交后同步调用向量后端删除该文档激活版本的向量，失败仅记 outbox 不回滚
//（元数据行已标记 deleted，检索时会被 status 过滤掉，残留向量无害）。
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

// indexVectors 为一批 chunk 生成 embedding 并写入向量索引。
// 关键设计：只对 child chunk 做向量化——child 粒度小、语义集中，召回更精准；
// parent chunk 不进向量索引，只在检索后展开阶段提供长上下文。
// 按 vectorIndexBatchSize 分批 Embed + Upsert，全部成功后写一条 vector.upsert 的 outbox 记录。
func (s *Store) indexVectors(ctx context.Context, item Item, version int, chunks []chunkDraft, vector VectorIndex, embedder EmbeddingProvider) error {
	if vector == nil || embedder == nil {
		return nil
	}
	// 过滤出内容非空的 child chunk（parent 与空 chunk 不做向量化）。
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

// indexVectorsAsync 在后台 goroutine 中执行向量化，避免 embedding API 的耗时
// 阻塞导入请求。整体超时 30 分钟（大文档可能有成百上千个 chunk），
// 失败以 vector.upsert/failed 记入 outbox，后续可通过 BackfillMissingVectorsAsync 补建。
func (s *Store) indexVectorsAsync(item Item, version int, chunks []chunkDraft) {
	vector, embedder := s.vectorDependencies()
	if vector == nil || embedder == nil {
		return
	}
	// 拷贝一份 chunk 切片，避免与调用方后续对底层数组的修改产生数据竞争。
	chunksCopy := append([]chunkDraft(nil), chunks...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if err := s.indexVectors(ctx, item, version, chunksCopy, vector, embedder); err != nil {
			_ = s.recordOutbox(ctx, "vector.upsert", item.ID, version, "", map[string]any{"error": err.Error()}, "failed", err.Error())
		}
	}()
}

// BackfillMissingVectorsAsync 异步补建缺失的向量：扫描所有“有 chunk 但无向量记录”的
// child chunk，重新 embedding 并写入向量索引。典型触发时机：服务启动后、
// 或用户刚配置好 embedding 模型时（此前导入的文档没有向量）。
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

// backfillMissingVectors 循环分批处理缺向量的 chunk：每轮取一批（64 个）→ Embed →
// Upsert，直到 missingVectorChunks 返回空。Upsert 成功后 document_chunk_vector_rows
// 里就有了记录，下一轮查询自然不会再取到同一批，因此无需显式游标。
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

// missingVectorChunks 找出“该有向量但还没有”的 child chunk：
// 用 LEFT JOIN document_chunk_vector_rows 且要求 v.chunk_id IS NULL（反连接），
// 即 chunk 与文档均 active、但向量元数据表里没有 active 记录的那些 chunk。
// 按文档更新时间倒序，优先补最新的文档。
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

// deleteVectorsAsync 在后台删除某文档版本的全部向量（超时 2 分钟），失败记 outbox。
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

// recordOutbox 往 index_outbox 表写一条事件记录。该表是简化版 outbox 模式：
// 当前主要作为索引/向量操作的审计与失败留痕（status=done/failed），
// 便于排障和将来实现失败重试，写入失败不会影响主流程。
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

// extractTextContent 是文本抽取的统一分发入口，按 MIME 类型 + 扩展名路由：
//   - PDF → extractPDFText（优先 pdftotext，回退内置流解析）；
//   - Office Open XML（docx/xlsx/pptx 及宏/模板变体）→ extractOfficeOpenXMLText；
//   - 旧版二进制 Office（doc/xls/ppt）→ 明确报错，提示用户先转换格式；
//   - 文本类（text/*、json、xml、md、csv、yaml…）→ 校验无 NUL 字节后原样返回；
//   - 其他未知类型 → 若整体看起来像文本则收下，否则报“暂不支持”错误。
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
