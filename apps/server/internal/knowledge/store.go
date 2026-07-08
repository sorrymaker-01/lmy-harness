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

const maxImportBytes int64 = 256 * 1024 * 1024

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

type Store struct {
	dir      string
	db       *sql.DB
	vector   VectorIndex
	embedder EmbeddingProvider
	mu       sync.Mutex
	vectorMu sync.RWMutex
}

type indexFile struct {
	Items []Item `json:"items"`
}

type Option func(*Store)

func WithDB(db *sql.DB) Option {
	return func(store *Store) {
		store.db = db
	}
}

func WithVectorIndex(index VectorIndex) Option {
	return func(store *Store) {
		store.vector = index
	}
}

func WithEmbedder(embedder EmbeddingProvider) Option {
	return func(store *Store) {
		store.embedder = embedder
	}
}

func (s *Store) SetEmbedder(embedder EmbeddingProvider) {
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	s.embedder = embedder
}

func (s *Store) SetVectorIndex(index VectorIndex) {
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	s.vector = index
}

func (s *Store) vectorDependencies() (VectorIndex, EmbeddingProvider) {
	if s == nil {
		return nil, nil
	}
	s.vectorMu.RLock()
	defer s.vectorMu.RUnlock()
	return s.vector, s.embedder
}

func (s *Store) vectorIndex() VectorIndex {
	if s == nil {
		return nil
	}
	s.vectorMu.RLock()
	defer s.vectorMu.RUnlock()
	return s.vector
}

func NewStore(dir string) (*Store, error) {
	return NewStoreWithOptions(dir)
}

func NewStoreWithDB(dir string, db *sql.DB, options ...Option) (*Store, error) {
	options = append([]Option{WithDB(db)}, options...)
	return NewStoreWithOptions(dir, options...)
}

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

func (s *Store) List() ([]Item, error) {
	return s.ListByKnowledgeBase(context.Background(), "")
}

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

func (s *Store) Import(name string, contentType string, reader io.Reader) (Item, error) {
	return s.ImportWithContext(context.Background(), name, contentType, reader)
}

func (s *Store) ImportWithContext(ctx context.Context, name string, contentType string, reader io.Reader) (Item, error) {
	return s.ImportToKnowledgeBase(ctx, "", name, contentType, reader)
}

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

func (s *Store) Delete(id string) (bool, error) {
	return s.DeleteWithContext(context.Background(), id)
}

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

func (s *Store) filesDir() string {
	return filepath.Join(s.dir, "files")
}

func (s *Store) parsedDir() string {
	return filepath.Join(s.dir, "parsed")
}

func (s *Store) indexPath() string {
	return filepath.Join(s.dir, "index.json")
}

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

func sanitizeDisplayName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Trim(value, ". ")
	return value
}

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
