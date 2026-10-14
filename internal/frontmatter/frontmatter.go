// Package frontmatter reads markdown files that start with a header of
// "key: value" lines between two "---" lines, as agent and skill files do:
//
//	---
//	name: explore
//	description: Read-only search of the codebase.
//	tools: Read, Glob
//	---
//	The body: everything after the header.
//
// It reads the subset of YAML these files use: one value per key, optionally
// quoted, or a block ("|" or ">") of indented lines.
package frontmatter

import (
	"errors"
	"slices"
	"strings"
)

type Document struct {
	Fields map[string]string
	Body   string
}

func Parse(data []byte) (Document, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if lines[0] != "---" {
		return Document{}, errors.New("missing front matter: the file must start with ---")
	}
	end := slices.Index(lines[1:], "---") + 1
	if end == 0 {
		return Document{}, errors.New("front matter is not closed with ---")
	}
	return Document{
		Fields: parseFields(lines[1:end]),
		Body:   strings.TrimSpace(strings.Join(lines[end+1:], "\n")),
	}, nil
}

// List splits a comma-separated value such as "Read, Glob" or "[Read, Glob]".
func List(value string) []string {
	var items []string
	for item := range strings.SplitSeq(strings.Trim(value, "[]"), ",") {
		if item = unquote(strings.TrimSpace(item)); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseFields(lines []string) map[string]string {
	fields := map[string]string{}
	for i := 0; i < len(lines); i++ {
		key, value, ok := strings.Cut(lines[i], ":")
		if !ok || strings.HasPrefix(key, " ") || strings.HasPrefix(key, "#") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "|" || value == ">" {
			// "|" keeps the line breaks, ">" joins the lines with spaces.
			sep := " "
			if value == "|" {
				sep = "\n"
			}
			var block []string
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], " ") {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			value = strings.Join(block, sep)
		}
		fields[strings.TrimSpace(key)] = unquote(value)
	}
	return fields
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
