package knowledge

import "context"

// 默认知识库的固定 ID 与名称。系统保证任何时刻至少存在这个默认知识库：
// 导入文档时若未指定知识库 ID，会自动落到 default；默认知识库不允许删除。
const (
	defaultKnowledgeBaseID   = "default"
	defaultKnowledgeBaseName = "默认知识库"
)

// KnowledgeBase 表示一个知识库（文档的逻辑分组），是检索过滤的最外层边界。
// 各类 Count 字段是从 documents / document_chunks 表按 active 状态聚合出来的统计值，
// 仅用于展示，不参与检索逻辑。
type KnowledgeBase struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Status        string `json:"status"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
	DocumentCount int    `json:"documentCount"`
	ChunkCount    int    `json:"chunkCount"`
	ChildChunks   int    `json:"childChunkCount"`
	ParentChunks  int    `json:"parentChunkCount"`
}

// EmbeddingProvider 是 embedding 生成器的抽象：输入一批文本，返回等长的向量列表。
// 具体实现由上层注入（通常是调用外部 embedding 模型 API），knowledge 模块自身不关心
// 向量维度——维度由第一次写入时的向量长度决定（见 SQLiteVecIndex.ensureVectorTable）。
type EmbeddingProvider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// VectorIndex 是向量索引后端的抽象，解耦具体向量库实现（当前唯一实现是 SQLiteVecIndex）。
//   - Upsert：批量写入/覆盖向量点；
//   - DeleteDocument：按“文档 ID + 版本”整体删除该文档的所有向量（文档删除/重建索引时调用）；
//   - Search：按查询向量做 KNN 检索，并叠加知识库/文档维度的元数据过滤。
type VectorIndex interface {
	Upsert(ctx context.Context, points []VectorPoint) error
	DeleteDocument(ctx context.Context, docID string, docVersion int) error
	Search(ctx context.Context, vector []float32, filter RetrievalFilter, limit int) ([]VectorHit, error)
}

// VectorPoint 是写入向量索引的一个“点”：向量本体 + 一份冗余的 chunk 元数据。
// 元数据（知识库 ID、doc_id、标题、heading_path 等）随向量一起存储，
// 使向量检索阶段无需回表 document_chunks 即可完成过滤和构造命中 payload。
type VectorPoint struct {
	ID              string
	Vector          []float32
	ChunkID         string
	DocID           string
	DocVersion      int
	KnowledgeBaseID string
	Title           string
	ContentHash     string
	SourceType      string
	HeadingPath     string
	Status          string
	Metadata        map[string]any
}

// VectorHit 是向量检索的单条命中结果。
// Score 是归一化后的相似度分数（越大越相似，见 sqliteVecDistanceScore），
// Payload 携带该向量点存储的全部元数据（chunk_id、doc_id、distance 等）。
type VectorHit struct {
	ChunkID string
	Score   float64
	Payload map[string]any
}

// vectorBackfillChunk 是“向量补建”流程使用的中间结构：
// 表示一个已经写入 document_chunks、但在 document_chunk_vector_rows 中
// 还没有对应向量记录的 child chunk（例如导入时 embedder 未配置、或异步向量化失败）。
// BackfillMissingVectorsAsync 会分批捞出这些 chunk 重新做 embedding 并写入向量索引。
type vectorBackfillChunk struct {
	ID              string
	DocID           string
	DocVersion      int
	KnowledgeBaseID string
	Title           string
	Content         string
	ContentHash     string
	SourceType      string
	HeadingPath     string
	Metadata        map[string]any
}

// RetrievalFilter 是检索时的范围过滤条件，三路召回（关键词/向量/元数据）共用：
//   - KnowledgeBaseIDs：限定在这些知识库内检索（空表示不限）；
//   - DocIDs：限定在这些文档内检索（空表示不限）；
//   - Metadata：预留的自定义元数据过滤字段（当前 SQL 层尚未使用）。
type RetrievalFilter struct {
	KnowledgeBaseIDs []string       `json:"knowledgeBaseIds,omitempty"`
	DocIDs           []string       `json:"docIds,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

// RetrievalOptions 是一次检索请求的参数。各 Limit 为对应召回通道的候选上限
// （默认：关键词 40、向量 40、元数据 20），TopK 是重排后最终返回的条数（默认 8）。
// ConversationID 仅用于把检索过程写入 retrieval_logs 便于回溯分析。
type RetrievalOptions struct {
	ConversationID string          `json:"conversationId,omitempty"`
	Filter         RetrievalFilter `json:"filter,omitempty"`
	KeywordLimit   int             `json:"keywordLimit,omitempty"`
	VectorLimit    int             `json:"vectorLimit,omitempty"`
	MetadataLimit  int             `json:"metadataLimit,omitempty"`
	TopK           int             `json:"topK,omitempty"`
}

// RetrievedChunk 是检索返回的单个 chunk。
//   - Scores：各通道分数汇总，键包括 keyword_score（bm25，越小越好）、vector_score、
//     metadata_score、rrf（多路融合分）、rerank_score（最终名次分）；
//   - Sources：该 chunk 被哪些召回通道命中（keyword / vector / metadata）；
//   - 经过父 chunk 展开后，Content 可能已替换为父 chunk 的更长上下文，
//     但 ID 仍保留命中的子 chunk ID（见 expandParentChunks）。
type RetrievedChunk struct {
	ID              string             `json:"id"`
	DocID           string             `json:"docId"`
	DocVersion      int                `json:"docVersion"`
	KnowledgeBaseID string             `json:"knowledgeBaseId"`
	ParentChunkID   string             `json:"parentChunkId,omitempty"`
	ChunkIndex      int                `json:"chunkIndex"`
	Title           string             `json:"title"`
	Content         string             `json:"content"`
	ContentHash     string             `json:"contentHash"`
	TokenCount      int                `json:"tokenCount"`
	HeadingPath     string             `json:"headingPath,omitempty"`
	SourceURI       string             `json:"sourceUri,omitempty"`
	SourceType      string             `json:"sourceType,omitempty"`
	Scores          map[string]float64 `json:"scores"`
	Sources         []string           `json:"sources"`
	Metadata        map[string]any     `json:"metadata,omitempty"`
}

// RetrievalResult 是一次混合检索的完整结果，保留了每个阶段的中间产物
// （三路召回原始结果、RRF 融合结果、重排结果），方便调试与前端展示召回链路。
// 最终喂给 LLM 的通常是 RerankedResults。
type RetrievalResult struct {
	Query           string           `json:"query"`
	Filter          RetrievalFilter  `json:"filter"`
	KeywordResults  []RetrievedChunk `json:"keywordResults"`
	VectorResults   []RetrievedChunk `json:"vectorResults"`
	MetadataResults []RetrievedChunk `json:"metadataResults"`
	MergedResults   []RetrievedChunk `json:"mergedResults"`
	RerankedResults []RetrievedChunk `json:"rerankedResults"`
}

// chunkDraft 是切块阶段产出的“待入库 chunk”草稿，尚未持久化。
//   - Type："parent" 或 "child"（父子分层切块，child 用于精确召回，parent 用于上下文扩展）；
//   - ParentID：child 所属的 parent chunk ID（parent 自身为空）；
//   - PrevID/NextID：同类型 chunk 的前后邻接链，便于将来做相邻上下文扩展；
//   - StartOffset/EndOffset：chunk 在原始解析文本中的 rune 偏移区间；
//   - TokenCount：approxTokenCount 估算的 token 数；ContentHash：归一化内容的 SHA-256，用于去重。
type chunkDraft struct {
	ID          string
	ParentID    string
	Type        string
	Index       int
	Title       string
	Content     string
	HeadingPath string
	StartOffset int
	EndOffset   int
	TokenCount  int
	ContentHash string
	PrevID      string
	NextID      string
	Metadata    map[string]any
}
