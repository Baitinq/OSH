package agent

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type skill struct {
	name                   string
	description            string
	path                   string
	disableModelInvocation bool
}

type skillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

func loadSkills(cwd, home string) []skill {
	var roots []string
	if cwd != "" {
		cwd, _ = filepath.Abs(cwd)
		for dir := cwd; ; dir = filepath.Dir(dir) {
			roots = append(roots, filepath.Join(dir, ".agents", "skills"))
			if isGitRoot(dir) || filepath.Dir(dir) == dir {
				break
			}
		}
	}
	if home != "" {
		roots = append(roots, filepath.Join(home, ".agents", "skills"))
	}

	seenPaths := make(map[string]bool)
	seenNames := make(map[string]bool)
	var skills []skill
	for _, root := range roots {
		for _, path := range findSkillFiles(root) {
			realPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				realPath = path
			}
			if seenPaths[realPath] {
				continue
			}
			seenPaths[realPath] = true
			s, ok := readSkill(path)
			if !ok || seenNames[s.name] {
				continue
			}
			seenNames[s.name] = true
			skills = append(skills, s)
		}
	}
	return skills
}

func isGitRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func findSkillFiles(root string) []string {
	return findSkillFilesInDir(root, make(map[string]bool))
}

func findSkillFilesInDir(root string, visited map[string]bool) []string {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil || visited[realRoot] {
		return nil
	}
	visited[realRoot] = true

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.Name() != "SKILL.md" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return []string{path}
		}
	}

	var paths []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		paths = append(paths, findSkillFilesInDir(path, visited)...)
	}
	return paths
}

func readSkill(path string) (skill, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return skill{}, false
	}
	frontmatter, ok := skillFrontmatterFromFile(strings.TrimPrefix(string(content), "\ufeff"))
	if !ok {
		return skill{}, false
	}
	var metadata skillFrontmatter
	if yaml.Unmarshal([]byte(frontmatter), &metadata) != nil || strings.TrimSpace(metadata.Description) == "" {
		return skill{}, false
	}
	if metadata.Name == "" {
		metadata.Name = filepath.Base(filepath.Dir(path))
	}
	return skill{
		name:                   metadata.Name,
		description:            metadata.Description,
		path:                   path,
		disableModelInvocation: metadata.DisableModelInvocation,
	}, true
}

func skillFrontmatterFromFile(content string) (string, bool) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") {
		return "", false
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return "", false
	}
	end += 4
	return content[4:end], true
}

func formatSkillsForPrompt(skills []skill) string {
	var visible []skill
	for _, s := range skills {
		if !s.disableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}

	var prompt strings.Builder
	prompt.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\n")
	prompt.WriteString("Use the shell tool to read a skill's file when the task matches its description.\n")
	prompt.WriteString("When a skill references a relative path, resolve it against the skill directory and use the absolute path in tool commands.\n\n")
	prompt.WriteString("<available_skills>\n")
	for _, s := range visible {
		fmt.Fprintf(&prompt, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>\n",
			html.EscapeString(s.name), html.EscapeString(s.description), html.EscapeString(s.path))
	}
	prompt.WriteString("</available_skills>")
	return prompt.String()
}
