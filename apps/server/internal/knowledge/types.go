package knowledge

import "context"

const (
	defaultKnowledgeBaseID   = "default"
	defaultKnowledgeBaseName = "默认知识库"
)

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

type EmbeddingProvider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type VectorIndex interface {
	Upsert(ctx context.Context, points []VectorPoint) error
	DeleteDocument(ctx context.Context, docID string, docVersion int) error
	Search(ctx context.Context, vector []float32, filter RetrievalFilter, limit int) ([]VectorHit, error)
}

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

type VectorHit struct {
	ChunkID string
	Score   float64
	Payload map[string]any
}

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

type RetrievalFilter struct {
	KnowledgeBaseIDs []string       `json:"knowledgeBaseIds,omitempty"`
	DocIDs           []string       `json:"docIds,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type RetrievalOptions struct {
	ConversationID string          `json:"conversationId,omitempty"`
	Filter         RetrievalFilter `json:"filter,omitempty"`
	KeywordLimit   int             `json:"keywordLimit,omitempty"`
	VectorLimit    int             `json:"vectorLimit,omitempty"`
	MetadataLimit  int             `json:"metadataLimit,omitempty"`
	TopK           int             `json:"topK,omitempty"`
}

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

type RetrievalResult struct {
	Query           string           `json:"query"`
	Filter          RetrievalFilter  `json:"filter"`
	KeywordResults  []RetrievedChunk `json:"keywordResults"`
	VectorResults   []RetrievedChunk `json:"vectorResults"`
	MetadataResults []RetrievedChunk `json:"metadataResults"`
	MergedResults   []RetrievedChunk `json:"mergedResults"`
	RerankedResults []RetrievedChunk `json:"rerankedResults"`
}

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
