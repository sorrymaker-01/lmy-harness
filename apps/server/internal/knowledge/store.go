package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/shared"
)

// maxImportBytes 是单个导入文件的大小上限（256 MiB），超限直接拒绝，防止恶意/误传大文件耗尽磁盘。
const maxImportBytes int64 = 256 * 1024 * 1024

// Item 是知识库中一个已导入文档的对外视图（对应 documents 表的一行 + chunk 统计）。
// Path 指向 files 目录下保存的原始文件；当索引成功后原始文件可能被清理，此时 Path 为空。
type Item struct {
	ID              string `json:"id"`
	KnowledgeBaseID string `json:"knowledgeBaseId,omitempty"`
	Name            string `json:"name"`
	Size            int64  `json:"size"`
	ContentType     string `json:"contentType,omitempty"`
	Path            string `json:"path,omitempty"`
	ImportedAt      string `json:"importedAt"`
	Status          string `json:"status,omitempty"`
	ChunkCount      int    `json:"chunkCount,omitempty"`
	ChildChunks     int    `json:"childChunkCount,omitempty"`
	ParentChunks    int    `json:"parentChunkCount,omitempty"`
}

// Store 是 knowledge 模块的核心入口，统一管理文档的导入、抽取、切块、索引与检索。
// 支持两种工作模式：
//  1. SQLite 模式（db != nil）：完整能力，chunk/FTS5/向量索引全部落 SQLite；
//  2. 降级 JSON 模式（db == nil）：仅维护 dir/index.json 的文件清单，无检索能力（兼容旧版本）。
//
// 字段说明：
//   - dir：数据目录，其下 files/ 存原始文件、parsed/ 存抽取后的纯文本、index.json 是旧版清单；
//   - vector/embedder：向量索引与 embedding 提供方，均可为 nil（此时只有关键词/元数据召回）；
//   - mu：串行化导入/删除等文件系统 + 清单的复合操作；
//   - vectorMu：单独保护 vector/embedder 的读写（它们可在运行时被 SetXxx 热替换）。
type Store struct {
	dir      string
	db       *sql.DB
	vector   VectorIndex
	embedder EmbeddingProvider
	mu       sync.Mutex
	vectorMu sync.RWMutex
}

// indexFile 是降级 JSON 模式下 index.json 的文件结构（旧版清单）。
type indexFile struct {
	Items []Item `json:"items"`
}

// Option 是 Store 的函数式配置项。
type Option func(*Store)

// WithDB 注入 SQLite 连接，启用完整的 chunk/FTS/向量索引能力。
func WithDB(db *sql.DB) Option {
	return func(store *Store) {
		store.db = db
	}
}

// WithVectorIndex 注入向量索引后端（如 SQLiteVecIndex），启用向量召回。
func WithVectorIndex(index VectorIndex) Option {
	return func(store *Store) {
		store.vector = index
	}
}

// WithEmbedder 注入 embedding 提供方，配合 WithVectorIndex 才能生成/检索向量。
func WithEmbedder(embedder EmbeddingProvider) Option {
	return func(store *Store) {
		store.embedder = embedder
	}
}

// SetEmbedder 在运行时热替换 embedding 提供方（例如用户在设置页切换了 embedding 模型）。
func (s *Store) SetEmbedder(embedder EmbeddingProvider) {
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	s.embedder = embedder
}

// SetVectorIndex 在运行时热替换向量索引后端。
func (s *Store) SetVectorIndex(index VectorIndex) {
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	s.vector = index
}

// vectorDependencies 以读锁一次性取出向量索引与 embedder 的当前快照，
// 保证异步 goroutine 使用的是同一时刻的一对依赖，避免热替换时读到不一致组合。
func (s *Store) vectorDependencies() (VectorIndex, EmbeddingProvider) {
	if s == nil {
		return nil, nil
	}
	s.vectorMu.RLock()
	defer s.vectorMu.RUnlock()
	return s.vector, s.embedder
}

// vectorIndex 以读锁取出当前向量索引后端（可能为 nil）。
func (s *Store) vectorIndex() VectorIndex {
	if s == nil {
		return nil
	}
	s.vectorMu.RLock()
	defer s.vectorMu.RUnlock()
	return s.vector
}

// NewStore 创建一个仅文件模式（无 SQLite）的 Store，主要供旧调用方/测试使用。
func NewStore(dir string) (*Store, error) {
	return NewStoreWithOptions(dir)
}

// NewStoreWithDB 创建带 SQLite 后端的 Store，可再叠加向量索引/embedder 等 Option。
func NewStoreWithDB(dir string, db *sql.DB, options ...Option) (*Store, error) {
	options = append([]Option{WithDB(db)}, options...)
	return NewStoreWithOptions(dir, options...)
}

// NewStoreWithOptions 是 Store 的统一构造入口。除了创建 files/、parsed/ 目录外，
// 若配置了 SQLite，还会依次执行几项启动自愈动作：
//  1. ensureDefaultKnowledgeBase：保证默认知识库存在且处于 active；
//  2. migrateLegacyIndex：把旧版 index.json 清单中的文档迁移进 SQLite 索引；
//  3. repairLimitedPDFExtractions：若本机新装了 pdftotext，则重抽取此前
//     只留下“无文本层”占位提示的 PDF 文档并重建其索引；
//  4. cleanupIndexedFiles：删除已经成功索引的文档的原始/解析文件，节省磁盘
//     （chunk 内容已完整落库，原文件不再需要）。
func NewStoreWithOptions(dir string, options ...Option) (*Store, error) {
	store := &Store{dir: strings.TrimSpace(dir)}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	if store.dir == "" {
		store.dir = filepath.Join(os.TempDir(), "lmy-knowledge")
	}
	if err := os.MkdirAll(store.filesDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.parsedDir(), 0o755); err != nil {
		return nil, err
	}
	if store.db != nil {
		if err := store.ensureDefaultKnowledgeBase(context.Background()); err != nil {
			return nil, err
		}
		if err := store.migrateLegacyIndex(context.Background()); err != nil {
			return nil, err
		}
		if err := store.repairLimitedPDFExtractions(context.Background()); err != nil {
			return nil, err
		}
		if err := store.cleanupIndexedFiles(context.Background()); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// List 返回全部知识库下的所有文档（等价于 ListByKnowledgeBase("")）。
func (s *Store) List() ([]Item, error) {
	return s.ListByKnowledgeBase(context.Background(), "")
}

// ListByKnowledgeBase 列出指定知识库（空串表示全部）下的文档。
// SQLite 模式走 listSQLite（附带 chunk 统计）；否则回退读 index.json，
// 并顺带用磁盘上的实际文件大小刷新 Size 字段。
func (s *Store) ListByKnowledgeBase(ctx context.Context, knowledgeBaseID string) ([]Item, error) {
	if s.db != nil {
		return s.listSQLite(ctx, knowledgeBaseID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(index.Items))
	for _, item := range index.Items {
		if item.ID == "" {
			continue
		}
		if item.Path != "" {
			if info, err := os.Stat(item.Path); err == nil && !info.IsDir() {
				item.Size = info.Size()
			}
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ImportedAt > items[j].ImportedAt })
	return items, nil
}

// Import 把一个文件导入默认知识库（无 context 的便捷封装）。
func (s *Store) Import(name string, contentType string, reader io.Reader) (Item, error) {
	return s.ImportWithContext(context.Background(), name, contentType, reader)
}

// ImportWithContext 把一个文件导入默认知识库。
func (s *Store) ImportWithContext(ctx context.Context, name string, contentType string, reader io.Reader) (Item, error) {
	return s.ImportToKnowledgeBase(ctx, "", name, contentType, reader)
}

// ImportToKnowledgeBase 是文档导入的主流程：
//  1. 清洗文件名并生成带 "kb" 前缀的文档 ID；
//  2. 以“临时文件 + rename”的方式把内容原子落盘到 files/ 目录，
//     并用 copyBounded 限制最大 256 MiB，防止半写状态与超大文件；
//  3. SQLite 模式下调用 indexImportedItem 完成抽取→切块→建索引，
//     失败时回滚删除已落盘文件，保证不会留下“有文件无索引”的孤儿；
//  4. 无 SQLite 时仅把条目追加进 index.json。
func (s *Store) ImportToKnowledgeBase(ctx context.Context, knowledgeBaseID string, name string, contentType string, reader io.Reader) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = sanitizeDisplayName(name)
	if name == "" {
		return Item{}, errors.New("file name is required")
	}
	if reader == nil {
		return Item{}, errors.New("file content is required")
	}
	if err := os.MkdirAll(s.filesDir(), 0o755); err != nil {
		return Item{}, err
	}
	id := shared.NewID("kb")
	fileName := id + "_" + sanitizeFileName(name)
	if fileName == id+"_" {
		fileName = id + "_document"
	}
	path := filepath.Join(s.filesDir(), fileName)
	tmpPath := path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return Item{}, err
	}
	written, copyErr := copyBounded(file, reader, maxImportBytes)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return Item{}, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return Item{}, closeErr
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return Item{}, err
	}
	item := Item{
		ID:              id,
		KnowledgeBaseID: normalizeKnowledgeBaseID(knowledgeBaseID),
		Name:            name,
		Size:            written,
		ContentType:     strings.TrimSpace(contentType),
		Path:            path,
		ImportedAt:      shared.Now().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Status:          "active",
	}
	if s.db != nil {
		if err := s.indexImportedItem(ctx, item); err != nil {
			_ = os.Remove(path)
			return Item{}, err
		}
		indexed, ok, err := s.itemSQLite(ctx, item.ID)
		if err != nil {
			return Item{}, err
		}
		if ok {
			return indexed, nil
		}
		return item, nil
	}
	index, err := s.loadIndex()
	if err != nil {
		return Item{}, err
	}
	index.Items = append(index.Items, item)
	if err := s.saveIndex(index); err != nil {
		return Item{}, err
	}
	return item, nil
}

// Delete 删除指定文档（无 context 的便捷封装），返回是否确实删除了条目。
func (s *Store) Delete(id string) (bool, error) {
	return s.DeleteWithContext(context.Background(), id)
}

// DeleteWithContext 删除指定文档。SQLite 模式走 deleteSQLite（软删除 + 清理向量）；
// 否则从 index.json 移除条目并删除磁盘上的原始文件。
func (s *Store) DeleteWithContext(ctx context.Context, id string) (bool, error) {
	if s.db != nil {
		return s.deleteSQLite(ctx, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	index, err := s.loadIndex()
	if err != nil {
		return false, err
	}
	next := make([]Item, 0, len(index.Items))
	var removed *Item
	for _, item := range index.Items {
		if item.ID == id {
			itemCopy := item
			removed = &itemCopy
			continue
		}
		next = append(next, item)
	}
	if removed == nil {
		return false, nil
	}
	index.Items = next
	if err := s.saveIndex(index); err != nil {
		return false, err
	}
	if removed.Path != "" {
		if err := os.Remove(removed.Path); err != nil && !os.IsNotExist(err) {
			return true, err
		}
	}
	return true, nil
}

// filesDir 返回原始上传文件的存放目录（<dir>/files）。
func (s *Store) filesDir() string {
	return filepath.Join(s.dir, "files")
}

// parsedDir 返回抽取出的纯文本的存放目录（<dir>/parsed，每个版本一个 <docID>_v<N>.txt）。
func (s *Store) parsedDir() string {
	return filepath.Join(s.dir, "parsed")
}

// indexPath 返回旧版 JSON 清单的路径（<dir>/index.json）。
func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "index.json")
}

// loadIndex 读取旧版 index.json 清单；文件不存在时返回空清单而非报错。
func (s *Store) loadIndex() (indexFile, error) {
	if s.dir == "" {
		return indexFile{Items: []Item{}}, nil
	}
	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return indexFile{Items: []Item{}}, nil
		}
		return indexFile{}, err
	}
	var index indexFile
	if err := json.Unmarshal(data, &index); err != nil {
		return indexFile{}, err
	}
	if index.Items == nil {
		index.Items = []Item{}
	}
	return index, nil
}

// saveIndex 以“写临时文件 + rename”的原子方式持久化 index.json，避免崩溃时清单损坏。
func (s *Store) saveIndex(index indexFile) error {
	if s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := s.indexPath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.indexPath())
}

// copyBounded 在拷贝时施加大小上限：用 LimitReader 读 maxBytes+1 字节，
// 若实际写入超过 maxBytes 说明源超限，直接报错（多读的 1 字节仅用于探测是否超限）。
func copyBounded(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	limited := io.LimitReader(src, maxBytes+1)
	written, err := io.Copy(dst, limited)
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return written, nil
}

// sanitizeDisplayName 清洗用户提供的文件名：只保留 basename（防目录穿越），
// 并去掉首尾的点和空格（防隐藏文件名/尾点等异常）。
func sanitizeDisplayName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Trim(value, ". ")
	return value
}

// sanitizeFileName 进一步把文件名收敛为磁盘安全形式：只允许字母/数字/./-/_，
// 其余字符折叠成单个下划线，避免特殊字符在不同文件系统上出问题。
func sanitizeFileName(value string) string {
	value = sanitizeDisplayName(value)
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_' {
			builder.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			builder.WriteRune('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(builder.String(), "._-")
}
