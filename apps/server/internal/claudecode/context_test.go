package claudecode

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupContextDiscoversProjectFiles(t *testing.T) {
	projectRoot := t.TempDir()
	configDir := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("PROJECT_ROOT", projectRoot)
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	if err := os.MkdirAll(filepath.Join(projectRoot, ".claude", "rules"), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "skills", "demo"), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "skills", "project-demo"), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	writeFile(t, filepath.Join(projectRoot, "go.mod"), "module example.com/local\n")
	writeFile(t, filepath.Join(projectRoot, "CLAUDE.md"), "project instructions")
	writeFile(t, filepath.Join(projectRoot, ".claude", "rules", "base.md"), "always be concise")
	writeFile(t, filepath.Join(configDir, "MEMORY.md"), "user memory")
	writeFile(t, filepath.Join(projectRoot, ".mcp.json"), `{"mcpServers":{"fs":{"type":"stdio","command":"node","args":["server.js"]}}}`)
	writeFile(t, filepath.Join(projectRoot, "skills", "project-demo", "SKILL.md"), "# project demo")
	writeFile(t, filepath.Join(configDir, "skills", "demo", "SKILL.md"), "# demo")

	ctx := LoadStartupContext()
	if ctx.ProjectRoot != projectRoot {
		t.Fatalf("unexpected project root: %q", ctx.ProjectRoot)
	}
	if len(ctx.Instructions) != 1 || ctx.Instructions[0].Content != "project instructions" {
		t.Fatalf("unexpected instructions: %#v", ctx.Instructions)
	}
	if len(ctx.Rules) != 1 || ctx.Rules[0].Content != "always be concise" {
		t.Fatalf("unexpected rules: %#v", ctx.Rules)
	}
	if ctx.AutoMemory == nil || ctx.AutoMemory.Content != "user memory" {
		t.Fatalf("unexpected auto memory: %#v", ctx.AutoMemory)
	}
	if len(ctx.MCP.Servers) != 1 || ctx.MCP.Servers[0].Name != "fs" {
		t.Fatalf("unexpected mcp servers: %#v", ctx.MCP.Servers)
	}
	if len(ctx.SkillDirectories) != 2 || ctx.SkillDirectories[0].Scope != "project" || ctx.SkillDirectories[1].Scope != "personal" {
		t.Fatalf("unexpected skill directories: %#v", ctx.SkillDirectories)
	}
	expectedStateDBPath := filepath.Join(projectRoot, "apps", "server", "data", "state.db")
	if ctx.StateDBPath() != expectedStateDBPath {
		t.Fatalf("unexpected state db path: %q", ctx.StateDBPath())
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}
