package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

const (
	// sqliteVecIndexName 是 vector_index_state 表里本索引的逻辑名（记录后端/维度/状态）。
	sqliteVecIndexName = "document_chunks"
	// sqliteVecTableName 是承载向量数据的 sqlite-vec 虚拟表名（vec0 表）。
	sqliteVecTableName = "document_chunk_vectors"
	// sqliteVecMetric 是相似度度量方式：余弦距离（适合归一化文本 embedding）。
	sqliteVecMetric = "cosine"
)

// SQLiteVecIndex 是 VectorIndex 接口基于 sqlite-vec 扩展的实现：把向量存进 SQLite 的
// vec0 虚拟表，与关键词索引共用同一个数据库文件，无需独立部署向量数据库。
// 内部用互斥锁串行化写操作，因为建表/维度切换/upsert 涉及多表联动，需保证一致性。
type SQLiteVecIndex struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSQLiteVecIndex 构造 sqlite-vec 向量索引；db 为 nil 时返回 nil（表示未启用向量能力）。
func NewSQLiteVecIndex(db *sql.DB) *SQLiteVecIndex {
	if db == nil {
		return nil
	}
	return &SQLiteVecIndex{db: db}
}

// Upsert 批量写入/覆盖向量点。核心设计是“元数据行 + 向量行”两张表配合：
//   - document_chunk_vector_rows：普通表，按 chunk_id 存元数据，自增主键 id 作为向量表的 rowid；
//   - vec0 虚拟表：只按 rowid 存 embedding blob（vec0 表不能直接 upsert，故先删后插）。
// 向量维度由本批首个点决定，并通过 ensureVectorTable 保证虚拟表按该维度建好；
// 单事务写入，保证元数据与向量同时生效或同时回滚。
func (i *SQLiteVecIndex) Upsert(ctx context.Context, points []VectorPoint) error {
	if i == nil || i.db == nil || len(points) == 0 {
		return nil
	}
	dimension := len(points[0].Vector)
	if dimension <= 0 {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.ensureVectorTable(ctx, dimension); err != nil {
		return err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackQuietly(tx)

	upsertMeta, err := tx.PrepareContext(ctx, `INSERT INTO document_chunk_vector_rows(
		chunk_id, doc_id, doc_version, knowledge_base_id, title, content_hash, source_type,
		heading_path, status, metadata_json, vector_dimension, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(chunk_id) DO UPDATE SET
		doc_id = excluded.doc_id,
		doc_version = excluded.doc_version,
		knowledge_base_id = excluded.knowledge_base_id,
		title = excluded.title,
		content_hash = excluded.content_hash,
		source_type = excluded.source_type,
		heading_path = excluded.heading_path,
		status = excluded.status,
		metadata_json = excluded.metadata_json,
		vector_dimension = excluded.vector_dimension,
		updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer upsertMeta.Close()

	deleteVector, err := tx.PrepareContext(ctx, `DELETE FROM `+sqliteVecTableName+` WHERE rowid = ?`)
	if err != nil {
		return err
	}
	defer deleteVector.Close()

	insertVector, err := tx.PrepareContext(ctx, `INSERT INTO `+sqliteVecTableName+`(rowid, embedding) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer insertVector.Close()

	now := formatSQLiteTime(shared.Now())
	for _, point := range points {
		// 跳过无 chunk_id 或维度不一致的点（同一批必须等维度，避免污染定长向量表）。
		if strings.TrimSpace(point.ChunkID) == "" || len(point.Vector) != dimension {
			continue
		}
		metadataJSON := mustKnowledgeJSON(point.Metadata)
		if _, err := upsertMeta.ExecContext(ctx,
			point.ChunkID,
			point.DocID,
			point.DocVersion,
			normalizeKnowledgeBaseID(point.KnowledgeBaseID),
			point.Title,
			point.ContentHash,
			point.SourceType,
			point.HeadingPath,
			normalizeVectorStatus(point.Status),
			metadataJSON,
			dimension,
			now,
			now,
		); err != nil {
			return err
		}
		// 取元数据行的自增 id 作为向量表 rowid，把两张表按 rowid 对齐。
		var rowID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM document_chunk_vector_rows WHERE chunk_id = ?`, point.ChunkID).Scan(&rowID); err != nil {
			return err
		}
		// 向量序列化为 sqlite-vec 要求的 float32 blob 格式。
		vectorBlob, err := sqlite_vec.SerializeFloat32(point.Vector)
		if err != nil {
			return err
		}
		// vec0 虚拟表不支持 upsert，故先按 rowid 删旧向量再插新向量。
		if _, err := deleteVector.ExecContext(ctx, rowID); err != nil {
			return err
		}
		if _, err := insertVector.ExecContext(ctx, rowID, vectorBlob); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteDocument 删除某文档指定版本的全部向量：先查出该文档版本对应的所有 rowid，
// 逐个从 vec0 虚拟表删向量，再删元数据行，单事务提交。表不存在时直接返回（无向量可删）。
func (i *SQLiteVecIndex) DeleteDocument(ctx context.Context, docID string, docVersion int) error {
	if i == nil || i.db == nil || strings.TrimSpace(docID) == "" {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if exists, err := i.vectorTableExists(ctx); err != nil || !exists {
		return err
	}
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackQuietly(tx)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM document_chunk_vector_rows WHERE doc_id = ? AND doc_version = ?`, docID, docVersion)
	if err != nil {
		return err
	}
	rowIDs := []int64{}
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			_ = rows.Close()
			return err
		}
		rowIDs = append(rowIDs, rowID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, rowID := range rowIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+sqliteVecTableName+` WHERE rowid = ?`, rowID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM document_chunk_vector_rows WHERE doc_id = ? AND doc_version = ?`, docID, docVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Search 用查询向量做 KNN 检索并叠加知识库/文档过滤。实现要点：
//   - 先校验查询向量维度与索引维度一致（不一致直接返回空，避免 sqlite-vec 报错）；
//   - vec0 的 KNN 只能在 MATCH ... AND k=? 子句里表达，且过滤条件会在 JOIN 之后生效，
//     因此当带元数据过滤时用 sqliteVecSearchK 放大 k（多召回候选），再 JOIN 元数据表过滤，
//     最后按距离升序 LIMIT，防止“过滤后不足 limit 条”；
//   - 距离经 sqliteVecDistanceScore 归一化成越大越相似的分数，并把元数据展开进 Payload。
func (i *SQLiteVecIndex) Search(ctx context.Context, vector []float32, filter RetrievalFilter, limit int) ([]VectorHit, error) {
	if i == nil || i.db == nil || len(vector) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 40
	}
	dimension, ok, err := i.currentDimension(ctx)
	if err != nil || !ok {
		return nil, err
	}
	if dimension != len(vector) {
		return nil, nil
	}
	if exists, err := i.vectorTableExists(ctx); err != nil || !exists {
		return nil, err
	}
	vectorBlob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, err
	}
	filter = normalizeRetrievalFilter(filter)
	where, args := sqliteVecFilterSQL("m", filter)
	searchK := sqliteVecSearchK(limit, filter)
	queryArgs := []any{vectorBlob, searchK}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)
	rows, err := i.db.QueryContext(ctx,
		`WITH knn_matches AS (
			SELECT rowid, distance
			FROM `+sqliteVecTableName+`
			WHERE embedding MATCH ? AND k = ?
		)
		SELECT m.chunk_id, m.doc_id, m.doc_version, m.knowledge_base_id, m.title,
			m.content_hash, m.source_type, m.heading_path, m.status, m.metadata_json, knn_matches.distance
		FROM knn_matches
		JOIN document_chunk_vector_rows m ON m.id = knn_matches.rowid
		WHERE m.status = 'active'`+where+`
		ORDER BY knn_matches.distance
		LIMIT ?`,
		queryArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []VectorHit{}
	for rows.Next() {
		var chunkID, docID, knowledgeBaseID, title, contentHash, sourceType, headingPath, status, metadataJSON string
		var docVersion int
		var distance float64
		if err := rows.Scan(&chunkID, &docID, &docVersion, &knowledgeBaseID, &title, &contentHash, &sourceType, &headingPath, &status, &metadataJSON, &distance); err != nil {
			return nil, err
		}
		payload := map[string]any{
			"chunk_id":          chunkID,
			"doc_id":            docID,
			"doc_version":       docVersion,
			"knowledge_base_id": knowledgeBaseID,
			"title":             title,
			"content_hash":      contentHash,
			"source_type":       sourceType,
			"heading_path":      headingPath,
			"status":            status,
			"distance":          distance,
		}
		var metadata map[string]any
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err == nil {
			for key, value := range metadata {
				payload[key] = value
			}
		}
		hits = append(hits, VectorHit{ChunkID: chunkID, Score: sqliteVecDistanceScore(distance), Payload: payload})
	}
	return hits, rows.Err()
}

// ensureVectorTable 幂等地保证 vec0 虚拟表按给定维度存在，并同步 vector_index_state：
//   - 若当前记录维度已等于目标维度且表存在，直接返回；
//   - 若维度发生变化（例如切换了 embedding 模型），必须删表重建：drop 虚拟表、清空元数据行、
//     清 state 记录（vec0 的向量维度在建表时固定，无法动态改，旧向量也与新模型不可比）；
//   - 建表后写入/更新 vector_index_state 记录本索引的后端、表名、维度、度量与状态。
func (i *SQLiteVecIndex) ensureVectorTable(ctx context.Context, dimension int) error {
	if dimension <= 0 {
		return nil
	}
	current, ok, err := i.currentDimension(ctx)
	if err != nil {
		return err
	}
	if ok && current == dimension {
		exists, err := i.vectorTableExists(ctx)
		if err != nil || exists {
			return err
		}
	}
	if ok && current != dimension {
		if _, err := i.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+sqliteVecTableName); err != nil {
			return err
		}
		if _, err := i.db.ExecContext(ctx, `DELETE FROM document_chunk_vector_rows`); err != nil {
			return err
		}
		if _, err := i.db.ExecContext(ctx, `DELETE FROM vector_index_state WHERE name = ?`, sqliteVecIndexName); err != nil {
			return err
		}
	}
	if _, err := i.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS %s USING vec0(embedding float[%d] distance_metric=cosine)`,
		sqliteVecTableName,
		dimension,
	)); err != nil {
		return err
	}
	now := formatSQLiteTime(shared.Now())
	_, err = i.db.ExecContext(ctx,
		`INSERT INTO vector_index_state(name, backend, vector_table, dimension, distance_metric, status, created_at, updated_at)
		 VALUES (?, 'sqlite-vec', ?, ?, ?, 'active', ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
			backend = 'sqlite-vec',
			vector_table = excluded.vector_table,
			dimension = excluded.dimension,
			distance_metric = excluded.distance_metric,
			status = 'active',
			updated_at = excluded.updated_at`,
		sqliteVecIndexName,
		sqliteVecTableName,
		dimension,
		sqliteVecMetric,
		now,
		now,
	)
	return err
}

// currentDimension 读取当前 active 的 sqlite-vec 索引维度；无记录时返回 (0,false,nil)。
func (i *SQLiteVecIndex) currentDimension(ctx context.Context) (int, bool, error) {
	var dimension int
	err := i.db.QueryRowContext(ctx, `SELECT dimension FROM vector_index_state WHERE name = ? AND backend = 'sqlite-vec' AND status = 'active'`, sqliteVecIndexName).Scan(&dimension)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return dimension, dimension > 0, nil
}

// vectorTableExists 通过 sqlite_master 判断 vec0 虚拟表是否已建。
func (i *SQLiteVecIndex) vectorTableExists(ctx context.Context) (bool, error) {
	var name string
	err := i.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE name = ?`, sqliteVecTableName).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return name == sqliteVecTableName, nil
}

// sqliteVecFilterSQL 把知识库/文档过滤条件拼成 SQL 片段（作用在元数据表别名 alias 上），
// 返回追加到 WHERE 后的 " AND ..." 字符串及对应参数。空过滤返回空串。
func sqliteVecFilterSQL(alias string, filter RetrievalFilter) (string, []any) {
	parts := []string{}
	args := []any{}
	if len(filter.KnowledgeBaseIDs) > 0 {
		placeholders := make([]string, 0, len(filter.KnowledgeBaseIDs))
		for _, id := range filter.KnowledgeBaseIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) > 0 {
			parts = append(parts, alias+".knowledge_base_id IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if len(filter.DocIDs) > 0 {
		placeholders := make([]string, 0, len(filter.DocIDs))
		for _, id := range filter.DocIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) > 0 {
			parts = append(parts, alias+".doc_id IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	if len(parts) == 0 {
		return "", args
	}
	return " AND " + strings.Join(parts, " AND "), args
}

// sqliteVecSearchK 计算传给 vec0 的 KNN 参数 k：
// 无过滤时 k 就等于 limit（KNN 结果直接够用）；带元数据过滤时，KNN 先于过滤执行，
// 若只取 limit 个近邻，过滤后可能剩不下几条，故把 k 放大 20 倍并夹在 [200, 2000] 区间，
// 用“多召回、后过滤”换取过滤场景下的召回充足性（同时限制上限避免开销过大）。
func sqliteVecSearchK(limit int, filter RetrievalFilter) int {
	if limit <= 0 {
		limit = 40
	}
	if len(filter.KnowledgeBaseIDs) == 0 && len(filter.DocIDs) == 0 {
		return limit
	}
	k := limit * 20
	if k < 200 {
		k = 200
	}
	if k > 2000 {
		k = 2000
	}
	return k
}

// sqliteVecDistanceScore 把“越小越近”的距离转成“越大越相似”的分数（1/(1+distance)），
// 落在 (0,1] 区间，便于与其他召回通道的分数统一比较/融合。
func sqliteVecDistanceScore(distance float64) float64 {
	if distance < 0 {
		return 0
	}
	return 1 / (1 + distance)
}

// normalizeVectorStatus 归一化向量行状态，空值默认为 "active"。
func normalizeVectorStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "active"
	}
	return status
}
