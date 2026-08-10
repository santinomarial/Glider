// Command docs-check validates repository Markdown structure and local links,
// and optionally extracts Mermaid blocks for syntax rendering.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var markdownLink = regexp.MustCompile(`\[[^]]*\]\(([^)]+)\)`)

func main() {
	root := flag.String("root", ".", "repository root")
	mermaidDir := flag.String("mermaid-dir", "", "optional directory for extracted Mermaid definitions")
	flag.Parse()

	files, err := markdownFiles(*root)
	if err != nil {
		fatal(err)
	}
	errors := make([]string, 0)
	diagram := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		rel, _ := filepath.Rel(*root, path)
		text := string(data)
		if countH1(text) != 1 {
			errors = append(errors, fmt.Sprintf("%s: expected exactly one level-one heading", rel))
		}
		for _, match := range markdownLink.FindAllStringSubmatch(text, -1) {
			target := strings.TrimSpace(strings.Split(match[1], " ")[0])
			if target == "" || strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target = strings.Split(strings.Split(target, "#")[0], "?")[0]
			if filepath.IsAbs(target) {
				errors = append(errors, fmt.Sprintf("%s: local link must be relative: %s", rel, target))
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
			if _, err := os.Stat(resolved); err != nil {
				errors = append(errors, fmt.Sprintf("%s: broken local link %s", rel, match[1]))
			}
		}
		blocks, err := mermaidBlocks(text)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		for _, block := range blocks {
			diagram++
			if *mermaidDir != "" {
				name := fmt.Sprintf("diagram-%03d.mmd", diagram)
				if err := os.WriteFile(filepath.Join(*mermaidDir, name), []byte(block), 0o644); err != nil {
					fatal(err)
				}
			}
		}
	}
	if len(errors) != 0 {
		sort.Strings(errors)
		for _, message := range errors {
			fmt.Fprintln(os.Stderr, message)
		}
		os.Exit(1)
	}
	fmt.Printf("DOCS GREEN: %d Markdown files, %d Mermaid diagrams, all local links resolved\n", len(files), diagram)
}

func markdownFiles(root string) ([]string, error) {
	files := make([]string, 0)
	for _, top := range []string{"README.md", "SECURITY.md"} {
		path := filepath.Join(root, top)
		if _, err := os.Stat(path); err == nil {
			files = append(files, path)
		}
	}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func countH1(text string) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(text))
	inFence := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(line, "# ") {
			count++
		}
	}
	return count
}

func mermaidBlocks(text string) ([]string, error) {
	blocks := make([]string, 0)
	scanner := bufio.NewScanner(strings.NewReader(text))
	inMermaid := false
	var current strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if !inMermaid && strings.TrimSpace(line) == "```mermaid" {
			inMermaid = true
			current.Reset()
			continue
		}
		if inMermaid && strings.TrimSpace(line) == "```" {
			blocks = append(blocks, current.String())
			inMermaid = false
			continue
		}
		if inMermaid {
			current.WriteString(line)
			current.WriteByte('\n')
		}
	}
	if inMermaid {
		return nil, fmt.Errorf("unclosed Mermaid fence")
	}
	return blocks, scanner.Err()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
