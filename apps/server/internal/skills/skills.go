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

type Manifest struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Purpose                string   `json:"purpose"`
	Description            string   `json:"description"`
	WhenToUse              string   `json:"whenToUse,omitempty"`
	Triggers               []string `json:"triggers"`
	Source                 string   `json:"source,omitempty"`
	Path                   string   `json:"path,omitempty"`
	DisableModelInvocation bool     `json:"disableModelInvocation,omitempty"`
	UserInvocable          bool     `json:"userInvocable"`
	AllowedTools           []string `json:"allowedTools,omitempty"`
	DisallowedTools        []string `json:"disallowedTools,omitempty"`
	Model                  string   `json:"model,omitempty"`
	Effort                 string   `json:"effort,omitempty"`
	Context                string   `json:"context,omitempty"`
	Agent                  string   `json:"agent,omitempty"`
	Shell                  string   `json:"shell,omitempty"`
	Enabled                bool     `json:"enabled"`
}

type Detail struct {
	Manifest
	Readme       string     `json:"readme"`
	Instructions string     `json:"instructions"`
	Examples     []Example  `json:"examples"`
	Resources    []Resource `json:"resources"`
}

type Example struct {
	Name      string `json:"name,omitempty"`
	User      string `json:"user"`
	Assistant string `json:"assistant"`
}

type Resource struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	URI     string `json:"uri,omitempty"`
}

type Skill struct {
	manifest     Manifest
	readme       string
	instructions string
	examples     []Example
	resources    []Resource
}

type Directory struct {
	Path  string
	Scope string
}

func (s Skill) Manifest() Manifest {
	return s.manifest
}

func (s Skill) Detail() Detail {
	return Detail{
		Manifest:     s.manifest,
		Readme:       s.readme,
		Instructions: s.instructions,
		Examples:     append([]Example(nil), s.examples...),
		Resources:    append([]Resource(nil), s.resources...),
	}
}

type Registry struct {
	mu     sync.RWMutex
	skills map[string]Skill
	order  []string
}

func NewRegistry() *Registry {
	return &Registry{skills: map[string]Skill{}, order: []string{}}
}

func (r *Registry) Register(skill Skill) {
	key := normalizeSkillName(skill.manifest.Name)
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[key]; !exists {
		r.order = append(r.order, key)
	}
	r.skills[key] = skill
}

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

func (r *Registry) List() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	manifests := make([]Manifest, 0, len(r.order))
	for _, key := range r.order {
		manifests = append(manifests, r.skills[key].manifest)
	}
	return manifests
}

func (r *Registry) Get(name string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[normalizeSkillName(name)]
	return skill, ok
}

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

func (r *Registry) Resolve(message string, enabled map[string]bool) (Skill, string, bool, bool) {
	if skill, query, ok := r.resolveCommand(message, enabled); ok {
		return skill, query, true, true
	}
	if skill, query, ok := r.resolveHeuristic(message, enabled); ok {
		return skill, query, false, true
	}
	return Skill{}, "", false, false
}

func (r *Registry) resolveCommand(message string, enabled map[string]bool) (Skill, string, bool) {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return Skill{}, "", false
	}
	command, rest, _ := strings.Cut(strings.TrimPrefix(trimmed, "/"), " ")
	command = normalizeSkillName(command)
	if command == "" || !enabled[command] {
		return Skill{}, "", false
	}
	skill, ok := r.Get(command)
	if !ok {
		return Skill{}, "", false
	}
	if !skill.manifest.UserInvocable {
		return Skill{}, "", false
	}
	return skill, strings.TrimSpace(rest), true
}

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

type ConfigStore struct {
	mu      sync.RWMutex
	enabled map[string]bool
	deleted map[string]bool
	path    string
	db      *sql.DB
}

func NewConfigStore(registry *Registry) *ConfigStore {
	return newConfigStore(registry, "")
}

func NewPersistentConfigStore(registry *Registry, path string) *ConfigStore {
	store := newConfigStore(registry, path)
	_ = store.Load(registry)
	return store
}

func NewSQLiteConfigStore(registry *Registry, db *sql.DB) *ConfigStore {
	store := newConfigStore(registry, "")
	store.db = db
	_ = store.Load(registry)
	return store
}

func newConfigStore(registry *Registry, path string) *ConfigStore {
	enabled := map[string]bool{}
	for _, manifest := range registry.List() {
		enabled[normalizeSkillName(manifest.Name)] = true
	}
	return &ConfigStore{enabled: enabled, deleted: map[string]bool{}, path: strings.TrimSpace(path)}
}

func (s *ConfigStore) EnabledMap() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	enabled := map[string]bool{}
	for key, value := range s.enabled {
		enabled[key] = value
	}
	return enabled
}

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

func (s *ConfigStore) SetEnabled(registry *Registry, enabledNames []string) []Manifest {
	next := map[string]bool{}
	deleted := s.DeletedMap()
	for _, name := range enabledNames {
		if _, ok := registry.Get(name); ok {
			key := normalizeSkillName(name)
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

func (s *ConfigStore) SetOne(registry *Registry, name string, enabled bool) ([]Manifest, bool) {
	key := normalizeSkillName(name)
	if _, ok := registry.Get(key); !ok {
		return nil, false
	}
	s.mu.Lock()
	if s.deleted[key] {
		s.mu.Unlock()
		return nil, false
	}
	s.enabled[key] = enabled
	s.mu.Unlock()
	return s.List(registry), true
}

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

func (s *ConfigStore) DeletedMap() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	deleted := map[string]bool{}
	for key, value := range s.deleted {
		deleted[key] = value
	}
	return deleted
}

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

type persistedConfig struct {
	Skills []persistedSkill `json:"skills"`
}

func loadSkillFile(root string, skillPath string, scope string) (Skill, bool) {
	content, err := os.ReadFile(skillPath)
	if err != nil {
		return Skill{}, false
	}
	meta, body := splitFrontmatter(string(content))
	dir := filepath.Dir(skillPath)
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

func loadSupportResources(skillDir string) []Resource {
	var resources []Resource
	_ = filepath.WalkDir(skillDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
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

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1", "on":
		return true
	default:
		return false
	}
}

func parseBoolDefault(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return parseBool(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func resourceType(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext == "" {
		return "document"
	}
	return ext
}

func isLikelyText(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

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
	enabled := s.EnabledMap()
	deleted := s.DeletedMap()
	for _, persisted := range config.Skills {
		key := normalizeSkillName(persisted.Name)
		if key == "" {
			continue
		}
		if persisted.Deleted != nil && *persisted.Deleted {
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
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

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

func normalizeSkillName(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "/"))
	value = strings.TrimPrefix(value, "skill:")
	return strings.ToLower(value)
}

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

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
