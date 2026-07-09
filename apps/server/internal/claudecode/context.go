// Package claudecode 负责在服务启动时发现并加载 Claude Code 风格的
// 本地配置上下文（startup context），包括：
//   - CLAUDE.md 指令文件（用户级 / 项目级 / 项目 .claude/ / 本地覆盖）；
//   - 规则文件（~/.claude/rules 与 <project>/.claude/rules 下的 *.md，
//     支持 frontmatter 的 paths 字段做路径限定）；
//   - settings（settings.json 的三层深度合并：用户 → 项目 → 项目本地）；
//   - MCP 服务配置（项目 .mcp.json、用户配置目录 .mcp.json、~/.claude.json）；
//   - skill 目录候选（项目 skills/、项目 .claude/skills/、用户 skills/）；
//   - 自动记忆文件（MEMORY.md / auto_memory.md）。
//
// 发现结果封装为 StartupContext，由 agent 层渲染进 system prompt
// （见 agent.renderStartupPrompt），并为 skills.Registry 提供扫描目录、
// 为 MCP 运行时提供服务清单。本包只做"发现与读取"，不做任何解释执行。
package claudecode

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// StartupContext 是一次启动发现的全部上下文快照。
// 它在进程启动时构建一次，随后贯穿 agent 生命周期：
// Instructions/Rules/AutoMemory/Settings 被渲染进 system prompt，
// MCP 供工具运行时连接外部服务，SkillDirectories 供 skills.Registry 扫描。
type StartupContext struct {
	ProjectRoot string `json:"projectRoot"` // 项目根目录（见 detectProjectRoot）
	ConfigDir   string `json:"configDir"`   // 用户配置目录（默认 ~/.claude）
	// Instructions 是按"用户 → 项目 → 项目.claude → 本地"顺序收集到的
	// CLAUDE.md 内容，顺序即注入 system prompt 的顺序（越靠后越贴近本地，
	// 由模型自然形成"后者细化/覆盖前者"的阅读语义）。
	Instructions []ContextFile `json:"instructions"`
	// Rules 是 rules 目录下的 Markdown 规则，已按 scope（user 先于 project）
	// 与路径排序；带 Paths 的规则仅对指定路径生效。
	Rules []ContextFile `json:"rules"`
	// AutoMemory 是自动记忆文件（MEMORY.md 或 auto_memory.md），可能不存在。
	AutoMemory *ContextFile `json:"autoMemory,omitempty"`
	// Settings 是三层 settings.json 深度合并后的最终配置。
	Settings map[string]any `json:"settings"`
	// MCP 是从各来源合并到的 MCP 服务清单。
	MCP MCPConfig `json:"mcp"`
	// SkillDirectories 是实际存在的 skill 根目录候选列表。
	SkillDirectories []SkillDirectory `json:"skillDirectories"`
}

// ContextFile 表示一个被读入内存的上下文文件（CLAUDE.md、规则或记忆）。
type ContextFile struct {
	Path    string `json:"path"`    // 文件绝对路径，便于溯源与提示展示
	Scope   string `json:"scope"`   // 来源作用域：user / project / local
	Content string `json:"content"` // 去除首尾空白后的文件内容
	// Paths 仅规则文件使用：来自 frontmatter 的 paths 字段，
	// 非空表示该规则只适用于这些路径（glob），渲染时会单独归类展示。
	Paths []string `json:"paths,omitempty"`
}

// SkillDirectory 是一个存在于磁盘上的 skill 根目录及其作用域，
// 与 skills.Directory 一一对应（由调用方转换）。
type SkillDirectory struct {
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

// MCPConfig 是发现到的全部 MCP 服务的集合。
type MCPConfig struct {
	Servers []MCPServer `json:"servers"`
}

// MCPServer 是一条归一化后的 MCP 服务配置。
// stdio 类服务使用 Command/Args/Env，HTTP/SSE 类服务使用 URL/Headers；
// 所有字符串值都已做过环境变量展开（${VAR}/$VAR）。
type MCPServer struct {
	Name    string            `json:"name"`
	Scope   string            `json:"scope"` // 来源作用域：project / user
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// rawMCPFile / rawMCPServer 对应 .mcp.json（以及 ~/.claude.json）的磁盘
// 格式：顶层为 "mcpServers" 对象，key 是服务名。解析后转换为 MCPServer。
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

// LoadStartupContext 是本包的总入口：在进程启动时执行一次完整的上下文
// 发现流程。步骤：先定位两个"锚点"目录（项目根 + 用户配置目录），
// 然后以它们为基准分别加载指令、规则、自动记忆、settings、MCP 配置
// 与 skill 目录。所有加载器都容忍文件缺失——本函数永不失败，
// 最坏情况下返回一个只有目录路径的空上下文。
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

// TranscriptDir 返回当前项目的会话记录目录：
// <configDir>/projects/<项目路径扁平化后的名字>。
// 项目路径被 sanitizeProjectPath 扁平化为单层目录名（分隔符替换为 "-"），
// 与 Claude Code 的 ~/.claude/projects/ 布局保持一致。
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

// KnowledgeDir 返回知识库（RAG 向量数据等）的存放目录，
// 固定挂在项目根下的 apps/server/data/knowledge。
func (c StartupContext) KnowledgeDir() string {
	root := strings.TrimSpace(c.ProjectRoot)
	if root == "" {
		root = detectProjectRoot()
	}
	return filepath.Join(root, "apps", "server", "data", "knowledge")
}

// StateDBPath 返回服务端 SQLite 状态库（会话/记忆/skill 配置等）的路径，
// 固定为项目根下的 apps/server/data/state.db。
func (c StartupContext) StateDBPath() string {
	root := strings.TrimSpace(c.ProjectRoot)
	if root == "" {
		root = detectProjectRoot()
	}
	return filepath.Join(root, "apps", "server", "data", "state.db")
}

// detectProjectRoot 定位项目根目录，优先级：
//  1. 环境变量 PROJECT_ROOT 显式指定（转绝对路径）；
//  2. 从当前工作目录逐级向上查找，第一个包含 go.mod / package.json / .git
//     任一标记文件的目录即视为项目根（与常见工具的 root 探测惯例一致）；
//  3. 爬到文件系统根仍未找到时，回退为当前工作目录本身。
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
		// 已到达文件系统根（父目录等于自身）：放弃探测，退回 cwd。
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// detectConfigDir 定位用户级配置目录，优先级：
//  1. 环境变量 CLAUDE_CONFIG_DIR 显式指定（转绝对路径）；
//  2. ~/.claude（与 Claude Code CLI 的默认约定一致）；
//  3. 取不到用户主目录时退回相对路径 ".claude"，保证总能返回可用值。
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

// loadInstructionFiles 按固定顺序收集四个 CLAUDE.md 候选位置：
//  1. <configDir>/CLAUDE.md      —— 用户级全局指令；
//  2. <projectRoot>/CLAUDE.md    —— 项目级指令（通常提交进仓库）；
//  3. <projectRoot>/.claude/CLAUDE.md —— 项目级指令的另一约定位置；
//  4. <projectRoot>/CLAUDE.local.md   —— 本地个人覆盖（通常 gitignore）。
//
// 不存在的文件直接跳过；不做内容合并，四份文件按"通用 → 具体"的顺序
// 依次全部注入 system prompt，由模型自行理解叠加关系。单文件上限 256KB，
// 超限文件被整体丢弃以防撑爆上下文。
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

// loadRuleFiles 从用户级 <configDir>/rules 与项目级 <projectRoot>/.claude/rules
// 两个目录递归收集规则文件，然后做稳定排序：先按作用域（user 在 project 前，
// 见 scopeOrder），同作用域内按路径字典序——保证注入 system prompt 的顺序
// 跨启动完全一致，避免提示词抖动影响缓存与可复现性。
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

// loadRulesFromDir 递归读取一个规则目录下的所有 *.md 文件。
// 每个文件用 splitFrontmatter 解析：正文作为规则内容，frontmatter 的
// paths 字段（可为列表）解析为路径限定——带 paths 的规则在渲染 system
// prompt 时只列出"路径 + 适用范围"索引而不内联全文，未带 paths 的规则
// 全文注入。读取失败或非 .md 文件一律跳过，不影响其他规则。
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

// loadAutoMemory 在配置目录下按优先级查找自动记忆文件：
// MEMORY.md 优先，其次 auto_memory.md，只取第一个存在的（不合并）。
// 自动记忆是 agent 跨会话沉淀的用户偏好/事实，上限 128KB，
// 找不到时返回 nil（渲染层据此跳过"自动记忆"小节）。
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

// loadSettings 依序读取三层 settings.json 并做递归深度合并：
//  1. <configDir>/settings.json            —— 用户级默认；
//  2. <projectRoot>/.claude/settings.json  —— 项目级共享配置；
//  3. <projectRoot>/.claude/settings.local.json —— 项目本地个人配置。
//
// 后加载者优先：同名标量键直接覆盖，同为对象的键递归合并（见 mergeMaps），
// 因此本地配置可以只写想覆盖的字段。文件缺失或 JSON 非法都静默跳过，
// 保证配置损坏不影响启动。
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

// loadMCP 从三个来源收集 MCP 服务配置：
//  1. <projectRoot>/.mcp.json           —— 项目级（scope=project）；
//  2. <configDir>/.mcp.json             —— 用户级（scope=user）；
//  3. <configDir 的父目录>/.claude.json —— Claude Code CLI 的主配置文件
//     （通常即 ~/.claude.json，其中也可内嵌 mcpServers），scope=user。
//
// 三个来源的服务简单拼接（不按名称去重，同名服务会并存，由消费方决策），
// 最后按 scope（user 先于 project）+ 名称做稳定排序，保证清单顺序确定。
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

// loadMCPFile 解析单个 MCP 配置文件（mcpServers 映射）。
// 文件缺失、JSON 非法或没有任何服务时返回 nil（静默容错）。
// 每个服务的 Command/Args/URL/Env/Headers 都会做环境变量展开
// （os.ExpandEnv），使配置文件可以引用 ${API_KEY} 之类的敏感值
// 而不必把明文写进磁盘。
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

// skillDirectories 返回实际存在的 skill 根目录，固定按以下顺序检查：
//  1. <projectRoot>/skills          —— 项目顶层 skills 目录（scope=project）；
//  2. <projectRoot>/.claude/skills  —— Claude Code 约定的项目 skill 目录；
//  3. <configDir>/skills            —— 用户个人 skill 目录（scope=personal）。
//
// 只返回存在的目录；顺序即 skills.Registry 的扫描顺序，决定了同名 skill
// 的覆盖关系（后扫描的用户级内容覆盖项目级，但保留先注册的排序位置）。
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

// splitFrontmatter 把规则 Markdown 切分为 YAML frontmatter 与正文。
// 与 skills 包中的同名函数是同一套"迷你 YAML"解析逻辑（有意保持独立
// 拷贝以避免包间耦合）：只支持 "key: value" 标量和 "- item" 块列表，
// 键统一小写；文件不以 "---\n" 开头或缺少结束分隔符时整体视为正文。
// 本包目前只消费 paths 键。
func splitFrontmatter(content string) (map[string]string, string) {
	// 统一 CRLF，兼容 Windows 编辑的文件。
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
	// currentKey 记录最近的键，供后续 "- item" 列表行归属。
	var currentKey string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && currentKey != "" {
			existing := meta[currentKey]
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			// 列表项以换行拼接，parseStringList 会再按换行拆开。
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

// parseStringList 把 frontmatter 值解析为字符串列表，兼容流式数组
// "[a, b]"、逗号分隔标量与块列表（换行分隔）三种写法，元素去引号去空白，
// 空元素丢弃。与 skills 包中的同名函数逻辑一致。
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

// trimYAMLScalar 清理 YAML 标量：去空白、剥掉包裹引号、再去空白。
func trimYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return strings.TrimSpace(value)
}

// readText 读取一个文本文件并去除首尾空白。
// limit（字节）用于防御：超过上限的文件被整体拒绝（返回 false）而非截断，
// 因为截断的指令/规则可能产生误导性的半句话。目录、不存在或读取失败
// 也返回 false。
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

// exists 判断路径是否存在（文件或目录均可）。
// 注意：非 NotExist 的错误（如权限错误）被视为"存在"，宁可后续读取
// 失败也不漏掉可能存在的配置。
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// mergeMaps 把 src 递归合并进 dst（就地修改 dst）：
// 两边同为对象的键递归下钻合并，否则 src 的值直接覆盖 dst。
// 这是 settings 三层叠加"后者优先、对象深合并"语义的实现基础。
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

// expandEnv 去空白后做环境变量展开（支持 $VAR 与 ${VAR}），
// 用于 MCP 配置中引用密钥等运行时环境值。
func expandEnv(value string) string {
	return os.ExpandEnv(strings.TrimSpace(value))
}

// expandEnvList 对字符串切片逐项做环境变量展开。
func expandEnvList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, expandEnv(value))
	}
	return out
}

// expandEnvMap 对映射的值逐项做环境变量展开；空映射返回 nil
// 以便 JSON 序列化时省略字段。
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

// sanitizeProjectPath 把项目绝对路径扁平化为可作单层目录名的字符串：
// 去掉首尾分隔符后，把路径分隔符与 ":"（兼容 Windows 盘符）都替换为 "-"。
// 例如 /Users/a/proj -> Users-a-proj，用于 TranscriptDir 的项目子目录名，
// 与 Claude Code 的 ~/.claude/projects/ 命名方式一致。
func sanitizeProjectPath(projectRoot string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	projectRoot = strings.Trim(projectRoot, string(os.PathSeparator))
	projectRoot = strings.ReplaceAll(projectRoot, string(os.PathSeparator), "-")
	projectRoot = strings.ReplaceAll(projectRoot, ":", "-")
	return strings.Trim(projectRoot, "-")
}

// scopeOrder 定义作用域的排序权重：user/personal(0) < project(1) <
// local(2) < 其他(3)。规则与 MCP 清单都按此排序，使"越通用越靠前、
// 越本地越靠后"，与 CLAUDE.md 指令的注入顺序保持同一语义。
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
