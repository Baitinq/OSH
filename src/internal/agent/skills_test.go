package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, dir, metadata, body string) string {
	t.Helper()
	path := filepath.Join(root, dir, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + metadata + "\n---\n\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSkillsFindsRepoAndGlobalSkills(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	cwd := filepath.Join(repo, "service")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	repoPath := writeSkill(t, filepath.Join(repo, ".agents", "skills"), "review", "name: repo-review\ndescription: Review this repository", "Review carefully.")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "search", "name: global-search\ndescription: Search globally", "Search carefully.")
	writeSkill(t, filepath.Join(root, ".agents", "skills"), "outside", "name: outside\ndescription: Must not load", "Outside.")

	skills := loadSkills(cwd, home)
	if len(skills) != 2 {
		t.Fatalf("skills = %#v", skills)
	}
	if skills[0].name != "repo-review" || skills[0].path != repoPath || skills[1].name != "global-search" {
		t.Fatalf("skills = %#v", skills)
	}
}

func TestLoadSkillsPrefersProjectSkills(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectPath := writeSkill(t, filepath.Join(cwd, ".agents", "skills"), "project", "name: shared\ndescription: Project version", "Project.")
	writeSkill(t, filepath.Join(home, ".agents", "skills"), "global", "name: shared\ndescription: Global version", "Global.")

	skills := loadSkills(cwd, home)
	if len(skills) != 1 || skills[0].path != projectPath {
		t.Fatalf("skills = %#v", skills)
	}
}

func TestLoadSkillsFollowsSymlinkedSkillDirectories(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "shared", "linked")
	writeSkill(t, filepath.Join(root, "shared"), "linked", "name: linked\ndescription: Linked skill", "Linked.")
	skillsRoot := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(skillsRoot, "linked")); err != nil {
		t.Fatal(err)
	}

	paths := findSkillFiles(skillsRoot)
	if len(paths) != 1 || filepath.Base(filepath.Dir(paths[0])) != "linked" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestReadSkillUsesDirectoryNameAndParsesMultilineDescription(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "pdf-tools", "description: |\n  Work with PDFs.\n  Use for PDF documents.\ndisable-model-invocation: true", "# PDF tools\n\nRun the helper.")

	s, ok := readSkill(path)
	if !ok || s.name != "pdf-tools" || !strings.Contains(s.description, "Use for PDF") || !s.disableModelInvocation {
		t.Fatalf("skill = %#v, ok = %v", s, ok)
	}
}

func TestFindSkillFilesStopsAtSkillRoot(t *testing.T) {
	root := t.TempDir()
	parent := writeSkill(t, root, "parent", "name: parent\ndescription: Parent skill", "Parent.")
	writeSkill(t, filepath.Join(root, "parent"), "child", "name: child\ndescription: Child skill", "Child.")

	paths := findSkillFiles(root)
	if len(paths) != 1 || paths[0] != parent {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestFormatSkillsForPromptUsesProgressiveDisclosure(t *testing.T) {
	skills := []skill{
		{name: "visible", description: "Use <carefully>", path: "/skills/visible/SKILL.md"},
		{name: "manual", description: "Manual only", path: "/skills/manual/SKILL.md", disableModelInvocation: true},
	}
	prompt := formatSkillsForPrompt(skills)
	for _, want := range []string{"<available_skills>", "<name>visible</name>", "Use &lt;carefully&gt;", "/skills/visible/SKILL.md", "Use the shell tool to read"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "manual") {
		t.Fatalf("hidden skill appeared in prompt: %s", prompt)
	}
}

func TestBuildSystemPromptIncludesSkills(t *testing.T) {
	prompt := buildSystemPromptWithSkills("/work/project", []skill{{name: "test", description: "Testing workflow", path: "/skills/test/SKILL.md"}})
	if !strings.Contains(prompt, "<name>test</name>") || !strings.Contains(prompt, "Testing workflow") {
		t.Fatalf("prompt = %q", prompt)
	}
}
