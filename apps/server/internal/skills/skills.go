// Package skills 实现智能体的 Skill（技能，即"渐进加载的提示词包"）子系统。
//
// 核心概念：
//   - Skill 不是可执行工具，而是一份以 SKILL.md 为载体的提示词/上下文包
//     （说明、示例、支持资源），只有在需要时才会被完整注入模型上下文；
//   - Registry 负责从多个目录树（项目 skills/、.claude/skills/、用户
//     ~/.claude/skills/ 等，由 claudecode 包发现）递归扫描并注册 SKILL.md，
//     支持解析 YAML frontmatter 元数据；
//   - ConfigStore 负责 skill 的启用/禁用/软删除状态管理，以及对 skill 内容
//     编辑结果的持久化（内存、JSON 文件、SQLite 三种后端）；
//   - Registry.Resolve 提供两条轻量匹配途径（"/命令" 显式选择与触发词启发式
//     匹配），配合 agent 的渐进加载策略：system prompt 中只放 skill 元数据
//     清单，命中匹配（或模型显式请求）后才把完整 skill 包作为一条消息注入，
//     从而节省上下文窗口。
package skills

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manifest 是 skill 的"轻量元数据"视图：不含 SKILL.md 正文、示例和资源，
// 只包含名称、用途、触发词等摘要信息。渐进加载的第一阶段就是把这份元数据
// 列表渲染进 system prompt，让模型知道有哪些 skill 可用而无需付出完整正文
// 的 token 成本。字段基本与 SKILL.md 的 YAML frontmatter 一一对应。
type Manifest struct {
	ID   string `json:"id"`   // 稳定标识，形如 "skill:<name>"
	Name string `json:"name"` // 归一化后的小写名称，也是注册表的 key
	// Purpose 与 Description 通常同源（frontmatter 的 description），
	// Purpose 用于 system prompt 中的一句话用途说明。
	Purpose     string `json:"purpose"`
	Description string `json:"description"`
	// WhenToUse 来自 frontmatter 的 when_to_use / when-to-use，描述适用场景，
	// 缺省时回退到 description。
	WhenToUse string `json:"whenToUse,omitempty"`
	// Triggers 是触发词列表：用户消息中包含任一触发词（大小写不敏感）时，
	// 启发式匹配会自动加载该 skill 的完整包。
	Triggers []string `json:"triggers"`
	// Source 记录 skill 的来源作用域（project / personal 等），Path 是
	// SKILL.md 的绝对路径，二者用于溯源与提示展示。
	Source string `json:"source,omitempty"`
	Path   string `json:"path,omitempty"`
	// DisableModelInvocation 为 true 时该 skill 不进入 system prompt 清单、
	// 不参与启发式匹配、也不允许模型通过 <load_skill> 请求加载——
	// 即只允许用户通过 /name 显式调用。
	DisableModelInvocation bool `json:"disableModelInvocation,omitempty"`
	// UserInvocable 控制是否允许用户用 "/name" 斜杠命令显式调用，默认 true。
	UserInvocable bool `json:"userInvocable"`
	// AllowedTools / DisallowedTools 声明该 skill 期望的工具白名单/黑名单
	//（透传给模型作为提示约束，并非运行时强制）。
	AllowedTools    []string `json:"allowedTools,omitempty"`
	DisallowedTools []string `json:"disallowedTools,omitempty"`
	// 以下字段为 frontmatter 中的可选执行偏好（模型、推理强度、上下文模式、
	// 子代理、shell），原样透传给渲染层。
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Context string `json:"context,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Shell   string `json:"shell,omitempty"`
	// Enabled 是"当前是否启用"的运行时状态，由 ConfigStore 在 List 时填充，
	// 不来自 SKILL.md 本身。
	Enabled bool `json:"enabled"`
}

// Detail 是 skill 的"完整包"视图：在 Manifest 元数据之上，附带 SKILL.md
// 正文（Readme/Instructions）、对话示例和支持资源。渐进加载的第二阶段
// （用户 / 命令、触发词命中或模型请求）才会把 Detail 渲染注入模型上下文。
type Detail struct {
	Manifest
	// Readme 与 Instructions 初始加载时都取 SKILL.md 去掉 frontmatter 后的
	// 正文；二者分开存储是为了允许后续通过 API 编辑时各自独立覆盖。
	Readme       string     `json:"readme"`
	Instructions string     `json:"instructions"`
	Examples     []Example  `json:"examples"`
	Resources    []Resource `json:"resources"`
}

// Example 是一条"用户输入 → 助手回复"的少样本示例，随完整 skill 包一起
// 注入模型上下文，帮助模型模仿 skill 期望的回答风格。
type Example struct {
	Name      string `json:"name,omitempty"`
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

// Resource 是 skill 目录下 SKILL.md 之外的支持文件（脚本、参考文档等）。
// 小于 64KB 的文本文件会把内容直接内联到 Content，否则只保留 URI（磁盘
// 路径）供按需读取。
type Resource struct {
	Name    string `json:"name"`              // 相对 skill 目录的路径
	Type    string `json:"type"`              // 由扩展名推断的类型，无扩展名时为 "document"
	Content string `json:"content,omitempty"` // 内联的文本内容（可能为空）
	URI     string `json:"uri,omitempty"`     // 文件的磁盘绝对路径
}

// Skill 是注册表内部保存的 skill 实体。字段全部私有，外部只能通过
// Manifest()/Detail() 拿到副本，避免调用方绕过锁直接修改注册表状态。
type Skill struct {
	manifest     Manifest
	readme       string
	instructions string
	examples     []Example
	resources    []Resource
}

// Directory 描述一个待扫描的 skill 根目录及其作用域（如 project/personal），
// 通常由 claudecode.StartupContext.SkillDirectories 发现后传入。
type Directory struct {
	Path  string
	Scope string
}

// Manifest 返回该 skill 的轻量元数据副本（值拷贝，可安全对外暴露）。
func (s Skill) Manifest() Manifest {
	return s.manifest
}

// Detail 返回该 skill 的完整包副本。切片字段做了防御性拷贝，
// 防止调用方修改返回值时污染注册表内部状态。
func (s Skill) Detail() Detail {
	return Detail{
		Manifest:     s.manifest,
		Readme:       s.readme,
		Instructions: s.instructions,
		Examples:     append([]Example(nil), s.examples...),
		Resources:    append([]Resource(nil), s.resources...),
	}
}

// Registry 是线程安全的 skill 注册表。
// 用 map 提供按名称的 O(1) 查找，同时用 order 切片记录首次注册顺序，
// 保证 List 的输出顺序稳定（先扫描到的目录优先），便于前端展示和
// 启发式匹配的确定性（谁先注册谁先命中）。
type Registry struct {
	mu     sync.RWMutex
	skills map[string]Skill // key 为 normalizeSkillName 后的名称
	order  []string         // 按首次 Register 的顺序保存 key
}

// NewRegistry 创建一个空注册表。
func NewRegistry() *Registry {
	return &Registry{skills: map[string]Skill{}, order: []string{}}
}

// Register 以归一化名称为 key 注册（或覆盖）一个 skill。
// 同名 skill 后注册者覆盖先注册者，但保留原有的顺序位置——
// 这意味着扫描目录时"后扫描的目录"（如用户级目录）可以覆盖
// "先扫描的目录"（如项目级目录）中的同名 skill 内容。
func (r *Registry) Register(skill Skill) {
	key := normalizeSkillName(skill.manifest.Name)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 只有首次注册才追加到 order，覆盖注册不改变排序位置。
	if _, exists := r.skills[key]; !exists {
		r.order = append(r.order, key)
	}
	r.skills[key] = skill
}

// LoadFromDirectories 依次扫描一组 skill 根目录并注册其中发现的所有 skill。
// 目录列表通常来自 claudecode 的启动上下文发现：项目 skills/、项目
// .claude/skills/、用户 ~/.claude/skills/。空路径被跳过；单个目录扫描
// 失败会中止并返回错误。
func (r *Registry) LoadFromDirectories(dirs []Directory) error {
	for _, dir := range dirs {
		if strings.TrimSpace(dir.Path) == "" {
			continue
		}
		if err := r.loadFromDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

// loadFromDirectory 递归遍历一个根目录，把其中每个名为 SKILL.md 的文件
// 解析为 skill 并注册。发现规则：
//   - 目录不存在视为正常情况（返回 nil），因为三个候选目录不一定都存在；
//   - 遍历过程中的单文件错误被静默跳过（return nil），保证一个坏文件
//     不会阻断整个目录的加载；
//   - skill 的默认名称由 SKILL.md 相对根目录的路径推导（见 loadSkillFile），
//     因此支持任意深度的嵌套目录组织（如 skills/a/b/SKILL.md → 名称 a:b）。
func (r *Registry) loadFromDirectory(dir Directory) error {
	info, err := os.Stat(dir.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(dir.Path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		// 只识别精确命名为 SKILL.md 的清单文件，其余文件由
		// loadSupportResources 作为支持资源收集。
		if entry.Name() != "SKILL.md" {
			return nil
		}
		skill, ok := loadSkillFile(dir.Path, path, dir.Scope)
		if ok {
			r.Register(skill)
		}
		return nil
	})
}

// List 按注册顺序返回所有 skill 的元数据快照。
// 注意返回的 Manifest.Enabled 恒为零值 false——启用状态由 ConfigStore.List
// 负责叠加，注册表本身不感知启用/禁用。
func (r *Registry) List() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	manifests := make([]Manifest, 0, len(r.order))
	for _, key := range r.order {
		manifests = append(manifests, r.skills[key].manifest)
	}
	return manifests
}

// Get 按名称查找 skill。名称先经过归一化（去 "/" 前缀、去 "skill:" 前缀、
// 转小写），所以 "/foo"、"skill:foo"、"FOO" 都能命中同一个 skill。
func (r *Registry) Get(name string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[normalizeSkillName(name)]
	return skill, ok
}

// Update 用给定 Detail 覆盖已注册 skill 的"可编辑字段"
// （用途、描述、触发词、正文、示例、资源）。
// 名称、来源、路径、调用开关等身份/来源字段不可通过 Update 修改，
// 保证 skill 的身份与磁盘来源始终可信。所有输入都会被清洗
// （去空白、去重、剔除空条目）后再写入。该方法只改内存，
// 持久化由 ConfigStore.Save 另行完成。
func (r *Registry) Update(name string, detail Detail) (Detail, bool) {
	key := normalizeSkillName(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	skill, ok := r.skills[key]
	if !ok {
		return Detail{}, false
	}
	skill.manifest.Purpose = strings.TrimSpace(detail.Purpose)
	skill.manifest.Description = strings.TrimSpace(detail.Description)
	skill.manifest.Triggers = cleanTriggers(detail.Triggers)
	skill.readme = strings.TrimSpace(detail.Readme)
	skill.instructions = strings.TrimSpace(detail.Instructions)
	skill.examples = cleanExamples(detail.Examples)
	skill.resources = cleanResources(detail.Resources)
	r.skills[key] = skill
	return skill.Detail(), true
}

// Resolve 是渐进加载的"轻量匹配"入口：对一条用户消息判断是否应该立即
// 加载某个 skill 的完整包。匹配分两级，显式优先于隐式：
//  1. 斜杠命令（/name ...）——用户显式选择，返回 explicit=true；
//  2. 触发词启发式——消息包含某个已启用 skill 的触发词，返回 explicit=false。
//
// 返回值依次为：命中的 skill、query（斜杠命令时是去掉命令后的剩余文本，
// 启发式时是原始消息）、是否显式命中、是否命中。
// 之所以在发送给模型之前做纯字符串匹配，是为了让明显相关的 skill 无需
// 额外一轮模型交互就能进入上下文（第三条途径——模型主动 <load_skill>
// 请求——由 agent 层处理，不在本函数内）。
func (r *Registry) Resolve(message string, enabled map[string]bool) (Skill, string, bool, bool) {
	if skill, query, ok := r.resolveCommand(message, enabled); ok {
		return skill, query, true, true
	}
	if skill, query, ok := r.resolveHeuristic(message, enabled); ok {
		return skill, query, false, true
	}
	return Skill{}, "", false, false
}

// resolveCommand 解析 "/name 参数..." 形式的斜杠命令。
// 约束：
//   - 必须以单个 "/" 开头（"//" 被视为普通文本，留作转义途径）；
//   - skill 必须存在、已启用、且 UserInvocable 为 true；
//   - 命令名与 skill 名同样经过归一化，大小写不敏感。
//
// 返回的 query 是命令名之后的剩余文本（即用户附带的参数）。
func (r *Registry) resolveCommand(message string, enabled map[string]bool) (Skill, string, bool) {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return Skill{}, "", false
	}
	// 以第一个空格切分出命令名与其余参数。
	command, rest, _ := strings.Cut(strings.TrimPrefix(trimmed, "/"), " ")
	command = normalizeSkillName(command)
	if command == "" || !enabled[command] {
		return Skill{}, "", false
	}
	skill, ok := r.Get(command)
	if !ok {
		return Skill{}, "", false
	}
	// UserInvocable=false 的 skill 禁止用户显式调用（只能由模型请求）。
	if !skill.manifest.UserInvocable {
		return Skill{}, "", false
	}
	return skill, strings.TrimSpace(rest), true
}

// resolveHeuristic 按注册顺序扫描已启用的 skill，做触发词子串匹配：
//   - 大小写不敏感匹配（lower vs lower）之外还保留一次原文匹配
//     （strings.Contains(message, trigger)），后者对中文等无大小写概念的
//     触发词是等价的，主要为了保留触发词的原样语义；
//   - DisableModelInvocation 的 skill 被排除——启发式匹配属于"隐式/自动"
//     加载，与模型调用同级，只有用户显式 / 命令才能绕过；
//   - 首个命中即返回（先注册的 skill 优先），保证结果确定性。
func (r *Registry) resolveHeuristic(message string, enabled map[string]bool) (Skill, string, bool) {
	lower := strings.ToLower(message)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, key := range r.order {
		if !enabled[key] {
			continue
		}
		skill := r.skills[key]
		if skill.manifest.DisableModelInvocation {
			continue
		}
		for _, trigger := range skill.manifest.Triggers {
			trigger = strings.TrimSpace(trigger)
			if trigger == "" {
				continue
			}
			if strings.Contains(lower, strings.ToLower(trigger)) || strings.Contains(message, trigger) {
				return skill, message, true
			}
		}
	}
	return Skill{}, "", false
}

// ConfigStore 管理 skill 的运行时状态（启用/禁用、软删除）并负责持久化。
// 它与 Registry 分离的原因：Registry 反映"磁盘上发现了什么"（每次启动
// 重新扫描），ConfigStore 反映"用户想怎么用"（跨重启保留）。
// 支持三种后端，按构造函数区分：
//   - 纯内存（NewConfigStore）：不持久化；
//   - JSON 文件（NewPersistentConfigStore）：整份配置含正文/示例/资源；
//   - SQLite（NewSQLiteConfigStore）：只持久化 enabled/deleted 两个开关。
type ConfigStore struct {
	mu      sync.RWMutex
	enabled map[string]bool // key -> 是否启用
	// deleted 是"软删除"标记：skill 文件仍在磁盘上（下次扫描还会注册），
	// 但对用户不可见、不可启用；用软删除而非真删文件，既不破坏磁盘上的
	// skill 来源，又能让删除操作跨重启生效。
	deleted map[string]bool
	path    string  // JSON 持久化文件路径；为空表示不用 JSON 后端
	db      *sql.DB // SQLite 连接；非 nil 时优先于 JSON 后端
}

// NewConfigStore 创建纯内存的配置存储（不持久化），默认启用注册表中的
// 全部 skill。主要用于测试或无持久化需求的场景。
func NewConfigStore(registry *Registry) *ConfigStore {
	return newConfigStore(registry, "")
}

// NewPersistentConfigStore 创建以 JSON 文件为后端的配置存储，
// 构造时立即尝试加载既有配置并叠加到注册表上（加载失败被忽略，
// 退化为默认全启用，保证启动不因配置损坏而失败）。
func NewPersistentConfigStore(registry *Registry, path string) *ConfigStore {
	store := newConfigStore(registry, path)
	_ = store.Load(registry)
	return store
}

// NewSQLiteConfigStore 创建以 SQLite（skill_configs 表）为后端的配置存储，
// 同样在构造时立即加载既有的启用/删除状态。
func NewSQLiteConfigStore(registry *Registry, db *sql.DB) *ConfigStore {
	store := newConfigStore(registry, "")
	store.db = db
	_ = store.Load(registry)
	return store
}

// newConfigStore 初始化默认状态：注册表中发现的所有 skill 默认启用、
// 均未删除。持久化配置随后由 Load 覆盖这些默认值。
func newConfigStore(registry *Registry, path string) *ConfigStore {
	enabled := map[string]bool{}
	for _, manifest := range registry.List() {
		enabled[normalizeSkillName(manifest.Name)] = true
	}
	return &ConfigStore{enabled: enabled, deleted: map[string]bool{}, path: strings.TrimSpace(path)}
}

// EnabledMap 返回启用状态映射的副本（key -> 是否启用）。
// 返回副本而非内部 map，是为了让调用方（如 Registry.Resolve）在无锁
// 状态下安全读取，避免并发读写冲突。
func (s *ConfigStore) EnabledMap() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	enabled := map[string]bool{}
	for key, value := range s.enabled {
		enabled[key] = value
	}
	return enabled
}

// List 返回"对用户可见"的 skill 清单：跳过已软删除的条目，
// 并把 ConfigStore 的启用状态叠加到注册表元数据的 Enabled 字段上。
// 这是 HTTP API 与 agent 构建 system prompt 时的标准入口。
func (s *ConfigStore) List(registry *Registry) []Manifest {
	enabled := s.EnabledMap()
	deleted := s.DeletedMap()
	manifests := registry.List()
	out := make([]Manifest, 0, len(manifests))
	for _, manifest := range manifests {
		key := normalizeSkillName(manifest.Name)
		if deleted[key] {
			continue
		}
		manifest.Enabled = enabled[key]
		out = append(out, manifest)
	}
	return out
}

// SetEnabled 以"全量覆盖"语义设置启用集合：只有出现在 enabledNames 中、
// 且确实存在于注册表、且未被软删除的 skill 会被启用，其余全部禁用。
// 适合前端多选框一次性提交的场景。返回更新后的可见清单。
func (s *ConfigStore) SetEnabled(registry *Registry, enabledNames []string) []Manifest {
	next := map[string]bool{}
	deleted := s.DeletedMap()
	for _, name := range enabledNames {
		if _, ok := registry.Get(name); ok {
			key := normalizeSkillName(name)
			// 已删除的 skill 即使被点名也不允许复活为启用状态。
			if !deleted[key] {
				next[key] = true
			}
		}
	}
	s.mu.Lock()
	s.enabled = next
	s.mu.Unlock()
	return s.List(registry)
}

// SetOne 单独切换某个 skill 的启用状态。skill 不存在或已被软删除时
// 返回 (nil, false)，成功时返回更新后的可见清单。
func (s *ConfigStore) SetOne(registry *Registry, name string, enabled bool) ([]Manifest, bool) {
	key := normalizeSkillName(name)
	if _, ok := registry.Get(key); !ok {
		return nil, false
	}
	s.mu.Lock()
	// 软删除状态下拒绝任何启用/禁用操作，必须先"恢复"（当前未提供恢复
	// 接口，删除即视为永久隐藏）。
	if s.deleted[key] {
		s.mu.Unlock()
		return nil, false
	}
	s.enabled[key] = enabled
	s.mu.Unlock()
	return s.List(registry), true
}

// DeleteOne 对某个 skill 做软删除：同时置 enabled=false、deleted=true。
// 不删除磁盘文件——注册表下次启动仍会扫描到它，但 Load 时会恢复 deleted
// 标记使其继续隐藏。返回更新后的可见清单（其中已不包含该 skill）。
func (s *ConfigStore) DeleteOne(registry *Registry, name string) ([]Manifest, bool) {
	key := normalizeSkillName(name)
	if _, ok := registry.Get(key); !ok {
		return nil, false
	}
	s.mu.Lock()
	if s.deleted == nil {
		s.deleted = map[string]bool{}
	}
	s.enabled[key] = false
	s.deleted[key] = true
	s.mu.Unlock()
	return s.List(registry), true
}

// DeletedMap 返回软删除状态映射的副本，理由同 EnabledMap。
func (s *ConfigStore) DeletedMap() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	deleted := map[string]bool{}
	for key, value := range s.deleted {
		deleted[key] = value
	}
	return deleted
}

// persistedSkill 是 JSON 持久化文件中单个 skill 的序列化结构。
// 除 ID/Name 外全部使用指针类型，以便区分"字段缺失（不覆盖磁盘扫描值）"
// 和"字段为空值（显式覆盖为空）"两种情况——这是 mergePersistedSkill
// 实现"部分覆盖合并"的关键。
type persistedSkill struct {
	ID           string      `json:"id,omitempty"`
	Name         string      `json:"name"`
	Purpose      *string     `json:"purpose,omitempty"`
	Description  *string     `json:"description,omitempty"`
	Triggers     *[]string   `json:"triggers,omitempty"`
	Enabled      *bool       `json:"enabled,omitempty"`
	Deleted      *bool       `json:"deleted,omitempty"`
	Readme       *string     `json:"readme,omitempty"`
	Instructions *string     `json:"instructions,omitempty"`
	Examples     *[]Example  `json:"examples,omitempty"`
	Resources    *[]Resource `json:"resources,omitempty"`
}

// persistedConfig 是 JSON 持久化文件的顶层结构：一份 skill 配置数组。
type persistedConfig struct {
	Skills []persistedSkill `json:"skills"`
}

// loadSkillFile 把一个 SKILL.md 文件解析为 Skill。
// 解析流程：
//  1. 用 splitFrontmatter 把内容切成 YAML frontmatter（meta）与 Markdown 正文；
//  2. 名称优先取 frontmatter 的 name，缺省时由 SKILL.md 所在目录相对根目录
//     的路径推导，路径分隔符替换为 ":"（如 skills/lark/doc/SKILL.md →
//     "lark:doc"），保证嵌套目录下的 skill 名称仍然全局唯一且可读；
//  3. description 与 when_to_use 互为回退：任一缺失时用另一个补齐，
//     保证 system prompt 中的用途/适用场景两栏都有内容；
//  4. frontmatter 的所有键都同时接受连字符与下划线两种写法
//     （如 disable-model-invocation / disable_model_invocation），
//     以兼容不同来源的 SKILL.md 书写习惯；
//  5. UserInvocable 默认 true（未写即允许 / 调用），DisableModelInvocation
//     默认 false（未写即允许自动/模型加载）；
//  6. 正文同时充当 readme 与 instructions 的初始值；同目录下的其他文件
//     由 loadSupportResources 收集为支持资源。
//
// 文件读取失败或最终名称为空时返回 ok=false，调用方跳过该文件。
func loadSkillFile(root string, skillPath string, scope string) (Skill, bool) {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return Skill{}, false
	}
	meta, body := splitFrontmatter(string(content))
	dir := filepath.Dir(skillPath)
	// 相对路径推导默认名称；SKILL.md 直接位于根目录（relDir == "."）时
	// 退而使用目录基名。
	relDir, err := filepath.Rel(root, dir)
	if err != nil || relDir == "." {
		relDir = filepath.Base(dir)
	}
	defaultName := strings.ReplaceAll(relDir, string(os.PathSeparator), ":")
	name := firstNonEmpty(meta["name"], defaultName)
	name = normalizeSkillName(name)
	if name == "" {
		return Skill{}, false
	}
	description := firstNonEmpty(meta["description"], meta["when_to_use"], meta["when-to-use"])
	whenToUse := firstNonEmpty(meta["when_to_use"], meta["when-to-use"], meta["description"])
	manifest := Manifest{
		ID:                     "skill:" + name,
		Name:                   name,
		Purpose:                description,
		Description:            description,
		WhenToUse:              whenToUse,
		Triggers:               cleanTriggers(parseStringList(firstNonEmpty(meta["triggers"], meta["trigger"]))),
		Source:                 scope,
		Path:                   skillPath,
		DisableModelInvocation: parseBool(firstNonEmpty(meta["disable-model-invocation"], meta["disable_model_invocation"])),
		UserInvocable:          parseBoolDefault(firstNonEmpty(meta["user-invocable"], meta["user_invocable"]), true),
		AllowedTools:           parseStringList(firstNonEmpty(meta["allowed-tools"], meta["allowed_tools"])),
		DisallowedTools:        parseStringList(firstNonEmpty(meta["disallowed-tools"], meta["disallowed_tools"])),
		Model:                  firstNonEmpty(meta["model"]),
		Effort:                 firstNonEmpty(meta["effort"]),
		Context:                firstNonEmpty(meta["context"]),
		Agent:                  firstNonEmpty(meta["agent"]),
		Shell:                  firstNonEmpty(meta["shell"]),
	}
	// 完全没有描述信息时兜底一句来源说明，避免 system prompt 出现空条目。
	if manifest.Purpose == "" {
		manifest.Purpose = "Prompt package loaded from " + skillPath
	}
	return Skill{
		manifest:     manifest,
		readme:       strings.TrimSpace(body),
		instructions: strings.TrimSpace(body),
		examples:     []Example{},
		resources:    loadSupportResources(dir),
	}, true
}

// loadSupportResources 递归收集 skill 目录下除 SKILL.md 之外的支持文件
// （参考文档、脚本、模板等），作为渐进加载时可随包注入的资源。
// 规则：
//   - 子目录中如果自带 SKILL.md，说明那是另一个独立 skill，整个子树跳过
//     （SkipDir），避免父 skill 把子 skill 的文件误收为自己的资源；
//   - SKILL.md 本身与隐藏文件（.开头）不收集；
//   - 资源名用相对 skill 目录的路径，保持目录层级信息；
//   - 只有 <=64KB 且看起来是文本（不含 NUL 字节）的文件才内联 Content，
//     二进制或大文件仅保留 URI，控制注入模型上下文的体积；
//   - 最后按名称排序，保证输出顺序稳定（WalkDir 顺序本身与平台相关性小，
//     但排序让序列化结果完全确定）。
func loadSupportResources(skillDir string) []Resource {
	var resources []Resource
	_ = filepath.WalkDir(skillDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			// 嵌套的独立 skill 目录：跳过整棵子树。
			if path != skillDir && fileExists(filepath.Join(path, "SKILL.md")) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "SKILL.md" || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			rel = entry.Name()
		}
		resource := Resource{
			Name: rel,
			Type: resourceType(path),
			URI:  path,
		}
		// 小文本文件直接内联内容，方便一次性注入模型上下文。
		if info, err := entry.Info(); err == nil && info.Size() <= 64*1024 {
			if data, err := os.ReadFile(path); err == nil && isLikelyText(data) {
				resource.Content = strings.TrimSpace(string(data))
			}
		}
		resources = append(resources, resource)
		return nil
	})
	sort.Slice(resources, func(i, j int) bool { return resources[i].Name < resources[j].Name })
	return resources
}

// splitFrontmatter 把 Markdown 文件切分为 YAML frontmatter 键值对与正文。
// 这是一个刻意保持简单的"迷你 YAML"解析器，不引入完整 YAML 依赖，
// 只支持 SKILL.md 实际需要的子集：
//   - 文件必须以 "---\n" 开头、以下一个 "\n---" 结束，否则整个文件视为正文；
//   - 统一 CRLF 为 LF，兼容 Windows 编辑的文件；
//   - 支持 "key: value" 标量（键统一转小写；值去除包裹引号）；
//   - 支持 "- item" 形式的列表项：追加到最近一个 key 的值上，用换行分隔，
//     后续由 parseStringList 按换行/逗号再拆分；
//   - 空行与 "#" 注释行跳过；不含 ":" 的行忽略；
//   - 不支持嵌套映射、多行字符串（| / >）等高级语法。
//
// 返回的 meta 键全部为小写，body 为去除首尾空白的正文。
func splitFrontmatter(content string) (map[string]string, string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return map[string]string{}, strings.TrimSpace(content)
	}
	rest := strings.TrimPrefix(content, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		// 只有起始分隔符没有结束分隔符：按无 frontmatter 处理，防止把
		// 正文误吞进元数据。
		return map[string]string{}, strings.TrimSpace(content)
	}
	raw := rest[:end]
	body := strings.TrimPrefix(rest[end:], "\n---")
	body = strings.TrimPrefix(body, "\n")
	meta := map[string]string{}
	// currentKey 记录最近解析到的键，供后续 "- item" 列表行归属。
	currentKey := ""
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") && currentKey != "" {
			item := trimYAMLScalar(strings.TrimPrefix(trimmed, "- "))
			if item == "" {
				continue
			}
			// 列表项以换行拼接到同一个值里，parseStringList 会再拆开。
			if meta[currentKey] == "" {
				meta[currentKey] = item
			} else {
				meta[currentKey] += "\n" + item
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

// parseStringList 把 frontmatter 中的字符串值解析为列表。
// 同时兼容三种写法：
//   - YAML 流式数组："[a, b, c]"（剥掉方括号后按逗号拆）；
//   - 逗号分隔的裸标量："a, b, c"；
//   - YAML 块列表（splitFrontmatter 已拼成换行分隔的多行值）。
//
// 每个元素再做一次去引号/去空白清理，空元素被丢弃。
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

// trimYAMLScalar 清理 YAML 标量：去首尾空白，再剥掉包裹的单/双引号，
// 最后再去一次空白（处理引号内侧的空格）。
func trimYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	return strings.TrimSpace(value)
}

// parseBool 宽松解析布尔值：true/yes/1/on（大小写不敏感）为 true，
// 其余一律 false。
func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1", "on":
		return true
	default:
		return false
	}
}

// parseBoolDefault 与 parseBool 相同，但值为空（frontmatter 未写该键）时
// 返回 fallback。用于 user-invocable 这类"默认开启"的开关。
func parseBoolDefault(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return parseBool(value)
}

// firstNonEmpty 返回第一个去空白后非空的值（已去空白），全空则返回 ""。
// 用于实现 frontmatter 键的多别名回退（如 description → when_to_use）。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// resourceType 由文件扩展名推断资源类型（小写、不含点），
// 无扩展名时归类为 "document"。
func resourceType(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext == "" {
		return "document"
	}
	return ext
}

// isLikelyText 用"是否包含 NUL 字节"这一廉价启发式判断内容是否为文本，
// 决定支持资源是否内联到 Resource.Content。
func isLikelyText(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

// fileExists 判断路径存在且是普通文件（目录返回 false）。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Load 从持久化后端读取配置并叠加到注册表与自身状态上。
// 分派逻辑：优先 SQLite（db 非 nil），其次 JSON 文件（path 非空），
// 都没有则视为纯内存模式直接返回。
//
// JSON 模式的合并语义（磁盘扫描结果为底、持久化配置为覆盖层）：
//   - 持久化里标记 deleted 的 skill：只要注册表里还存在，就恢复
//     "禁用 + 软删除"状态（文件还在磁盘上，但对用户隐藏）；
//   - 其余条目：用 mergePersistedSkill 做字段级部分覆盖（只有持久化中
//     出现的字段才覆盖磁盘值），再写回注册表；
//   - 持久化里不存在的 skill 保持默认（启用、未删除）；
//   - 持久化里有但注册表里已不存在的 skill（文件被移走）被静默跳过。
//
// 文件不存在视为首次运行，不算错误。
func (s *ConfigStore) Load(registry *Registry) error {
	if s.db != nil {
		return s.loadSQLite(registry)
	}
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var config persistedConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	// 以当前（默认全启用）状态为基础做叠加，最后整体换入，
	// 避免逐条修改期间出现中间状态。
	enabled := s.EnabledMap()
	deleted := s.DeletedMap()
	for _, persisted := range config.Skills {
		key := normalizeSkillName(persisted.Name)
		if key == "" {
			continue
		}
		if persisted.Deleted != nil && *persisted.Deleted {
			// 已删除条目不做内容合并，仅恢复隐藏状态。
			if _, ok := registry.Get(key); ok {
				enabled[key] = false
				deleted[key] = true
			}
			continue
		}
		skill, ok := registry.Get(key)
		if !ok {
			continue
		}
		// 把用户编辑过的内容（用途/正文/示例/资源等）覆盖回注册表。
		detail := mergePersistedSkill(skill.Detail(), persisted)
		if _, ok := registry.Update(key, detail); !ok {
			continue
		}
		if persisted.Deleted != nil {
			deleted[key] = *persisted.Deleted
		}
		if persisted.Enabled != nil {
			enabled[key] = *persisted.Enabled
		}
	}
	s.mu.Lock()
	s.enabled = enabled
	s.deleted = deleted
	s.mu.Unlock()
	return nil
}

// Save 把当前注册表内容 + 启用/删除状态写入持久化后端。
// 与 Load 对应：优先 SQLite，其次 JSON，纯内存模式为空操作。
//
// JSON 模式下：
//   - 已删除的 skill 只写一条极简记录（name + enabled=false + deleted=true），
//     不保存正文，因为内容以磁盘 SKILL.md 为准；
//   - 正常 skill 保存完整 Detail（含用户编辑过的正文/示例/资源）；
//   - 采用"写临时文件 + rename"的原子写入方式，避免进程中途退出留下
//     半截 JSON 导致下次 Load 失败。
func (s *ConfigStore) Save(registry *Registry) error {
	if s.db != nil {
		return s.saveSQLite(registry)
	}
	if s.path == "" {
		return nil
	}
	enabled := s.EnabledMap()
	deleted := s.DeletedMap()
	config := persistedConfig{Skills: []persistedSkill{}}
	for _, manifest := range registry.List() {
		key := normalizeSkillName(manifest.Name)
		if deleted[key] {
			config.Skills = append(config.Skills, persistedSkill{
				Name:    key,
				Enabled: boolPtr(false),
				Deleted: boolPtr(true),
			})
			continue
		}
		skill, ok := registry.Get(manifest.Name)
		if !ok {
			continue
		}
		detail := skill.Detail()
		detail.Enabled = enabled[normalizeSkillName(detail.Name)]
		config.Skills = append(config.Skills, persistedFromDetail(detail))
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	// 原子写：先写 .tmp 再 rename，同目录 rename 在 POSIX 上是原子操作。
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// loadSQLite 从 skill_configs 表读取 enabled/deleted 两个开关状态。
// 注意 SQLite 后端只持久化开关，不持久化用户编辑的 skill 正文——
// 内容始终以磁盘 SKILL.md 扫描结果为准（与 JSON 后端不同）。
// 表中存在但注册表中已不存在的 skill 被忽略。
func (s *ConfigStore) loadSQLite(registry *Registry) error {
	rows, err := s.db.Query(`SELECT skill_name, enabled, deleted FROM skill_configs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	enabled := s.EnabledMap()
	deleted := s.DeletedMap()
	for rows.Next() {
		var name string
		var isEnabled int
		var isDeleted int
		if err := rows.Scan(&name, &isEnabled, &isDeleted); err != nil {
			return err
		}
		// 只叠加注册表里仍然存在的 skill，避免残留脏数据影响状态。
		if _, ok := registry.Get(name); ok {
			key := normalizeSkillName(name)
			enabled[key] = isEnabled != 0
			deleted[key] = isDeleted != 0
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.enabled = enabled
	s.deleted = deleted
	s.mu.Unlock()
	return nil
}

// saveSQLite 把每个已注册 skill 的开关状态 upsert 到 skill_configs 表。
// 整体包在一个事务里：任一条失败则回滚（defer tx.Rollback 在 Commit
// 成功后是空操作），保证配置不会写成一半。
func (s *ConfigStore) saveSQLite(registry *Registry) error {
	enabled := s.EnabledMap()
	deleted := s.DeletedMap()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, manifest := range registry.List() {
		key := normalizeSkillName(manifest.Name)
		_, err := tx.Exec(
			`INSERT INTO skill_configs(skill_name, enabled, deleted, updated_at)
			 VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
			 ON CONFLICT(skill_name) DO UPDATE SET
				enabled = excluded.enabled,
				deleted = excluded.deleted,
				updated_at = excluded.updated_at`,
			key,
			boolInt(enabled[key]),
			boolInt(deleted[key]),
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// mergePersistedSkill 把持久化条目合并到磁盘扫描出的 Detail 之上。
// 采用"指针即存在"的部分覆盖语义：只有持久化 JSON 中出现的字段
// （指针非 nil）才覆盖磁盘值，未出现的字段保持 SKILL.md 的原始内容。
// 这使得用户只编辑过 description 时，正文更新仍能跟随磁盘文件演进。
func mergePersistedSkill(base Detail, persisted persistedSkill) Detail {
	if strings.TrimSpace(persisted.ID) != "" {
		base.ID = strings.TrimSpace(persisted.ID)
	}
	if strings.TrimSpace(persisted.Name) != "" {
		base.Name = strings.TrimSpace(persisted.Name)
	}
	if persisted.Purpose != nil {
		base.Purpose = strings.TrimSpace(*persisted.Purpose)
	}
	if persisted.Description != nil {
		base.Description = strings.TrimSpace(*persisted.Description)
	}
	if persisted.Triggers != nil {
		base.Triggers = cleanTriggers(*persisted.Triggers)
	}
	if persisted.Enabled != nil {
		base.Enabled = *persisted.Enabled
	}
	if persisted.Readme != nil {
		base.Readme = strings.TrimSpace(*persisted.Readme)
	}
	if persisted.Instructions != nil {
		base.Instructions = strings.TrimSpace(*persisted.Instructions)
	}
	if persisted.Examples != nil {
		base.Examples = cleanExamples(*persisted.Examples)
	}
	if persisted.Resources != nil {
		base.Resources = cleanResources(*persisted.Resources)
	}
	return base
}

// persistedFromDetail 把完整 Detail 转换为持久化条目：所有可选字段都取
// 指针（显式写入），保证 Save 落盘的是完整快照而非增量。
func persistedFromDetail(detail Detail) persistedSkill {
	return persistedSkill{
		ID:           detail.ID,
		Name:         detail.Name,
		Purpose:      stringPtr(detail.Purpose),
		Description:  stringPtr(detail.Description),
		Triggers:     stringSlicePtr(detail.Triggers),
		Enabled:      boolPtr(detail.Enabled),
		Readme:       stringPtr(detail.Readme),
		Instructions: stringPtr(detail.Instructions),
		Examples:     examplesPtr(detail.Examples),
		Resources:    resourcesPtr(detail.Resources),
	}
}

// stringPtr / boolPtr / stringSlicePtr / examplesPtr / resourcesPtr
// 是构造持久化结构所需的取址辅助函数；切片版本先做防御性拷贝，
// 防止持久化快照与注册表内部切片共享底层数组。

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func stringSlicePtr(value []string) *[]string {
	copied := append([]string(nil), value...)
	return &copied
}

func examplesPtr(value []Example) *[]Example {
	copied := append([]Example(nil), value...)
	return &copied
}

func resourcesPtr(value []Resource) *[]Resource {
	copied := append([]Resource(nil), value...)
	return &copied
}

// normalizeSkillName 把各种形态的 skill 引用统一为注册表 key：
// 去掉前导 "/"（斜杠命令形态）、去掉 "skill:" 前缀（ID 形态）、转小写。
// 全项目所有按名称查找/比较的入口都必须先经过这里，保证一致性。
func normalizeSkillName(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/"))
	value = strings.TrimPrefix(value, "skill:")
	return strings.ToLower(value)
}

// cleanTriggers 清洗触发词列表：去首尾空白、丢弃空项、按小写去重
// （保留首次出现时的原始大小写，便于展示）。
func cleanTriggers(values []string) []string {
	triggers := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		triggers = append(triggers, value)
	}
	return triggers
}

// cleanExamples 清洗示例列表：各字段去空白，三个字段全空的条目被丢弃。
func cleanExamples(values []Example) []Example {
	examples := []Example{}
	for _, value := range values {
		example := Example{
			Name:      strings.TrimSpace(value.Name),
			User:      strings.TrimSpace(value.User),
			Assistant: strings.TrimSpace(value.Assistant),
		}
		if example.Name == "" && example.User == "" && example.Assistant == "" {
			continue
		}
		examples = append(examples, example)
	}
	return examples
}

// cleanResources 清洗资源列表：各字段去空白，名称/内容/URI 全空的条目
// 被丢弃，类型缺省补为 "document"。
func cleanResources(values []Resource) []Resource {
	resources := []Resource{}
	for _, value := range values {
		resource := Resource{
			Name:    strings.TrimSpace(value.Name),
			Type:    strings.TrimSpace(value.Type),
			Content: strings.TrimSpace(value.Content),
			URI:     strings.TrimSpace(value.URI),
		}
		if resource.Name == "" && resource.Content == "" && resource.URI == "" {
			continue
		}
		if resource.Type == "" {
			resource.Type = "document"
		}
		resources = append(resources, resource)
	}
	return resources
}

// boolInt 把布尔值转成 SQLite 存储用的 0/1 整数。
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
