package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.byted.org/ai/lmy/apps/server/internal/agent"
	"code.byted.org/ai/lmy/apps/server/internal/contracts"
	"code.byted.org/ai/lmy/apps/server/internal/memory"
	"code.byted.org/ai/lmy/apps/server/internal/runtime"
)

var changedFiles sync.Map

func RegisterCoreCoder(registry *runtime.Runtime) {
	registry.Register(NewBashTool())
	registry.Register(NewReadFileTool())
	registry.Register(NewWriteFileTool())
	registry.Register(NewEditFileTool())
	registry.Register(NewGlobTool())
	registry.Register(NewGrepTool())
}

type BashTool struct {
	mu  sync.Mutex
	cwd map[string]string
}

func NewBashTool() *BashTool {
	return &BashTool{cwd: map[string]string{}}
}

func (t *BashTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:bash",
		Source:      "tool",
		Name:        "bash",
		Description: "执行 shell 命令，并返回 stdout、stderr 和退出码。适用于运行测试、安装依赖、执行 git 操作等。",
		InputSchema: runtime.Schema(map[string]any{
			"command": map[string]any{"type": "string", "description": "要执行的 shell 命令"},
			"timeout": map[string]any{"type": "integer", "description": "超时时间，单位秒（默认 120）"},
		}, []string{"command"}),
		Risk: contracts.RiskLow,
	}
}

func (t *BashTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	command, _ := input["command"].(string)
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("command is required")
	}
	if warning := checkDangerousCommand(command); warning != "" {
		return nil, fmt.Errorf("blocked dangerous command: %s; command: %s", warning, command)
	}
	timeout := intFromInput(input["timeout"], 120)
	if timeout <= 0 {
		timeout = 120
	}
	cwd := t.currentCWD(invokeCtx.ConversationID)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-lc", command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("Error: timed out after %ds", timeout), nil
	}
	if err == nil {
		t.updateCWD(invokeCtx.ConversationID, command, cwd)
	}
	out := stdout.String()
	if stderr.Len() > 0 {
		out += "\n[stderr]\n" + stderr.String()
	}
	if err != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		out += fmt.Sprintf("\n[exit code: %d]", exitCode)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		out = "(no output)"
	}
	return truncateHeadTail(out, 15000, 6000, 3000), nil
}

func (t *BashTool) currentCWD(conversationID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cwd := t.cwd[conversationID]; cwd != "" {
		return cwd
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	t.cwd[conversationID] = cwd
	return cwd
}

func (t *BashTool) updateCWD(conversationID string, command string, current string) {
	running := current
	changed := false
	for _, part := range strings.Split(command, "&&") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "cd ") {
			continue
		}
		target := strings.Trim(strings.TrimSpace(strings.TrimPrefix(part, "cd ")), `"'`)
		if target == "" {
			continue
		}
		if strings.HasPrefix(target, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				target = filepath.Join(home, strings.TrimPrefix(target, "~"))
			}
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(running, target)
		}
		target = filepath.Clean(target)
		if stat, err := os.Stat(target); err == nil && stat.IsDir() {
			running = target
			changed = true
		}
	}
	if changed {
		t.mu.Lock()
		t.cwd[conversationID] = running
		t.mu.Unlock()
	}
}

type ReadFileTool struct{}

func NewReadFileTool() ReadFileTool {
	return ReadFileTool{}
}

func (ReadFileTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:read_file",
		Source:      "tool",
		Name:        "read_file",
		Description: "读取文件内容并带上行号。编辑文件前应先读取文件。",
		InputSchema: runtime.Schema(map[string]any{
			"file_path": map[string]any{"type": "string", "description": "文件路径"},
			"offset":    map[string]any{"type": "integer", "description": "起始行号（从 1 开始，默认 1）"},
			"limit":     map[string]any{"type": "integer", "description": "最多读取的行数（默认 2000）"},
		}, []string{"file_path"}),
		Risk: contracts.RiskLow,
	}
}

func (ReadFileTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	filePath, _ := input["file_path"].(string)
	path, err := resolveUserPath(filePath)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Sprintf("Error: %s not found", filePath), nil
	}
	if stat.IsDir() {
		return fmt.Sprintf("Error: %s is a directory, not a file", filePath), nil
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	offset := intFromInput(input["offset"], 1)
	limit := intFromInput(input["limit"], 2000)
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	lines := strings.Split(strings.ReplaceAll(string(bytes), "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := offset - 1
	if start >= len(lines) {
		return "(empty range)", nil
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	out := make([]string, 0, end-start+1)
	for i := start; i < end; i++ {
		out = append(out, fmt.Sprintf("%d\t%s", i+1, lines[i]))
	}
	if len(lines) > end {
		out = append(out, fmt.Sprintf("... (%d lines total, showing %d-%d)", len(lines), start+1, end))
	}
	if len(out) == 0 {
		return "(empty file)", nil
	}
	return strings.Join(out, "\n"), nil
}

type WriteFileTool struct{}

func NewWriteFileTool() WriteFileTool {
	return WriteFileTool{}
}

func (WriteFileTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:write_file",
		Source:      "tool",
		Name:        "write_file",
		Description: "创建新文件或完整覆盖已有文件。对已有文件做小范围修改时，优先使用 edit_file。",
		InputSchema: runtime.Schema(map[string]any{
			"file_path": map[string]any{"type": "string", "description": "目标文件路径"},
			"content":   map[string]any{"type": "string", "description": "要写入的完整文件内容"},
		}, []string{"file_path", "content"}),
		Risk: contracts.RiskLow,
	}
}

func (WriteFileTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	filePath, _ := input["file_path"].(string)
	content, _ := input["content"].(string)
	path, err := resolveUserPath(filePath)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "Error: " + err.Error(), nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "Error: " + err.Error(), nil
	}
	changedFiles.Store(path, struct{}{})
	return fmt.Sprintf("Wrote %d lines to %s", countLines(content), filePath), nil
}

type EditFileTool struct{}

func NewEditFileTool() EditFileTool {
	return EditFileTool{}
}

func (EditFileTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:edit_file",
		Source:      "tool",
		Name:        "edit_file",
		Description: "通过精确字符串替换来编辑文件。为保证安全，old_string 必须在文件中只出现一次；请包含足够上下文以确保唯一。",
		InputSchema: runtime.Schema(map[string]any{
			"file_path":  map[string]any{"type": "string", "description": "要编辑的文件路径"},
			"old_string": map[string]any{"type": "string", "description": "要查找的精确文本（必须在文件中唯一）"},
			"new_string": map[string]any{"type": "string", "description": "替换后的文本"},
		}, []string{"file_path", "old_string", "new_string"}),
		Risk: contracts.RiskLow,
	}
}

func (EditFileTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	filePath, _ := input["file_path"].(string)
	oldString, _ := input["old_string"].(string)
	newString, _ := input["new_string"].(string)
	path, err := resolveUserPath(filePath)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error: %s not found", filePath), nil
	}
	content := string(bytes)
	occurrences := strings.Count(content, oldString)
	if occurrences == 0 {
		preview := content
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		return fmt.Sprintf("Error: old_string not found in %s.\nFile starts with:\n%s", filePath, preview), nil
	}
	if occurrences > 1 {
		return fmt.Sprintf("Error: old_string appears %d times in %s. Include more surrounding lines to make it unique.", occurrences, filePath), nil
	}
	newContent := strings.Replace(content, oldString, newString, 1)
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return "Error: " + err.Error(), nil
	}
	changedFiles.Store(path, struct{}{})
	return "Edited " + filePath + "\n" + unifiedDiff(content, newContent, path), nil
}

type GlobTool struct{}

func NewGlobTool() GlobTool {
	return GlobTool{}
}

func (GlobTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:glob",
		Source:      "tool",
		Name:        "glob",
		Description: "查找匹配 glob 模式的文件。支持使用 ** 进行递归匹配（例如 '**/*.py'）。",
		InputSchema: runtime.Schema(map[string]any{
			"pattern": map[string]any{"type": "string", "description": "Glob 模式，例如 '**/*.py' 或 'src/**/*.ts'"},
			"path":    map[string]any{"type": "string", "description": "搜索目录（默认当前工作目录）"},
		}, []string{"pattern"}),
		Risk: contracts.RiskLow,
	}
}

func (GlobTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	pattern, _ := input["pattern"].(string)
	baseInput, _ := input["path"].(string)
	if baseInput == "" {
		baseInput = "."
	}
	base, err := resolveUserPath(baseInput)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	stat, err := os.Stat(base)
	if err != nil || !stat.IsDir() {
		return fmt.Sprintf("Error: %s is not a directory", baseInput), nil
	}
	re, err := globRegexp(pattern)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	type hit struct {
		path    string
		modTime time.Time
	}
	hits := []hit{}
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !re.MatchString(rel) {
			return nil
		}
		info, _ := d.Info()
		mod := time.Time{}
		if info != nil {
			mod = info.ModTime()
		}
		hits = append(hits, hit{path: path, modTime: mod})
		return nil
	})
	sort.Slice(hits, func(i, j int) bool { return hits[i].modTime.After(hits[j].modTime) })
	limit := len(hits)
	if limit > 100 {
		limit = 100
	}
	lines := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		lines = append(lines, hits[i].path)
	}
	if len(hits) > 100 {
		lines = append(lines, fmt.Sprintf("... (%d matches, showing first 100)", len(hits)))
	}
	if len(lines) == 0 {
		return "没有匹配的文件。", nil
	}
	return strings.Join(lines, "\n"), nil
}

type GrepTool struct{}

func NewGrepTool() GrepTool {
	return GrepTool{}
}

func (GrepTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:grep",
		Source:      "tool",
		Name:        "grep",
		Description: "使用正则搜索文件内容，返回匹配行及其文件路径和行号。",
		InputSchema: runtime.Schema(map[string]any{
			"pattern": map[string]any{"type": "string", "description": "要搜索的正则表达式"},
			"path":    map[string]any{"type": "string", "description": "要搜索的文件或目录（默认当前工作目录）"},
			"include": map[string]any{"type": "string", "description": "仅搜索匹配该 glob 的文件（例如 '*.py'）"},
		}, []string{"pattern"}),
		Risk: contracts.RiskLow,
	}
}

func (GrepTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	pattern, _ := input["pattern"].(string)
	pathInput, _ := input["path"].(string)
	include, _ := input["include"].(string)
	if pathInput == "" {
		pathInput = "."
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return "Invalid regex: " + err.Error(), nil
	}
	base, err := resolveUserPath(pathInput)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	stat, err := os.Stat(base)
	if err != nil {
		return fmt.Sprintf("Error: %s not found", pathInput), nil
	}
	files := []string{}
	if !stat.IsDir() {
		files = append(files, base)
	} else {
		files = walkGrepFiles(base, include)
	}
	matches := []string{}
	for _, file := range files {
		bytes, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(strings.ReplaceAll(string(bytes), "\r\n", "\n"), "\n") {
			if regex.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", file, i+1, strings.TrimRight(line, "\n")))
				if len(matches) >= 200 {
					matches = append(matches, "... (200 match limit reached)")
					return strings.Join(matches, "\n"), nil
				}
			}
		}
	}
	if len(matches) == 0 {
		return "没有找到匹配结果。", nil
	}
	return strings.Join(matches, "\n"), nil
}

type AgentTool struct {
	parent *agent.Agent
}

func NewAgentTool(parent *agent.Agent) AgentTool {
	return AgentTool{parent: parent}
}

func (AgentTool) Tool() contracts.RuntimeTool {
	return contracts.RuntimeTool{
		ID:          "tool:agent",
		Source:      "tool",
		Name:        "agent",
		Description: "启动一个子代理独立处理复杂子任务。子代理拥有自己的上下文和工具访问能力。",
		InputSchema: runtime.Schema(map[string]any{
			"task": map[string]any{"type": "string", "description": "子代理需要完成的任务"},
		}, []string{"task"}),
		Risk: contracts.RiskLow,
	}
}

func (t AgentTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	task, _ := input["task"].(string)
	if strings.TrimSpace(task) == "" {
		return "Error: task is required", nil
	}
	if t.parent == nil {
		return "Error: agent tool not initialized (no parent agent)", nil
	}
	subStore := memory.NewInMemoryStore()
	subRuntime := t.parent.CloneRuntimeWithout("agent")
	modelClient := invokeCtx.Model
	if modelClient == nil {
		modelClient = t.parent.Model()
	}
	subAgent := agent.NewAgent(subStore, subRuntime, modelClient, t.parent.SkillRegistry(), t.parent.SkillConfig(), t.parent.StartupContext())
	subAgent.SetMaxRounds(20)
	conversation := subStore.CreateConversation("Sub-agent")
	output, err := subAgent.Run(ctx, agent.RunInput{ConversationID: conversation.ID, UserMessage: task})
	if err != nil {
		return "Sub-agent error: " + err.Error(), nil
	}
	result := output.AssistantMessage.Content
	if len(result) > 5000 {
		result = result[:4500] + "\n... (sub-agent output truncated)"
	}
	return "[Sub-agent completed]\n" + result, nil
}

func checkDangerousCommand(command string) string {
	patterns := []struct {
		reason  string
		pattern string
	}{
		{"recursive delete on home/root", `\brm\s+(-\w*)?-r\w*\s+(/|~|\$HOME)`},
		{"force recursive delete", `\brm\b.*-\w*[rR]\w*.*-\w*f|\brm\b.*-\w*f\w*.*-\w*[rR]`},
		{"force recursive delete", `\brm\b.*--recursive\b.*--force\b|\brm\b.*--force\b.*--recursive\b`},
		{"format filesystem", `\bmkfs\b`},
		{"raw disk write", `\bdd\s+.*of=/dev/`},
		{"overwrite block device", `>\s*/dev/sd[a-z]`},
		{"chmod 777 on root", `\bchmod\s+(-R\s+)?777\s+/`},
		{"fork bomb", `:\(\)\s*\{.*:\|:.*\}`},
		{"pipe curl to shell", `\bcurl\b.*\|\s*(sudo\s+)?(ba)?sh\b`},
		{"pipe wget to shell", `\bwget\b.*\|\s*(sudo\s+)?(ba)?sh\b`},
	}
	for _, item := range patterns {
		if regexp.MustCompile(item.pattern).FindString(command) != "" {
			return item.reason
		}
	}
	return ""
}

func resolveUserPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~"))
	}
	if !filepath.IsAbs(value) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		value = filepath.Join(cwd, value)
	}
	return filepath.Clean(value), nil
}

func intFromInput(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	return fallback
}

func truncateHeadTail(value string, max int, head int, tail int) string {
	if len(value) <= max {
		return value
	}
	return value[:head] + fmt.Sprintf("\n\n... truncated (%d chars total) ...\n\n", len(value)) + value[len(value)-tail:]
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	count := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

func unifiedDiff(old string, new string, filename string) string {
	oldLines := strings.Split(old, "\n")
	newLines := strings.Split(new, "\n")
	var out strings.Builder
	out.WriteString("--- a/" + filename + "\n")
	out.WriteString("+++ b/" + filename + "\n")
	out.WriteString("@@\n")
	limit := maxInt(len(oldLines), len(newLines))
	for i := 0; i < limit; i++ {
		var oldLine, newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine == newLine {
			continue
		}
		if i < len(oldLines) {
			out.WriteString("-" + oldLine + "\n")
		}
		if i < len(newLines) {
			out.WriteString("+" + newLine + "\n")
		}
		if out.Len() > 3000 {
			return out.String()[:2500] + "\n... (diff truncated)\n"
		}
	}
	return out.String()
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if ch == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				out.WriteString(".*")
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					out.WriteString("(/)?")
					i++
				}
			} else {
				out.WriteString(`[^/]*`)
			}
			continue
		}
		if ch == '?' {
			out.WriteString(`[^/]`)
			continue
		}
		out.WriteString(regexp.QuoteMeta(string(ch)))
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}

func walkGrepFiles(root string, include string) []string {
	skipDirs := map[string]struct{}{
		".git": {}, "node_modules": {}, "__pycache__": {}, ".venv": {}, "venv": {}, ".tox": {}, "dist": {}, "build": {},
	}
	files := []string{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if include != "" {
			matched, err := filepath.Match(include, filepath.Base(path))
			if err != nil || !matched {
				return nil
			}
		}
		files = append(files, path)
		if len(files) >= 5000 {
			return filepath.SkipAll
		}
		return nil
	})
	return files
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
