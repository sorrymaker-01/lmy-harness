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

	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/agent"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/contracts"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/memory"
	"github.com/sorrymaker-01/lmy-harness/apps/server/internal/runtime"
)

// changedFiles 进程级记录所有被 write_file / edit_file 修改过的文件绝对路径（value 无意义）。
// 用 sync.Map 是因为 agent 可能并发执行多个工具调用；该集合可供其他模块（如变更审计）读取。
var changedFiles sync.Map

// RegisterCoreCoder 把“核心编码”六件套注册进工具运行时：
// bash（shell 执行）、read_file / write_file / edit_file（文件读写编辑）、
// glob（文件名匹配）、grep（内容正则搜索）。这组工具构成 agent 编码能力的基础。
func RegisterCoreCoder(registry *runtime.Runtime) {
	registry.Register(NewBashTool())
	registry.Register(NewReadFileTool())
	registry.Register(NewWriteFileTool())
	registry.Register(NewEditFileTool())
	registry.Register(NewGlobTool())
	registry.Register(NewGrepTool())
}

// BashTool 执行 shell 命令。它是唯一带跨调用状态的核心工具：
// 按会话 ID 记录“逻辑工作目录”（cwd），模拟出交互式 shell 中 `cd` 能持久生效的体验——
// 因为每次调用都是新起的 bash 子进程，cd 本身不会真正跨进程保留。
type BashTool struct {
	mu  sync.Mutex        // 保护 cwd map（多个工具调用可能并发执行）
	cwd map[string]string // conversationID -> 该会话当前的工作目录
}

// NewBashTool 创建 bash 工具实例。注意返回指针，因为它内部有可变状态。
func NewBashTool() *BashTool {
	return &BashTool{cwd: map[string]string{}}
}

// Tool 返回 bash 工具的元信息：参数为 command（必填）与 timeout 秒数（可选，默认 120）。
// 风险等级声明为 low——运行时策略层不拦截，安全性由 Invoke 内的危险命令黑名单保证。
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

// Invoke 执行 shell 命令。实现关键点：
//   - 安全：执行前先过 checkDangerousCommand 黑名单（rm -rf /、mkfs、fork bomb、curl|sh 等），
//     命中即返回 error 拒绝执行——这是 bash 声明为 RiskLow 的前提；
//   - 超时：用 context.WithTimeout 强制截断，超时返回文本错误（而非 Go error），让模型可感知并调整；
//   - 执行方式：`bash -lc`（login shell），以便加载用户 profile 中的 PATH/环境变量；
//   - cwd：从会话级 map 取当前目录；命令成功后解析其中的 `cd` 更新记录（见 updateCWD）；
//   - 输出：stdout + stderr（带 [stderr] 标记）+ 非零退出码（带 [exit code] 标记）合并成一段文本，
//     并做“头 6000 + 尾 3000”的截断，避免超长输出撑爆模型上下文。
func (t *BashTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	command, _ := input["command"].(string)
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("command is required")
	}
	// 危险命令黑名单：命中直接以 error 形式拒绝（会变成 ToolResult.Error 反馈给模型）。
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

	// -l：login shell，加载 profile；-c：执行命令字符串。工作目录用会话记录的逻辑 cwd。
	cmd := exec.CommandContext(runCtx, "bash", "-lc", command)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		// 超时作为普通文本输出返回（nil error），模型能读到并决定加大 timeout 或换策略。
		return fmt.Sprintf("Error: timed out after %ds", timeout), nil
	}
	if err == nil {
		// 仅在命令整体成功时才更新逻辑 cwd，避免 `cd 不存在的目录 && ...` 失败后污染状态。
		t.updateCWD(invokeCtx.ConversationID, command, cwd)
	}
	out := stdout.String()
	if stderr.Len() > 0 {
		out += "\n[stderr]\n" + stderr.String()
	}
	if err != nil {
		// 非零退出不视为工具失败，而是把退出码附在输出里，让模型自行判断。
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
	// 头尾截断：总长超过 15000 字符时保留开头 6000 + 结尾 3000（错误信息常在结尾）。
	return truncateHeadTail(out, 15000, 6000, 3000), nil
}

// currentCWD 返回该会话当前的逻辑工作目录；首次调用时懒初始化为服务进程的 cwd。
// 加锁保护 map，因为工具可能被并发调用。
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

// updateCWD 在命令成功后，从命令文本中静态解析 `cd` 片段来推进逻辑工作目录。
// 做法：按 "&&" 拆分命令，逐段找以 "cd " 开头的片段，依次解析目标路径
// （支持 ~ 展开、相对路径基于“上一段 cd 后的目录”累积解析），
// 并用 os.Stat 校验目标确实是已存在的目录后才采纳。
// 这是一个启发式方案：不理解 `;`、`||`、子 shell、变量展开等复杂语法，
// 但覆盖了模型最常见的 `cd xxx && do-something` 写法，且校验存在性保证不会记录非法目录。
func (t *BashTool) updateCWD(conversationID string, command string, current string) {
	running := current
	changed := false
	for _, part := range strings.Split(command, "&&") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "cd ") {
			continue
		}
		// 去掉包裹路径的引号（cd "some dir" / cd 'dir'）。
		target := strings.Trim(strings.TrimSpace(strings.TrimPrefix(part, "cd ")), `"'`)
		if target == "" {
			continue
		}
		// ~ 展开为用户 home 目录。
		if strings.HasPrefix(target, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				target = filepath.Join(home, strings.TrimPrefix(target, "~"))
			}
		}
		// 相对路径基于当前累积目录解析（同一命令里多个 cd 会依次生效）。
		if !filepath.IsAbs(target) {
			target = filepath.Join(running, target)
		}
		target = filepath.Clean(target)
		// 只有目标真实存在且是目录时才更新，避免记录错误路径。
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

// ReadFileTool 读取文件内容并按 "行号\t内容" 格式返回。
// 带行号是刻意设计：让模型能精确引用行位置，也与 edit_file 的“先读后改”约定配合。
type ReadFileTool struct{}

// NewReadFileTool 创建 read_file 工具（无状态，值类型即可）。
func NewReadFileTool() ReadFileTool {
	return ReadFileTool{}
}

// Tool 返回 read_file 的元信息：file_path 必填，offset/limit 支持分页读取大文件
// （默认从第 1 行起最多读 2000 行）。
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

// Invoke 读取文件。实现关键点：
//   - 路径经 resolveUserPath 归一化（~ 展开、相对路径转绝对、Clean）；
//   - 所有业务错误（不存在、是目录等）都以 "Error: ..." 文本返回而非 Go error，
//     便于模型读到具体原因后自我纠正；
//   - CRLF 统一为 LF 后按行切分；末尾空行剔除，保证行数统计符合直觉；
//   - offset 从 1 开始（对模型更自然），内部转 0 基索引；越界返回 "(empty range)"；
//   - 每行输出 "行号\t内容"；若还有后续行，追加一条总行数提示，引导模型继续分页读取。
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
	// 统一换行符后按行切分；Split 会在文件以 \n 结尾时多出一个空元素，需剔除。
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
		// 行号从 1 开始，与编辑器/编译器报错的习惯一致。
		out = append(out, fmt.Sprintf("%d\t%s", i+1, lines[i]))
	}
	if len(lines) > end {
		// 提示总行数与当前窗口，方便模型决定是否用 offset 继续读。
		out = append(out, fmt.Sprintf("... (%d lines total, showing %d-%d)", len(lines), start+1, end))
	}
	if len(out) == 0 {
		return "(empty file)", nil
	}
	return strings.Join(out, "\n"), nil
}

// WriteFileTool 创建新文件或整体覆盖已有文件。
// 与 edit_file 的分工：write_file 用于新建/全量重写，edit_file 用于局部精确修改。
type WriteFileTool struct{}

// NewWriteFileTool 创建 write_file 工具（无状态）。
func NewWriteFileTool() WriteFileTool {
	return WriteFileTool{}
}

// Tool 返回 write_file 的元信息：file_path 与 content 均必填。
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

// Invoke 写文件。实现关键点：
//   - 自动 MkdirAll 创建缺失的父目录，模型无需先手动建目录；
//   - 0o644/0o755 常规权限；写入成功后把绝对路径记入 changedFiles 供变更追踪；
//   - 返回写入行数摘要而不是回显全文，节省模型上下文。
func (WriteFileTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	filePath, _ := input["file_path"].(string)
	content, _ := input["content"].(string)
	path, err := resolveUserPath(filePath)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	// 先确保父目录存在，避免 WriteFile 因目录缺失而失败。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "Error: " + err.Error(), nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "Error: " + err.Error(), nil
	}
	// 记录变更文件（进程级），供审计/统计使用。
	changedFiles.Store(path, struct{}{})
	return fmt.Sprintf("Wrote %d lines to %s", countLines(content), filePath), nil
}

// EditFileTool 通过“精确字符串替换”编辑文件（old_string -> new_string）。
// 采用字符串匹配而不是行号补丁，是因为模型给出的行号常有偏差，而唯一文本锚点更稳健。
type EditFileTool struct{}

// NewEditFileTool 创建 edit_file 工具（无状态）。
func NewEditFileTool() EditFileTool {
	return EditFileTool{}
}

// Tool 返回 edit_file 的元信息：file_path/old_string/new_string 均必填；
// 描述中明确要求 old_string 在文件中唯一，引导模型带足上下文。
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

// Invoke 执行精确替换编辑。安全与可恢复性设计：
//   - 唯一性校验：old_string 出现 0 次 → 报错并附文件开头 500 字符预览，帮模型定位真实内容；
//     出现多次 → 报错并要求补充上下文，防止替换到非预期位置；恰好 1 次才执行替换；
//   - 编辑成功后返回一个简化版 unified diff，让模型（和用户）直观确认改动内容；
//   - 与 write_file 一样把路径记入 changedFiles。
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
		// 未命中：附上文件开头预览，提示模型内容可能与它记忆中的不一致（应先 read_file）。
		preview := content
		if len(preview) > 500 {
			preview = preview[:500] + "..."
		}
		return fmt.Sprintf("Error: old_string not found in %s.\nFile starts with:\n%s", filePath, preview), nil
	}
	if occurrences > 1 {
		// 多处命中：拒绝执行，避免误改；引导模型加入更多包围行使锚点唯一。
		return fmt.Sprintf("Error: old_string appears %d times in %s. Include more surrounding lines to make it unique.", occurrences, filePath), nil
	}
	// 只替换第一处（此时也只有一处）。
	newContent := strings.Replace(content, oldString, newString, 1)
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		return "Error: " + err.Error(), nil
	}
	changedFiles.Store(path, struct{}{})
	// 返回 diff 便于模型确认改动符合预期。
	return "Edited " + filePath + "\n" + unifiedDiff(content, newContent, path), nil
}

// GlobTool 按 glob 模式查找文件（支持 ** 递归），结果按修改时间倒序返回。
// 不使用标准库 filepath.Glob 是因为它不支持 **；这里把 glob 编译成正则后配合 WalkDir 匹配。
type GlobTool struct{}

// NewGlobTool 创建 glob 工具（无状态）。
func NewGlobTool() GlobTool {
	return GlobTool{}
}

// Tool 返回 glob 的元信息：pattern 必填，path 可选（默认当前工作目录）。
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

// Invoke 执行 glob 匹配。实现关键点：
//   - 把 glob 模式经 globRegexp 编译成正则（** → .*，* → [^/]*，? → [^/]），
//     再用 WalkDir 遍历目录树，对“相对 base 的 slash 路径”做整串匹配；
//   - 匹配结果按文件修改时间倒序排（最近改过的文件通常最相关）；
//   - 最多返回 100 条并附总数提示，防止大仓库把上下文刷爆。
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
	// 遍历错误一律忽略（如权限不足的子目录），尽力返回能拿到的结果。
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		// 统一为 slash 分隔，保证 glob 语义跨平台一致（Windows 上也用 /）。
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
	// 按修改时间从新到旧排序：最近被编辑的文件更可能是模型想找的。
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

// GrepTool 用 Go 标准库 regexp（RE2 语法）在文件内容中做逐行正则搜索。
// 纯 Go 实现而非调用系统 grep/ripgrep，避免了外部依赖，行为跨平台一致。
type GrepTool struct{}

// NewGrepTool 创建 grep 工具（无状态）。
func NewGrepTool() GrepTool {
	return GrepTool{}
}

// Tool 返回 grep 的元信息：pattern（正则）必填；path 可以是文件或目录；
// include 是文件名 glob 过滤器（如 "*.go"），只作用于 basename。
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

// Invoke 执行内容搜索。实现关键点：
//   - 先编译正则，非法正则以文本形式报错（模型可修正语法后重试）；
//   - path 是单文件则只搜该文件；是目录则由 walkGrepFiles 收集候选文件
//     （自动跳过 .git/node_modules 等噪音目录，最多收集 5000 个文件）；
//   - 逐文件整读入内存、按行匹配，输出 "文件:行号: 内容" 格式（与 grep -n 一致）；
//   - 命中 200 条即提前返回并附提示，控制输出规模；读文件失败（二进制/权限）静默跳过。
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
			// 读不了的文件直接跳过，不让个别坏文件影响整体搜索。
			continue
		}
		for i, line := range strings.Split(strings.ReplaceAll(string(bytes), "\r\n", "\n"), "\n") {
			if regex.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", file, i+1, strings.TrimRight(line, "\n")))
				if len(matches) >= 200 {
					// 达到上限立即返回，避免在超大仓库中继续无谓扫描。
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

// AgentTool 是“子 Agent”工具：让主 Agent 把一个复杂子任务派发给独立的子 Agent 执行，
// 子任务的中间过程（多轮工具调用）不占用主会话上下文，只把最终结论带回。
// 持有 parent 引用是为了复用父 Agent 的运行时、模型、技能注册表与启动上下文。
type AgentTool struct {
	parent *agent.Agent
}

// NewAgentTool 创建子 Agent 工具。它不在 RegisterCoreCoder 中注册，
// 而是由 http server 在构造出主 Agent 之后单独注册（因为需要 parent 引用，存在构造顺序依赖）。
func NewAgentTool(parent *agent.Agent) AgentTool {
	return AgentTool{parent: parent}
}

// Tool 返回 agent 工具的元信息：仅一个 task 参数（自然语言任务描述）。
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

// Invoke 派生并运行一个子 Agent。隔离与防护设计：
//   - 记忆隔离：子 Agent 用全新的 InMemoryStore 和新会话，不读也不写主会话历史，
//     保证子任务上下文干净、结束即丢弃；
//   - 递归防护：工具集用 CloneRuntimeWithout("agent") 克隆，剔除 agent 工具本身，
//     使子 Agent 无法再派生孙 Agent，嵌套深度被硬性限制为 1；
//   - 模型复用：优先用调用上下文注入的模型客户端（支持多模型场景下与当前会话保持一致），
//     否则回退到父 Agent 的默认模型；技能注册表/配置/启动上下文均沿用父 Agent；
//   - 资源上限：SetMaxRounds(20) 限制子 Agent 最多 20 轮工具循环，防失控；
//   - 输出裁剪：最终回答超 5000 字符时截到 4500 并标注，避免子 Agent 输出反噬主会话上下文。
func (t AgentTool) Invoke(ctx context.Context, input map[string]any, invokeCtx runtime.InvocationContext) (any, error) {
	task, _ := input["task"].(string)
	if strings.TrimSpace(task) == "" {
		return "Error: task is required", nil
	}
	if t.parent == nil {
		return "Error: agent tool not initialized (no parent agent)", nil
	}
	// 独立的内存态存储：子 Agent 的会话/记忆与主会话完全隔离。
	subStore := memory.NewInMemoryStore()
	// 克隆运行时并去掉 "agent" 工具，阻断递归派生。
	subRuntime := t.parent.CloneRuntimeWithout("agent")
	modelClient := invokeCtx.Model
	if modelClient == nil {
		modelClient = t.parent.Model()
	}
	subAgent := agent.NewAgent(subStore, subRuntime, modelClient, t.parent.SkillRegistry(), t.parent.SkillConfig(), t.parent.StartupContext())
	subAgent.SetMaxRounds(20)
	conversation := subStore.CreateConversation("Sub-agent")
	// 同步阻塞运行子 Agent，直到它给出最终回答或出错。
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

// checkDangerousCommand 是 bash 工具的安全黑名单：用一组正则识别高破坏性命令模式，
// 命中返回原因（非空即拦截），未命中返回空串。
// 覆盖场景：对根目录/home 的递归删除、rm -rf（任意顺序的 r/f 组合及长选项写法）、
// mkfs 格式化、dd 直写块设备、重定向覆盖磁盘设备、chmod 777 /、fork bomb、
// curl/wget 管道到 shell 执行（远程代码执行）。
// 注意这是黑名单式启发，无法穷尽所有危险写法，属于“兜底最后防线”而非完备沙箱。
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

// resolveUserPath 把模型给出的路径归一化为干净的绝对路径：
// 空路径报错；~ 前缀展开为用户 home；相对路径基于服务进程的 cwd 补全；最后 Clean 去除 ./..。
// 注意：这里只做归一化，不做目录逃逸限制——文件工具可以访问进程有权限的任意路径。
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

// intFromInput 宽容地把模型传入的 JSON 值转成 int。
// JSON 数字反序列化成 map[string]any 后是 float64，模型偶尔还会传字符串数字，
// 这里统一兼容 int/int64/float64/数字字符串，转换失败返回 fallback 默认值。
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

// truncateHeadTail 对超长文本做“保头保尾”截断：超过 max 时保留开头 head 字符和
// 结尾 tail 字符，中间以省略标记（含原始总长）替代。
// 之所以头尾都保留：命令输出的开头通常有关键上下文，而错误/结论往往在结尾。
func truncateHeadTail(value string, max int, head int, tail int) string {
	if len(value) <= max {
		return value
	}
	return value[:head] + fmt.Sprintf("\n\n... truncated (%d chars total) ...\n\n", len(value)) + value[len(value)-tail:]
}

// countLines 统计文本行数：按 \n 计数，末尾没有换行符的最后一行也算一行。
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

// unifiedDiff 生成一个简化版的 unified diff（供 edit_file 回显改动）。
// 简化点：不做 LCS 最长公共子序列对齐，只按相同行号逐行比较（行发生增删错位后
// 后续行会全部被视为不同），也没有 hunk 头的行号范围；输出超过 3000 字符时截断。
// 对 edit_file 这种“单点局部替换”场景足够直观，且实现开销极小。
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
		// 同行号内容一致就跳过，只输出差异行。
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

// globRegexp 把 glob 模式编译为锚定全串（^...$）的正则表达式。转换规则：
//   - "**"  → ".*"（跨目录层级任意匹配）；若其后紧跟 "/"，则该斜杠译为 "(/)?" 可选，
//     这样 "**/*.go" 也能匹配根目录下的 "main.go"（零层目录的情况）；
//   - "*"   → "[^/]*"（单层内任意字符，不跨目录分隔符）；
//   - "?"   → "[^/]"（单层内任意单字符）；
//   - 其余字符一律 QuoteMeta 转义，防止 "." "+" 等被当作正则元字符。
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
					// "**/" 中的斜杠可选：允许匹配零层目录。
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

// walkGrepFiles 为 grep 收集候选文件列表：
//   - 跳过常见的噪音/生成目录（.git、node_modules、虚拟环境、构建产物等），
//     既提速也避免搜到无意义的匹配；root 本身即使重名也不跳过；
//   - include 非空时用 filepath.Match 对文件 basename 做 glob 过滤（如 "*.go"）；
//   - 最多收集 5000 个文件即 SkipAll 终止遍历，为超大仓库设置硬上限。
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

// maxInt 返回两个整数中的较大者（Go 1.21 之前无内置 max，故自行实现）。
func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
