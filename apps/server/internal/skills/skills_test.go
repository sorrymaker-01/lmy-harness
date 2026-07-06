package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewRegistryStartsEmpty(t *testing.T) {
	registry := NewRegistry()
	if got := registry.List(); len(got) != 0 {
		t.Fatalf("default registry should not include bundled skills, got %#v", got)
	}
}

func TestConfigStoreSetEnabled(t *testing.T) {
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	store := NewConfigStore(registry)

	skills := store.SetEnabled(registry, []string{"custom-review"})
	states := map[string]bool{}
	for _, skill := range skills {
		states[skill.Name] = skill.Enabled
	}
	if !states["custom-review"] {
		t.Fatalf("custom-review should be enabled, got %#v", states)
	}
}

func TestRegistryResolveSlashSkill(t *testing.T) {
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	enabled := map[string]bool{"custom-review": true}

	skill, query, explicit, ok := registry.Resolve("/custom-review inspect changes", enabled)
	if !ok {
		t.Fatal("expected slash skill to resolve")
	}
	if !explicit {
		t.Fatal("slash skill should be explicit")
	}
	if skill.Manifest().Name != "custom-review" {
		t.Fatalf("unexpected skill: %s", skill.Manifest().Name)
	}
	if query != "inspect changes" {
		t.Fatalf("unexpected query: %q", query)
	}
}

func TestRegistryResolveIgnoresDisabledSkill(t *testing.T) {
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	_, _, _, ok := registry.Resolve("/custom-review inspect changes", map[string]bool{"custom-review": false})
	if ok {
		t.Fatal("disabled skill should not resolve")
	}
}

func TestSkillDetailContainsPromptPackage(t *testing.T) {
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	skill, ok := registry.Get("custom-review")
	if !ok {
		t.Fatal("custom-review missing")
	}
	detail := skill.Detail()
	if detail.Readme == "" {
		t.Fatal("filesystem skill should include SKILL.md body")
	}
	if detail.Instructions == "" {
		t.Fatal("filesystem skill should expose instructions from SKILL.md body")
	}
	if len(detail.Resources) != 1 || detail.Resources[0].Name != "template.md" {
		t.Fatalf("unexpected resources: %#v", detail.Resources)
	}
}

func TestRegistryUpdateSkillDetail(t *testing.T) {
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	current, ok := registry.Get("custom-review")
	if !ok {
		t.Fatal("custom-review missing")
	}
	next := current.Detail()
	next.Purpose = "Updated review purpose"
	next.Description = "Updated review"
	next.Triggers = []string{"review", "代码审查"}
	next.Readme = "Updated README"
	next.Instructions = "Updated instructions"
	next.Examples = []Example{{Name: "demo", User: "review this", Assistant: "findings"}}
	next.Resources = []Resource{{Name: "rubric", Type: "document", Content: "content"}}
	detail, ok := registry.Update("custom-review", next)
	if !ok {
		t.Fatal("expected update to succeed")
	}
	if detail.Purpose != "Updated review purpose" {
		t.Fatalf("unexpected purpose: %q", detail.Purpose)
	}
	if detail.Description != "Updated review" {
		t.Fatalf("unexpected description: %q", detail.Description)
	}
	if len(detail.Triggers) != 2 || detail.Triggers[0] != "review" || detail.Triggers[1] != "代码审查" {
		t.Fatalf("unexpected triggers: %#v", detail.Triggers)
	}
	if detail.Readme != "Updated README" {
		t.Fatalf("unexpected readme: %q", detail.Readme)
	}
	if detail.Instructions != "Updated instructions" {
		t.Fatalf("unexpected instructions: %q", detail.Instructions)
	}
	if len(detail.Examples) != 1 || detail.Examples[0].User != "review this" {
		t.Fatalf("unexpected examples: %#v", detail.Examples)
	}
	if len(detail.Resources) != 1 || detail.Resources[0].Name != "rubric" {
		t.Fatalf("unexpected resources: %#v", detail.Resources)
	}
}

func TestPersistentConfigStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.json")
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	store := NewPersistentConfigStore(registry, path)
	current, ok := registry.Get("custom-review")
	if !ok {
		t.Fatal("custom-review missing")
	}
	next := current.Detail()
	next.Purpose = "Saved purpose"
	next.Description = "Saved review"
	next.Triggers = []string{"saved"}
	next.Readme = "Saved README"
	next.Instructions = "Saved instructions"
	next.Examples = []Example{{Name: "saved", User: "review", Assistant: "done"}}
	next.Resources = []Resource{{Name: "saved resource", Type: "document", Content: "content"}}
	if _, ok := registry.Update("custom-review", next); !ok {
		t.Fatal("expected update to succeed")
	}
	if _, ok := store.SetOne(registry, "custom-review", false); !ok {
		t.Fatal("expected config update to succeed")
	}
	if err := store.Save(registry); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	nextRegistry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	nextStore := NewPersistentConfigStore(nextRegistry, path)
	skill, ok := nextRegistry.Get("custom-review")
	if !ok {
		t.Fatal("custom-review missing after load")
	}
	detail := skill.Detail()
	if detail.Purpose != "Saved purpose" {
		t.Fatalf("unexpected purpose after load: %q", detail.Purpose)
	}
	if detail.Description != "Saved review" {
		t.Fatalf("unexpected description after load: %q", detail.Description)
	}
	if len(detail.Triggers) != 1 || detail.Triggers[0] != "saved" {
		t.Fatalf("unexpected triggers after load: %#v", detail.Triggers)
	}
	if detail.Readme != "Saved README" {
		t.Fatalf("unexpected readme after load: %q", detail.Readme)
	}
	if detail.Instructions != "Saved instructions" {
		t.Fatalf("unexpected instructions after load: %q", detail.Instructions)
	}
	if len(detail.Examples) != 1 || detail.Examples[0].Assistant != "done" {
		t.Fatalf("unexpected examples after load: %#v", detail.Examples)
	}
	if len(detail.Resources) != 1 || detail.Resources[0].Name != "saved resource" {
		t.Fatalf("unexpected resources after load: %#v", detail.Resources)
	}
	if nextStore.EnabledMap()["custom-review"] {
		t.Fatal("custom-review should remain disabled after load")
	}
}

func TestPersistentConfigStoreDeleteOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.json")
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	store := NewPersistentConfigStore(registry, path)
	updated, ok := store.DeleteOne(registry, "custom-review")
	if !ok {
		t.Fatal("expected delete to succeed")
	}
	if len(updated) != 0 {
		t.Fatalf("deleted skill should not be listed, got %#v", updated)
	}
	if err := store.Save(registry); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	nextRegistry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	nextStore := NewPersistentConfigStore(nextRegistry, path)
	if listed := nextStore.List(nextRegistry); len(listed) != 0 {
		t.Fatalf("deleted skill should stay hidden after reload, got %#v", listed)
	}
	if nextStore.EnabledMap()["custom-review"] {
		t.Fatal("deleted skill should not remain enabled")
	}
	if !nextStore.DeletedMap()["custom-review"] {
		t.Fatal("deleted skill flag was not restored")
	}
}

func TestPersistentConfigStoreMergesOldConfigWithFilesystemSkill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.json")
	if err := os.WriteFile(path, []byte(`{"skills":[{"name":"custom-review","description":"Old description","triggers":["old"],"enabled":false,"instructions":"Old instructions"}]}`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	registry := registryWithFilesystemSkill(t, "custom-review", "review", true, false)
	store := NewPersistentConfigStore(registry, path)
	skill, ok := registry.Get("custom-review")
	if !ok {
		t.Fatal("custom-review missing")
	}
	detail := skill.Detail()
	if detail.Description != "Old description" {
		t.Fatalf("unexpected description: %q", detail.Description)
	}
	if detail.Readme == "" {
		t.Fatal("old config should keep filesystem SKILL.md body when readme is absent")
	}
	if store.EnabledMap()["custom-review"] {
		t.Fatal("custom-review should be disabled by old config")
	}
}

func TestRegistryLoadsFilesystemSkill(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "custom-review")
	writeSkill(t, skillDir, "custom-review", "review", true, false)
	if err := os.WriteFile(filepath.Join(skillDir, "template.md"), []byte("Finding template"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	registry := NewRegistry()
	if err := registry.LoadFromDirectories([]Directory{{Path: root, Scope: "project"}}); err != nil {
		t.Fatalf("LoadFromDirectories returned error: %v", err)
	}
	skill, ok := registry.Get("custom-review")
	if !ok {
		t.Fatal("filesystem skill should be registered")
	}
	detail := skill.Detail()
	if detail.Source != "project" || detail.Path == "" {
		t.Fatalf("unexpected source/path: %#v", detail.Manifest)
	}
	if detail.WhenToUse != "Use when the user asks for review." {
		t.Fatalf("unexpected when_to_use: %q", detail.WhenToUse)
	}
	if !detail.UserInvocable || detail.DisableModelInvocation {
		t.Fatalf("unexpected invocation flags: %#v", detail.Manifest)
	}
	if len(detail.AllowedTools) != 1 || detail.AllowedTools[0] != "read_file" {
		t.Fatalf("unexpected allowed tools: %#v", detail.AllowedTools)
	}
	if len(detail.Resources) != 1 || detail.Resources[0].Name != "template.md" || detail.Resources[0].Content != "Finding template" {
		t.Fatalf("unexpected resources: %#v", detail.Resources)
	}
}

func TestRegistryResolveHonorsFilesystemInvocationFlags(t *testing.T) {
	registry := registryWithFilesystemSkill(t, "hidden", "hidden", false, true)
	enabled := map[string]bool{"hidden": true}
	if _, _, _, ok := registry.Resolve("/hidden test", enabled); ok {
		t.Fatal("user-invocable=false skill should not resolve via slash")
	}
	if _, _, _, ok := registry.Resolve("please use hidden", enabled); ok {
		t.Fatal("disable-model-invocation=true skill should not resolve heuristically")
	}
}

func TestRegistrySkipsNestedSkillResources(t *testing.T) {
	root := t.TempDir()
	parentDir := filepath.Join(root, "parent")
	childDir := filepath.Join(parentDir, "child")
	writeSkill(t, parentDir, "parent", "parent", true, false)
	writeSkill(t, childDir, "child", "child", true, false)
	if err := os.WriteFile(filepath.Join(childDir, "child-only.md"), []byte("child resource"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(parentDir, "parent.md"), []byte("parent resource"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	registry := NewRegistry()
	if err := registry.LoadFromDirectories([]Directory{{Path: root, Scope: "project"}}); err != nil {
		t.Fatalf("LoadFromDirectories returned error: %v", err)
	}
	parent, ok := registry.Get("parent")
	if !ok {
		t.Fatal("parent skill missing")
	}
	resources := parent.Detail().Resources
	if len(resources) != 1 || resources[0].Name != "parent.md" {
		t.Fatalf("parent should not include child skill resources: %#v", resources)
	}
}

func registryWithFilesystemSkill(t *testing.T, name string, trigger string, userInvocable bool, disableModelInvocation bool) *Registry {
	t.Helper()
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, name), name, trigger, userInvocable, disableModelInvocation)
	if err := os.WriteFile(filepath.Join(root, name, "template.md"), []byte("Finding template"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	registry := NewRegistry()
	if err := registry.LoadFromDirectories([]Directory{{Path: root, Scope: "project"}}); err != nil {
		t.Fatalf("LoadFromDirectories returned error: %v", err)
	}
	return registry
}

func writeSkill(t *testing.T, dir string, name string, trigger string, userInvocable bool, disableModelInvocation bool) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	body := `---
name: ` + name + `
description: ` + name + ` skill.
when_to_use: Use when the user asks for ` + trigger + `.
triggers:
  - ` + trigger + `
user-invocable: ` + boolString(userInvocable) + `
disable-model-invocation: ` + boolString(disableModelInvocation) + `
allowed-tools:
  - read_file
---
# ` + name + `

Read the relevant files and produce concise findings.`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
