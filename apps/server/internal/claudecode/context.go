package claudecode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type StartupContext struct {
	ProjectRoot      string           `json:"projectRoot"`
	ConfigDir        string           `json:"configDir"`
	Instructions     []ContextFile    `json:"instructions"`
	Rules            []ContextFile    `json:"rules"`
	AutoMemory       *ContextFile     `json:"autoMemory,omitempty"`
	Settings         map[string]any   `json:"settings"`
	MCP              MCPConfig        `json:"mcp"`
	SkillDirectories []SkillDirectory `json:"skillDirectories"`
}

type ContextFile struct {
	Path    string   `json:"path"`
	Scope   string   `json:"scope"`
	Content string   `json:"content"`
	Paths   []string `json:"paths,omitempty"`
}

type SkillDirectory struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

type MCPConfig struct {
	Servers []MCPServer `json:"servers"`
}

type MCPServer struct {
	Name    string            `json:"name"`
	Scope   string            `json:"scope"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type rawMCPFile struct {
	MCPServers map[string]rawMCPServer `json:"mcpServers"`
}

type rawMCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	URL     string            `json:"url"`
	Env     map[string]string `json:"env"`
	Headers map[string]string `json:"headers"`
}

func LoadStartupContext() StartupContext {
	projectRoot := detectProjectRoot()
	configDir := detectConfigDir()
	return StartupContext{
		ProjectRoot:      projectRoot,
		ConfigDir:        configDir,
		Instructions:     loadInstructionFiles(projectRoot, configDir),
		Rules:            loadRuleFiles(projectRoot, configDir),
		AutoMemory:       loadAutoMemory(configDir),
		Settings:         loadSettings(projectRoot, configDir),
		MCP:              loadMCP(projectRoot, configDir),
		SkillDirectories: skillDirectories(projectRoot, configDir),
	}
}

func (c StartupContext) TranscriptDir() string {
	root := c.ConfigDir
	if strings.TrimSpace(root) == "" {
		root = detectConfigDir()
	}
	project := sanitizeProjectPath(c.ProjectRoot)
	if project == "" {
		project = "unknown-project"
	}
	return filepath.Join(root, "projects", project)
}

func (c StartupContext) KnowledgeDir() string {
	return filepath.Join(c.TranscriptDir(), "knowledge")
}

func (c StartupContext) StateDBPath() string {
	root := strings.TrimSpace(c.ProjectRoot)
	if root == "" {
		root = detectProjectRoot()
	}
	return filepath.Join(root, "apps", "server", "data", "state.db")
}

func detectProjectRoot() string {
	if explicit := strings.TrimSpace(os.Getenv("PROJECT_ROOT")); explicit != "" {
		if abs, err := filepath.Abs(explicit); err == nil {
			return abs
		}
		return explicit
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	dir := cwd
	for {
		if exists(filepath.Join(dir, "go.mod")) || exists(filepath.Join(dir, "package.json")) || exists(filepath.Join(dir, ".git")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

func detectConfigDir() string {
	if explicit := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); explicit != "" {
		if abs, err := filepath.Abs(explicit); err == nil {
			return abs
		}
		return explicit
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

func loadInstructionFiles(projectRoot string, configDir string) []ContextFile {
	candidates := []ContextFile{
		{Path: filepath.Join(configDir, "CLAUDE.md"), Scope: "user"},
		{Path: filepath.Join(projectRoot, "CLAUDE.md"), Scope: "project"},
		{Path: filepath.Join(projectRoot, ".claude", "CLAUDE.md"), Scope: "project"},
		{Path: filepath.Join(projectRoot, "CLAUDE.local.md"), Scope: "local"},
	}
	out := make([]ContextFile, 0, len(candidates))
	for _, candidate := range candidates {
		content, ok := readText(candidate.Path, 256*1024)
		if !ok {
			continue
		}
		candidate.Content = content
		out = append(out, candidate)
	}
	return out
}

func loadRuleFiles(projectRoot string, configDir string) []ContextFile {
	var rules []ContextFile
	rules = append(rules, loadRulesFromDir(filepath.Join(configDir, "rules"), "user")...)
	rules = append(rules, loadRulesFromDir(filepath.Join(projectRoot, ".claude", "rules"), "project")...)
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Scope == rules[j].Scope {
			return rules[i].Path < rules[j].Path
		}
		return scopeOrder(rules[i].Scope) < scopeOrder(rules[j].Scope)
	})
	return rules
}

func loadRulesFromDir(root string, scope string) []ContextFile {
	if !exists(root) {
		return nil
	}
	var rules []ContextFile
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}
		content, ok := readText(path, 256*1024)
		if !ok {
			return nil
		}
		meta, body := splitFrontmatter(content)
		rules = append(rules, ContextFile{
			Path:    path,
			Scope:   scope,
			Content: body,
			Paths:   parseStringList(meta["paths"]),
		})
		return nil
	})
	return rules
}

func loadAutoMemory(configDir string) *ContextFile {
	for _, name := range []string{"MEMORY.md", "auto_memory.md"} {
		path := filepath.Join(configDir, name)
		content, ok := readText(path, 128*1024)
		if !ok {
			continue
		}
		return &ContextFile{Path: path, Scope: "user", Content: content}
	}
	return nil
}

func loadSettings(projectRoot string, configDir string) map[string]any {
	settings := map[string]any{}
	for _, path := range []string{
		filepath.Join(configDir, "settings.json"),
		filepath.Join(projectRoot, ".claude", "settings.json"),
		filepath.Join(projectRoot, ".claude", "settings.local.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		mergeMaps(settings, raw)
	}
	return settings
}

func loadMCP(projectRoot string, configDir string) MCPConfig {
	servers := []MCPServer{}
	servers = append(servers, loadMCPFile(filepath.Join(projectRoot, ".mcp.json"), "project")...)
	servers = append(servers, loadMCPFile(filepath.Join(configDir, ".mcp.json"), "user")...)
	servers = append(servers, loadMCPFile(filepath.Join(filepath.Dir(configDir), ".claude.json"), "user")...)
	sort.SliceStable(servers, func(i, j int) bool {
		if servers[i].Scope == servers[j].Scope {
			return servers[i].Name < servers[j].Name
		}
		return scopeOrder(servers[i].Scope) < scopeOrder(servers[j].Scope)
	})
	return MCPConfig{Servers: servers}
}

func loadMCPFile(path string, scope string) []MCPServer {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw rawMCPFile
	if err := json.Unmarshal(data, &raw); err != nil || len(raw.MCPServers) == 0 {
		return nil
	}
	servers := make([]MCPServer, 0, len(raw.MCPServers))
	for name, server := range raw.MCPServers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		servers = append(servers, MCPServer{
			Name:    name,
			Scope:   scope,
			Type:    strings.TrimSpace(server.Type),
			Command: expandEnv(server.Command),
			Args:    expandEnvList(server.Args),
			URL:     expandEnv(server.URL),
			Env:     expandEnvMap(server.Env),
			Headers: expandEnvMap(server.Headers),
		})
	}
	return servers
}

func skillDirectories(projectRoot string, configDir string) []SkillDirectory {
	dirs := []SkillDirectory{
		{Path: filepath.Join(projectRoot, "skills"), Scope: "project"},
		{Path: filepath.Join(projectRoot, ".claude", "skills"), Scope: "project"},
		{Path: filepath.Join(configDir, "skills"), Scope: "personal"},
	}
	result := make([]SkillDirectory, 0, len(dirs))
	for _, dir := range dirs {
		if exists(dir.Path) {
			result = append(result, dir)
		}
	}
	return result
}

func splitFrontmatter(content string) (map[string]string, string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return map[string]string{}, strings.TrimSpace(content)
	}
	rest := strings.TrimPrefix(content, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return map[string]string{}, strings.TrimSpace(content)
	}
	raw := rest[:end]
	body := strings.TrimPrefix(rest[end:], "\n---")
	body = strings.TrimPrefix(body, "\n")
	meta := map[string]string{}
	var currentKey string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && currentKey != "" {
			existing := meta[currentKey]
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if existing == "" {
				meta[currentKey] = item
			} else {
				meta[currentKey] = existing + "\n" + item
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		currentKey = strings.ToLower(strings.TrimSpace(key))
		meta[currentKey] = trimYAMLScalar(value)
	}
	return meta, strings.TrimSpace(body)
}

func parseStringList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
	out := []string{}
	for _, part := range parts {
		part = trimYAMLScalar(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func trimYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return strings.TrimSpace(value)
}

func readText(path string, limit int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	if limit > 0 && info.Size() > limit {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

func mergeMaps(dst map[string]any, src map[string]any) {
	for key, value := range src {
		if srcMap, ok := value.(map[string]any); ok {
			if dstMap, ok := dst[key].(map[string]any); ok {
				mergeMaps(dstMap, srcMap)
				continue
			}
		}
		dst[key] = value
	}
}

func expandEnv(value string) string {
	return os.ExpandEnv(strings.TrimSpace(value))
}

func expandEnvList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, expandEnv(value))
	}
	return out
}

func expandEnvMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = expandEnv(value)
	}
	return out
}

func sanitizeProjectPath(projectRoot string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	projectRoot = strings.Trim(projectRoot, string(os.PathSeparator))
	projectRoot = strings.ReplaceAll(projectRoot, string(os.PathSeparator), "-")
	projectRoot = strings.ReplaceAll(projectRoot, ":", "-")
	return strings.Trim(projectRoot, "-")
}

func scopeOrder(scope string) int {
	switch scope {
	case "user", "personal":
		return 0
	case "project":
		return 1
	case "local":
		return 2
	default:
		return 3
	}
}
