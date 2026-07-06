package state

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"code.byted.org/ai/lmy/apps/server/internal/claudecode"
	statedb "code.byted.org/ai/lmy/apps/server/internal/infra/db"
)

func TestToolConfigPersistence(t *testing.T) {
	store, database := newTestStore(t)
	defer database.Close()

	if err := store.SaveToolConfig(ToolConfigRecord{
		ToolName:       "AskUserQuestion",
		Enabled:        false,
		ApprovalPolicy: "ask",
	}); err != nil {
		t.Fatalf("save tool config: %v", err)
	}

	config, ok := store.ToolConfigFor("AskUserQuestion")
	if !ok {
		t.Fatal("expected tool config")
	}
	if config.Enabled {
		t.Fatal("expected tool to be disabled")
	}
	if config.ApprovalPolicy != "ask" {
		t.Fatalf("expected ask approval policy, got %q", config.ApprovalPolicy)
	}

	configs, err := store.ListToolConfigs()
	if err != nil {
		t.Fatalf("list tool configs: %v", err)
	}
	record := configs["AskUserQuestion"]
	if record.Config == nil {
		t.Fatal("expected config map to default to an empty object")
	}

	var rawConfig string
	if err := database.QueryRow(`SELECT config_json FROM tool_configs WHERE tool_name = ?`, "AskUserQuestion").Scan(&rawConfig); err != nil {
		t.Fatalf("read raw config: %v", err)
	}
	if rawConfig != "{}" {
		t.Fatalf("expected empty object config JSON, got %q", rawConfig)
	}

	if err := store.SaveToolConfig(ToolConfigRecord{
		ToolName:       "AskUserQuestion",
		Enabled:        true,
		ApprovalPolicy: "invalid-policy",
		Config:         map[string]any{"limit": float64(3)},
	}); err != nil {
		t.Fatalf("save updated tool config: %v", err)
	}
	config, ok = store.ToolConfigFor("AskUserQuestion")
	if !ok {
		t.Fatal("expected updated tool config")
	}
	if !config.Enabled {
		t.Fatal("expected tool to be enabled")
	}
	if config.ApprovalPolicy != "auto" {
		t.Fatalf("expected invalid policy to normalize to auto, got %q", config.ApprovalPolicy)
	}
}

func TestMCPServerConfigPersistence(t *testing.T) {
	store, database := newTestStore(t)
	defer database.Close()

	initial := []claudecode.MCPServer{{
		Name:    "filesystem",
		Scope:   "project",
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "."},
		Env:     map[string]string{"A": "B"},
	}}
	if err := store.SyncMCPServers(initial); err != nil {
		t.Fatalf("sync mcp servers: %v", err)
	}

	enabled, err := store.EnabledMCPServers()
	if err != nil {
		t.Fatalf("enabled mcp servers: %v", err)
	}
	if len(enabled) != 1 || enabled[0].Name != "filesystem" {
		t.Fatalf("expected filesystem server enabled, got %#v", enabled)
	}

	if err := store.SetMCPServerEnabled("filesystem", false); err != nil {
		t.Fatalf("disable mcp server: %v", err)
	}

	updatedFromFile := []claudecode.MCPServer{{
		Name:    "filesystem",
		Scope:   "project",
		Type:    "stdio",
		Command: "node",
		Args:    []string{"server.js"},
	}}
	if err := store.SyncMCPServers(updatedFromFile); err != nil {
		t.Fatalf("resync mcp servers: %v", err)
	}

	configs, err := store.ListMCPServerConfigs()
	if err != nil {
		t.Fatalf("list mcp server configs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected one mcp config, got %d", len(configs))
	}
	if configs[0].Command != "node" {
		t.Fatalf("expected command to refresh from file config, got %q", configs[0].Command)
	}
	if configs[0].Enabled {
		t.Fatal("expected disabled state to be preserved after file resync")
	}

	enabled, err = store.EnabledMCPServers()
	if err != nil {
		t.Fatalf("enabled mcp servers after disable: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("expected no enabled servers, got %#v", enabled)
	}

	if err := store.SetMCPServerEnabled("missing", true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for missing mcp server, got %v", err)
	}
}

func TestModelConfigPersistence(t *testing.T) {
	store, database := newTestStore(t)
	defer database.Close()

	seed := ModelConfigRecord{
		ID:             "default",
		Provider:       "openai-compatible",
		APIKey:         "sk-seed",
		BaseURL:        "https://example.com/api/v3/",
		Model:          "seed-model",
		Temperature:    0.4,
		TimeoutSeconds: 30,
	}
	if err := store.SeedModelConfig(seed); err != nil {
		t.Fatalf("seed model config: %v", err)
	}
	if err := store.SeedModelConfig(ModelConfigRecord{
		ID:             "default",
		APIKey:         "sk-ignored",
		BaseURL:        "https://ignored.example.com",
		Model:          "ignored-model",
		Temperature:    0.9,
		TimeoutSeconds: 99,
	}); err != nil {
		t.Fatalf("seed existing model config: %v", err)
	}

	record, ok, err := store.ModelConfig("default")
	if err != nil {
		t.Fatalf("read model config: %v", err)
	}
	if !ok {
		t.Fatal("expected model config")
	}
	if record.APIKey != "sk-seed" {
		t.Fatalf("seed should not overwrite existing config, got api key %q", record.APIKey)
	}
	if record.BaseURL != "https://example.com/api/v3" {
		t.Fatalf("expected normalized base url, got %q", record.BaseURL)
	}

	if err := store.SaveModelConfig(ModelConfigRecord{
		ID:             "default",
		Provider:       "",
		APIKey:         "sk-updated",
		BaseURL:        "",
		Model:          "",
		Temperature:    3,
		TimeoutSeconds: -1,
		Extra:          map[string]any{"routing": "primary"},
	}); err != nil {
		t.Fatalf("save model config: %v", err)
	}
	record, ok, err = store.ModelConfig("default")
	if err != nil {
		t.Fatalf("read updated model config: %v", err)
	}
	if !ok {
		t.Fatal("expected updated model config")
	}
	if record.Provider != "openai-compatible" {
		t.Fatalf("expected default provider, got %q", record.Provider)
	}
	if record.APIKey != "sk-updated" {
		t.Fatalf("expected updated api key, got %q", record.APIKey)
	}
	if record.BaseURL != "https://ark-cn-beijing.bytedance.net/api/v3" {
		t.Fatalf("expected default base url, got %q", record.BaseURL)
	}
	if record.Model != "ep-20260507115713-ltdzl" {
		t.Fatalf("expected default model, got %q", record.Model)
	}
	if record.Temperature != 0.2 {
		t.Fatalf("expected normalized temperature, got %v", record.Temperature)
	}
	if record.TimeoutSeconds != 60 {
		t.Fatalf("expected default timeout, got %d", record.TimeoutSeconds)
	}
	if record.Extra["routing"] != "primary" {
		t.Fatalf("expected extra json to persist, got %#v", record.Extra)
	}

	if err := store.SaveModelConfig(ModelConfigRecord{
		ID:             "fast",
		Provider:       "openai-compatible",
		APIKey:         "sk-fast",
		BaseURL:        "https://fast.example.com/api",
		Model:          "fast-model",
		Temperature:    0.1,
		TimeoutSeconds: 15,
	}); err != nil {
		t.Fatalf("save custom model config: %v", err)
	}
	configs, err := store.ListModelConfigs()
	if err != nil {
		t.Fatalf("list model configs: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("expected two model configs, got %d", len(configs))
	}
	if configs[0].ID != "default" || configs[1].ID != "fast" {
		t.Fatalf("expected default config first, got %#v", configs)
	}
	if err := store.DeleteModelConfig("fast"); err != nil {
		t.Fatalf("delete custom model config: %v", err)
	}
	if _, ok, err := store.ModelConfig("fast"); err != nil || ok {
		t.Fatalf("expected fast model config to be deleted, ok=%v err=%v", ok, err)
	}
	if err := store.DeleteModelConfig("default"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected default model delete to be rejected with sql.ErrNoRows, got %v", err)
	}
}

func newTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	database, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	return NewStore(database), database
}
