package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreImportAndList(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	item, err := store.Import("../notes.md", "text/markdown", strings.NewReader("# Notes\nhello"))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if item.ID == "" {
		t.Fatal("Import() returned empty item ID")
	}
	if item.Name != "notes.md" {
		t.Fatalf("item.Name = %q, want %q", item.Name, "notes.md")
	}
	if item.ContentType != "text/markdown" {
		t.Fatalf("item.ContentType = %q, want text/markdown", item.ContentType)
	}
	if item.Size != int64(len("# Notes\nhello")) {
		t.Fatalf("item.Size = %d, want %d", item.Size, len("# Notes\nhello"))
	}
	if filepath.Base(item.Path) == "notes.md" {
		t.Fatalf("stored file name should include generated ID, got %q", item.Path)
	}
	data, err := os.ReadFile(item.Path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", item.Path, err)
	}
	if string(data) != "# Notes\nhello" {
		t.Fatalf("stored content = %q", string(data))
	}

	items, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List() returned %d items, want 1", len(items))
	}
	if items[0].ID != item.ID || items[0].Name != "notes.md" {
		t.Fatalf("List()[0] = %+v, want imported item", items[0])
	}

	deleted, err := store.Delete(item.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !deleted {
		t.Fatal("Delete() returned false, want true")
	}
	if _, err := os.Stat(item.Path); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat error = %v, want not exist", err)
	}
	items, err = store.List()
	if err != nil {
		t.Fatalf("List() after delete error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List() after delete returned %d items, want 0", len(items))
	}
}
