package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"code.byted.org/ai/lmy/apps/server/internal/shared"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

const (
	sqliteVecIndexName = "document_chunks"
	sqliteVecTableName = "document_chunk_vectors"
	sqliteVecMetric    = "cosine"
)

type SQLiteVecIndex struct {
	db *sql.DB
	mu sync.Mutex
}

func NewSQLiteVecIndex(db *sql.DB) *SQLiteVecIndex {
	if db == nil {
		return nil
	}
	return &SQLiteVecIndex{db: db}
}

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
		var rowID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM document_chunk_vector_rows WHERE chunk_id = ?`, point.ChunkID).Scan(&rowID); err != nil {
			return err
		}
		vectorBlob, err := sqlite_vec.SerializeFloat32(point.Vector)
		if err != nil {
			return err
		}
		if _, err := deleteVector.ExecContext(ctx, rowID); err != nil {
			return err
		}
		if _, err := insertVector.ExecContext(ctx, rowID, vectorBlob); err != nil {
			return err
		}
	}
	return tx.Commit()
}

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

func sqliteVecDistanceScore(distance float64) float64 {
	if distance < 0 {
		return 0
	}
	return 1 / (1 + distance)
}

func normalizeVectorStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "active"
	}
	return status
}
