package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"code.byted.org/ai/lmy/apps/server/internal/shared"
)

func (s *Store) Retrieve(ctx context.Context, query string, options RetrievalOptions) (RetrievalResult, error) {
	result := RetrievalResult{Query: query, Filter: normalizeRetrievalFilter(options.Filter)}
	if s == nil || s.db == nil || strings.TrimSpace(query) == "" {
		return result, nil
	}
	options = normalizeRetrievalOptions(options)
	options.Filter = result.Filter

	metadataResults, _ := s.metadataRecall(ctx, query, options)
	keywordResults, _ := s.keywordRecall(ctx, query, options)
	vectorCtx, cancelVector := context.WithTimeout(ctx, 3*time.Second)
	vectorResults, _ := s.vectorRecall(vectorCtx, query, options)
	cancelVector()
	merged := mergeRetrievedChunks(keywordResults, vectorResults, metadataResults)
	reranked := diversifyRetrievedChunks(expandParentChunks(ctx, s.db, merged), options.TopK)

	result.MetadataResults = metadataResults
	result.KeywordResults = keywordResults
	result.VectorResults = vectorResults
	result.MergedResults = merged
	result.RerankedResults = reranked
	normalizeRetrievalResultSlices(&result)
	_ = s.recordRetrievalLog(ctx, options.ConversationID, result)
	return result, nil
}

func normalizeRetrievalResultSlices(result *RetrievalResult) {
	if result.KeywordResults == nil {
		result.KeywordResults = []RetrievedChunk{}
	}
	if result.VectorResults == nil {
		result.VectorResults = []RetrievedChunk{}
	}
	if result.MetadataResults == nil {
		result.MetadataResults = []RetrievedChunk{}
	}
	if result.MergedResults == nil {
		result.MergedResults = []RetrievedChunk{}
	}
	if result.RerankedResults == nil {
		result.RerankedResults = []RetrievedChunk{}
	}
}

func normalizeRetrievalOptions(options RetrievalOptions) RetrievalOptions {
	if options.KeywordLimit <= 0 {
		options.KeywordLimit = 40
	}
	if options.VectorLimit <= 0 {
		options.VectorLimit = 40
	}
	if options.MetadataLimit <= 0 {
		options.MetadataLimit = 20
	}
	if options.TopK <= 0 {
		options.TopK = 8
	}
	options.Filter = normalizeRetrievalFilter(options.Filter)
	return options
}

func normalizeRetrievalFilter(filter RetrievalFilter) RetrievalFilter {
	clean := make([]string, 0, len(filter.KnowledgeBaseIDs))
	for _, id := range filter.KnowledgeBaseIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			clean = append(clean, normalizeKnowledgeBaseID(id))
		}
	}
	filter.KnowledgeBaseIDs = clean
	return filter
}

func (s *Store) keywordRecall(ctx context.Context, query string, options RetrievalOptions) ([]RetrievedChunk, error) {
	fts := buildFTSQuery(query)
	if fts == "" {
		return nil, nil
	}
	where, args := retrievalWhereSQL("c", options.Filter)
	args = append([]any{fts}, args...)
	args = append(args, options.KeywordLimit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.doc_id, c.doc_version, c.knowledge_base_id, c.parent_chunk_id, c.chunk_index,
			c.title, c.content, c.content_hash, c.token_count, c.heading_path, d.source_uri, d.source_type,
			c.metadata_json, bm25(document_chunks_fts) AS score
		 FROM document_chunks_fts
		 JOIN document_chunks c ON c.rowid = document_chunks_fts.rowid
		 JOIN documents d ON d.id = c.doc_id
		 WHERE document_chunks_fts MATCH ?
		   AND c.status = 'active'
		   AND c.chunk_type = 'child'
		   AND d.status = 'active'`+where+`
		 ORDER BY score
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRetrievedRows(rows, "keyword")
}

func (s *Store) metadataRecall(ctx context.Context, query string, options RetrievalOptions) ([]RetrievedChunk, error) {
	terms := searchTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	where, args := retrievalWhereSQL("c", options.Filter)
	likeParts := []string{}
	for _, term := range terms {
		pattern := "%" + term + "%"
		likeParts = append(likeParts, "(c.title LIKE ? OR c.heading_path LIKE ? OR d.title LIKE ? OR d.source_uri LIKE ?)")
		args = append(args, pattern, pattern, pattern, pattern)
	}
	args = append(args, options.MetadataLimit)
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.doc_id, c.doc_version, c.knowledge_base_id, c.parent_chunk_id, c.chunk_index,
			c.title, c.content, c.content_hash, c.token_count, c.heading_path, d.source_uri, d.source_type,
			c.metadata_json, 1.0 AS score
		 FROM document_chunks c
		 JOIN documents d ON d.id = c.doc_id
		 WHERE c.status = 'active'
		   AND c.chunk_type = 'child'
		   AND d.status = 'active'`+where+`
		   AND (`+strings.Join(likeParts, " OR ")+`)
		 ORDER BY d.updated_at DESC, c.chunk_index
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRetrievedRows(rows, "metadata")
}

func (s *Store) vectorRecall(ctx context.Context, query string, options RetrievalOptions) ([]RetrievedChunk, error) {
	vector, embedder := s.vectorDependencies()
	if vector == nil || embedder == nil {
		return nil, nil
	}
	vectors, err := embedder.Embed(ctx, []string{query})
	if err != nil || len(vectors) == 0 {
		return nil, err
	}
	hits, err := vector.Search(ctx, vectors[0], options.Filter, options.VectorLimit)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(hits))
	scoreByID := map[string]float64{}
	for _, hit := range hits {
		if strings.TrimSpace(hit.ChunkID) == "" {
			continue
		}
		ids = append(ids, hit.ChunkID)
		scoreByID[hit.ChunkID] = hit.Score
	}
	chunks, err := s.chunksByIDs(ctx, ids, "vector")
	if err != nil {
		return nil, err
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		return scoreByID[chunks[i].ID] > scoreByID[chunks[j].ID]
	})
	for i := range chunks {
		if chunks[i].Scores == nil {
			chunks[i].Scores = map[string]float64{}
		}
		chunks[i].Scores["vector_score"] = scoreByID[chunks[i].ID]
	}
	return chunks, nil
}

func retrievalWhereSQL(alias string, filter RetrievalFilter) (string, []any) {
	parts := []string{}
	args := []any{}
	if len(filter.KnowledgeBaseIDs) > 0 {
		placeholders := make([]string, 0, len(filter.KnowledgeBaseIDs))
		for _, id := range filter.KnowledgeBaseIDs {
			if strings.TrimSpace(id) == "" {
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
			if strings.TrimSpace(id) == "" {
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

func (s *Store) chunksByIDs(ctx context.Context, ids []string, source string) ([]RetrievedChunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.doc_id, c.doc_version, c.knowledge_base_id, c.parent_chunk_id, c.chunk_index,
			c.title, c.content, c.content_hash, c.token_count, c.heading_path, d.source_uri, d.source_type,
			c.metadata_json, 1.0 AS score
		 FROM document_chunks c
		 JOIN documents d ON d.id = c.doc_id
		 WHERE c.id IN (`+strings.Join(placeholders, ",")+`)
		   AND c.status = 'active'
		   AND c.chunk_type = 'child'
		   AND d.status = 'active'`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chunks, err := scanRetrievedRows(rows, source)
	if err != nil {
		return nil, err
	}
	byID := map[string]RetrievedChunk{}
	for _, chunk := range chunks {
		byID[chunk.ID] = chunk
	}
	ordered := []RetrievedChunk{}
	for _, id := range ids {
		if chunk, ok := byID[id]; ok {
			ordered = append(ordered, chunk)
		}
	}
	return ordered, nil
}

func scanRetrievedRows(rows *sql.Rows, source string) ([]RetrievedChunk, error) {
	chunks := []RetrievedChunk{}
	for rows.Next() {
		var chunk RetrievedChunk
		var metadataJSON string
		var score float64
		if err := rows.Scan(
			&chunk.ID,
			&chunk.DocID,
			&chunk.DocVersion,
			&chunk.KnowledgeBaseID,
			&chunk.ParentChunkID,
			&chunk.ChunkIndex,
			&chunk.Title,
			&chunk.Content,
			&chunk.ContentHash,
			&chunk.TokenCount,
			&chunk.HeadingPath,
			&chunk.SourceURI,
			&chunk.SourceType,
			&metadataJSON,
			&score,
		); err != nil {
			return nil, err
		}
		chunk.Metadata = map[string]any{}
		_ = json.Unmarshal([]byte(metadataJSON), &chunk.Metadata)
		chunk.Scores = map[string]float64{source + "_score": score}
		chunk.Sources = []string{source}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func mergeRetrievedChunks(groups ...[]RetrievedChunk) []RetrievedChunk {
	byKey := map[string]*RetrievedChunk{}
	order := []string{}
	for _, group := range groups {
		for rank, chunk := range group {
			key := dedupeKey(chunk)
			existing, ok := byKey[key]
			if !ok {
				copyChunk := chunk
				copyChunk.Scores = copyScores(chunk.Scores)
				copyChunk.Scores["rrf"] = 0
				copyChunk.Sources = append([]string(nil), chunk.Sources...)
				byKey[key] = &copyChunk
				order = append(order, key)
				existing = &copyChunk
			}
			if existing.Scores == nil {
				existing.Scores = map[string]float64{}
			}
			existing.Scores["rrf"] += 1 / (60 + float64(rank+1))
			for name, score := range chunk.Scores {
				if current, ok := existing.Scores[name]; !ok || betterScore(name, score, current) {
					existing.Scores[name] = score
				}
			}
			existing.Sources = mergeSources(existing.Sources, chunk.Sources)
		}
	}
	out := make([]RetrievedChunk, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Scores["rrf"] > out[j].Scores["rrf"]
	})
	return out
}

func expandParentChunks(ctx context.Context, db *sql.DB, chunks []RetrievedChunk) []RetrievedChunk {
	out := make([]RetrievedChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.ParentChunkID) == "" {
			out = append(out, chunk)
			continue
		}
		parent, ok := fetchParentChunk(ctx, db, chunk.ParentChunkID)
		if ok {
			parent.ID = chunk.ID
			parent.ParentChunkID = chunk.ParentChunkID
			parent.Scores = chunk.Scores
			parent.Sources = chunk.Sources
			parent.Metadata = chunk.Metadata
			out = append(out, parent)
			continue
		}
		out = append(out, chunk)
	}
	return out
}

func fetchParentChunk(ctx context.Context, db *sql.DB, parentID string) (RetrievedChunk, bool) {
	var chunk RetrievedChunk
	var metadataJSON string
	err := db.QueryRowContext(ctx,
		`SELECT c.id, c.doc_id, c.doc_version, c.knowledge_base_id, c.parent_chunk_id, c.chunk_index,
			c.title, c.content, c.content_hash, c.token_count, c.heading_path, d.source_uri, d.source_type,
			c.metadata_json
		 FROM document_chunks c
		 JOIN documents d ON d.id = c.doc_id
		 WHERE c.id = ? AND c.status = 'active' AND d.status = 'active'`,
		parentID,
	).Scan(
		&chunk.ID,
		&chunk.DocID,
		&chunk.DocVersion,
		&chunk.KnowledgeBaseID,
		&chunk.ParentChunkID,
		&chunk.ChunkIndex,
		&chunk.Title,
		&chunk.Content,
		&chunk.ContentHash,
		&chunk.TokenCount,
		&chunk.HeadingPath,
		&chunk.SourceURI,
		&chunk.SourceType,
		&metadataJSON,
	)
	if err != nil {
		return RetrievedChunk{}, false
	}
	chunk.Metadata = map[string]any{}
	_ = json.Unmarshal([]byte(metadataJSON), &chunk.Metadata)
	return chunk, true
}

func diversifyRetrievedChunks(chunks []RetrievedChunk, topK int) []RetrievedChunk {
	if topK <= 0 {
		topK = 8
	}
	out := []RetrievedChunk{}
	docCount := map[string]int{}
	contentSeen := map[string]struct{}{}
	for _, chunk := range chunks {
		if chunk.ContentHash != "" {
			if _, ok := contentSeen[chunk.ContentHash]; ok {
				continue
			}
			contentSeen[chunk.ContentHash] = struct{}{}
		}
		if docCount[chunk.DocID] >= 3 && len(out) >= 3 {
			continue
		}
		out = append(out, chunk)
		docCount[chunk.DocID]++
		if len(out) >= topK {
			break
		}
	}
	return out
}

func dedupeKey(chunk RetrievedChunk) string {
	if strings.TrimSpace(chunk.ParentChunkID) != "" {
		return "parent:" + chunk.ParentChunkID
	}
	if strings.TrimSpace(chunk.ContentHash) != "" {
		return "hash:" + chunk.ContentHash
	}
	return "chunk:" + chunk.ID
}

func copyScores(scores map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for name, score := range scores {
		out[name] = score
	}
	return out
}

func betterScore(name string, next float64, current float64) bool {
	if strings.Contains(name, "bm25") || strings.Contains(name, "keyword") {
		return next < current
	}
	return next > current || math.Abs(next-current) < 1e-9
}

func mergeSources(left []string, right []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range append(left, right...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func buildFTSQuery(query string) string {
	terms := searchTerms(query)
	if len(terms) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.ReplaceAll(term, `"`, `""`)
		if term != "" {
			quoted = append(quoted, `"`+term+`"`)
		}
	}
	return strings.Join(quoted, " OR ")
}

func searchTerms(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	terms := []string{}
	var current strings.Builder
	flush := func() {
		term := strings.TrimSpace(current.String())
		current.Reset()
		if len([]rune(term)) >= 2 {
			terms = append(terms, term)
		}
	}
	for _, r := range query {
		if r == '_' || r == '-' || r == '/' || r == '.' || r == ':' || r == '#' || r == '@' {
			current.WriteRune(r)
			continue
		}
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r >= 128 {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	if len(terms) == 0 && len([]rune(query)) >= 2 {
		terms = append(terms, query)
	}
	if len(terms) > 8 {
		terms = terms[:8]
	}
	return terms
}

func (s *Store) recordRetrievalLog(ctx context.Context, conversationID string, result RetrievalResult) error {
	if s.db == nil {
		return nil
	}
	now := formatSQLiteTime(shared.Now())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO retrieval_logs(id, conversation_id, query, metadata_filter_json, keyword_results_json, vector_results_json, merged_results_json, reranked_results_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		shared.NewID("retrieval"),
		conversationID,
		result.Query,
		mustKnowledgeJSON(result.Filter),
		mustKnowledgeJSON(slimChunks(result.KeywordResults)),
		mustKnowledgeJSON(slimChunks(result.VectorResults)),
		mustKnowledgeJSON(slimChunks(result.MergedResults)),
		mustKnowledgeJSON(slimChunks(result.RerankedResults)),
		now,
	)
	return err
}

func slimChunks(chunks []RetrievedChunk) []map[string]any {
	out := make([]map[string]any, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, map[string]any{
			"id":      chunk.ID,
			"doc_id":  chunk.DocID,
			"title":   chunk.Title,
			"sources": chunk.Sources,
			"scores":  chunk.Scores,
			"token":   chunk.TokenCount,
			"heading": chunk.HeadingPath,
			"hash":    chunk.ContentHash,
		})
	}
	return out
}
