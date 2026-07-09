package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// Retrieve 是混合检索的对外主入口，执行完整的三路召回 + 融合 + 重排链路：
//  1. 三路召回：metadataRecall（标题/路径 LIKE）、keywordRecall（FTS5 BM25）、
//     vectorRecall（sqlite-vec KNN，单独设 3 秒超时以免慢的 embedding/向量搜索拖累整体）；
//  2. mergeRetrievedChunks 用 RRF（Reciprocal Rank Fusion）把三路结果融合去重；
//  3. rerankRetrievedChunks 展开父 chunk、做多样性重排，截断到 TopK；
//  4. 保留各阶段中间结果（便于前端展示召回链路），并把本次检索写入 retrieval_logs。
// 单路召回出错只被忽略（返回空），不影响其余通道，保证检索的鲁棒性。
func (s *Store) Retrieve(ctx context.Context, query string, options RetrievalOptions) (RetrievalResult, error) {
	result := RetrievalResult{Query: query, Filter: normalizeRetrievalFilter(options.Filter)}
	if s == nil || s.db == nil || strings.TrimSpace(query) == "" {
		return result, nil
	}
	options = normalizeRetrievalOptions(options)
	options.Filter = result.Filter

	metadataResults, _ := s.metadataRecall(ctx, query, options)
	keywordResults, _ := s.keywordRecall(ctx, query, options)
	// 向量召回单独限时 3 秒：embedding 与 KNN 可能较慢，超时则本路为空，其他两路仍可返回结果。
	vectorCtx, cancelVector := context.WithTimeout(ctx, 3*time.Second)
	vectorResults, _ := s.vectorRecall(vectorCtx, query, options)
	cancelVector()
	merged := mergeRetrievedChunks(keywordResults, vectorResults, metadataResults)
	reranked := rerankRetrievedChunks(ctx, s.db, merged, options.TopK)

	result.MetadataResults = metadataResults
	result.KeywordResults = keywordResults
	result.VectorResults = vectorResults
	result.MergedResults = merged
	result.RerankedResults = reranked
	normalizeRetrievalResultSlices(&result)
	_ = s.recordRetrievalLog(ctx, options.ConversationID, result)
	return result, nil
}

// normalizeRetrievalResultSlices 把结果里的各 nil 切片替换为空切片，
// 保证 JSON 序列化出 [] 而非 null，前端处理更稳。
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

// normalizeRetrievalOptions 为未设置的检索参数填默认值：
// 关键词/向量候选各 40、元数据候选 20、最终 TopK 为 8，并归一化过滤条件。
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

// normalizeRetrievalFilter 清洗过滤条件：去空白、丢弃空串，并把知识库 ID 归一化
//（空知识库 ID 归为 default）。三路召回共用清洗后的 filter。
func normalizeRetrievalFilter(filter RetrievalFilter) RetrievalFilter {
	cleanBaseIDs := make([]string, 0, len(filter.KnowledgeBaseIDs))
	for _, id := range filter.KnowledgeBaseIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			cleanBaseIDs = append(cleanBaseIDs, normalizeKnowledgeBaseID(id))
		}
	}
	filter.KnowledgeBaseIDs = cleanBaseIDs

	cleanDocIDs := make([]string, 0, len(filter.DocIDs))
	for _, id := range filter.DocIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			cleanDocIDs = append(cleanDocIDs, id)
		}
	}
	filter.DocIDs = cleanDocIDs
	return filter
}

// keywordRecall 是关键词召回：把查询词构造成 FTS5 MATCH 表达式，在 document_chunks_fts
// 外部内容表上做全文检索，用 bm25() 打分（分数越小越相关，故 ORDER BY score 升序）。
// 只召回 active 的 child chunk（child 粒度小、召回更精确），并叠加知识库/文档过滤。
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

// metadataRecall 是元数据召回：对每个查询词在 chunk 标题、标题路径、文档标题、来源 URI 上
// 做 LIKE '%term%' 匹配（词间 OR）。它补充 FTS5 覆盖不到的场景（如按文件名/标题命中），
// 统一给固定分 1.0，按文档更新时间倒序取前 MetadataLimit 条。
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

// vectorRecall 是向量（语义）召回：把 query 用 embedder 编码成向量，交给向量索引做 KNN，
// 得到命中的 chunk_id 及相似度分数；再回 document_chunks 表按这些 ID 取出完整 chunk
// （向量表只存元数据不存正文），并按向量分数从高到低排序、写入 vector_score。
// 未配置 embedder/向量索引时直接返回空（退化为纯关键词+元数据检索）。
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

// retrievalWhereSQL 把知识库/文档过滤条件拼成追加在 WHERE 后的 SQL 片段（作用于别名 alias），
// 供关键词/元数据召回共用；空过滤返回空串。
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

// chunksByIDs 按一批 chunk ID 从 document_chunks 取出完整 child chunk（含正文/元数据），
// 主要供向量召回回表使用，并按传入 ids 的顺序重排结果（保持向量相似度排序）。
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

// scanRetrievedRows 把查询结果行扫描成 RetrievedChunk：解析 metadata_json，
// 并为该 chunk 打上来源标签（source）与对应的 "<source>_score" 分数。
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

// mergeRetrievedChunks 用 RRF（Reciprocal Rank Fusion，倒数排名融合）把多路召回结果合并去重。
// RRF 的思想：一个 chunk 在某路中排名越靠前（rank 越小）贡献越大，得分累加 1/(60+rank+1)
//（常数 60 是 RRF 惯用平滑项，削弱头部名次的过度主导）。融合的好处是无需对不同量纲的
// 分数（bm25 vs 余弦 vs 固定 1.0）做归一化，只看各自的相对排名即可跨通道比较。
// 去重键由 dedupeKey 决定（同父 chunk 或同内容哈希视为一条），命中多路时累加 rrf、
// 合并来源标签、并保留各通道的最优单项分（betterScore）。最后按 rrf 降序输出。
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
			// RRF 累加：该 chunk 在本路中排第 rank+1 名，贡献 1/(60+名次)。
			existing.Scores["rrf"] += 1 / (60 + float64(rank+1))
			// 同一 chunk 被多路命中时，保留每类单项分中“更优”的那个（bm25 越小越好，其余越大越好）。
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

// expandParentChunks 把命中的 child chunk 展开为其 parent chunk 的长上下文：
// 对每个有 ParentChunkID 的命中，取出 parent，用 parent 的正文/标题/偏移替换 child，
// 但保留 child 的 ID、命中来源与分数（即“召回靠 child 精确、喂给 LLM 用 parent 完整”）。
// parent 取不到（如已删除）时退回原 child。这是父子分层检索的关键收益环节。
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

// rerankRetrievedChunks 是内置的“重排”实现（无外部 reranker 模型）：
// 先按 RRF 顺序展开父 chunk，再做多样性截断到 TopK，最后写入 rerank_score
//（用倒序名次 len-i 作为分值，纯粹表达最终名次，越靠前分越高）。
func rerankRetrievedChunks(ctx context.Context, db *sql.DB, chunks []RetrievedChunk, topK int) []RetrievedChunk {
	expanded := expandParentChunks(ctx, db, chunks)
	reranked := diversifyRetrievedChunks(expanded, topK)
	for i := range reranked {
		if reranked[i].Scores == nil {
			reranked[i].Scores = map[string]float64{}
		}
		reranked[i].Scores["rerank_score"] = float64(len(reranked) - i)
	}
	return reranked
}

// fetchParentChunk 按 ID 取一个 active 的 parent chunk（连同文档来源信息）；不存在返回 false。
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

// diversifyRetrievedChunks 做多样性重排/截断，避免最终结果被单一文档或重复内容霸占：
//   - 按内容哈希去重（父 chunk 展开后相邻 child 常指向同一 parent，会产生重复正文）；
//   - 每个文档最多贡献 3 条（在已凑够 3 条结果后才开始限制，保证少文档场景不被误伤）；
//   - 累计到 topK 条即停止。输入已按 RRF 名次有序，这里按序贪心挑选。
func diversifyRetrievedChunks(chunks []RetrievedChunk, topK int) []RetrievedChunk {
	if topK <= 0 {
		topK = 8
	}
	out := []RetrievedChunk{}
	docCount := map[string]int{}
	contentSeen := map[string]struct{}{}
	for _, chunk := range chunks {
		// 内容哈希去重：父展开后的重复正文只保留一条。
		if chunk.ContentHash != "" {
			if _, ok := contentSeen[chunk.ContentHash]; ok {
				continue
			}
			contentSeen[chunk.ContentHash] = struct{}{}
		}
		// 单文档配额：已有 >=3 条结果后，同一文档超过 3 条则跳过，促进跨文档多样性。
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

// dedupeKey 为融合去重生成键：优先按父 chunk（同一 parent 下的不同 child 视为一条，
// 因为最终都会展开成同一段父上下文），其次按内容哈希，最后退化为 chunk ID。
func dedupeKey(chunk RetrievedChunk) string {
	if strings.TrimSpace(chunk.ParentChunkID) != "" {
		return "parent:" + chunk.ParentChunkID
	}
	if strings.TrimSpace(chunk.ContentHash) != "" {
		return "hash:" + chunk.ContentHash
	}
	return "chunk:" + chunk.ID
}

// copyScores 深拷贝分数 map，避免融合时改到原召回结果的分数。
func copyScores(scores map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for name, score := range scores {
		out[name] = score
	}
	return out
}

// betterScore 判断某类单项分 next 是否比 current 更优：
// bm25/keyword 分越小越好（用 <），其余（向量/元数据等）越大越好。
func betterScore(name string, next float64, current float64) bool {
	if strings.Contains(name, "bm25") || strings.Contains(name, "keyword") {
		return next < current
	}
	return next > current || math.Abs(next-current) < 1e-9
}

// mergeSources 合并两个来源标签列表并去重（保持出现顺序）。
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

// buildFTSQuery 把用户查询转成 FTS5 的 MATCH 表达式：
// 提取词元后各自用双引号包裹（转义内部引号，作为短语项避免 FTS5 语法符号被误解），
// 以 OR 连接（宽松召回，命中任一词即可）。无有效词元时返回空串（跳过关键词召回）。
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

// searchTerms 把查询切成词元，供 FTS5 与元数据 LIKE 共用。规则：
//   - 字母/数字/非 ASCII（含中文，>=128）以及连接符 _ - / . : # @ 视为词内字符（保留标识符/路径完整）；
//   - 其他字符作为分隔符；
//   - 丢弃长度 <2 的词；若切不出任何词但原查询>=2 字符则整体作为一个词兜底；
//   - 最多保留 8 个词，防止超长查询把 SQL 撑爆。
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

// recordRetrievalLog 把一次检索的过程（查询、过滤、各阶段结果的精简版）写入 retrieval_logs，
// 便于后续回溯召回质量与调优，写入失败不影响检索返回。
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

// slimChunks 把 chunk 精简为只含 ID/来源/分数等关键字段的日志视图，
// 避免把大段正文写进 retrieval_logs（节省空间，日志只需能追溯召回链路）。
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
