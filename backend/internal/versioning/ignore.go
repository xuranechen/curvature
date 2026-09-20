package versioning

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// builtinIgnorePatterns are always excluded from snapshots, regardless of any
// project-level ignore file. VCS metadata, the Curvature metadata directory and
// dependency trees are never worth versioning and would swamp the store.
var builtinIgnorePatterns = []string{
	".git/",
	".hg/",
	".svn/",
	".curvature/",
	"node_modules/",
	"__pycache__/",
	".DS_Store",
	"Thumbs.db",
	"*.tmp",
	"*.swp",
	"*~",
}

// ignoreRule is one compiled pattern.
type ignoreRule struct {
	negate bool
	// dirOnly patterns (trailing slash) only match directories.
	dirOnly bool
	re      *regexp.Regexp
}

// ignoreMatcher evaluates ignore patterns with gitignore semantics: the last
// matching rule wins, and a negated rule re-includes a path.
type ignoreMatcher struct {
	rules []ignoreRule
}

// buildIgnoreMatcher assembles the rule set for a project root.
//
// Order matters: built-in defaults first, then root .gitignore, then root
// .versioningignore, then any configured extras. Later rules win the last-match
// contest, so a project can re-include something a default excluded.
func buildIgnoreMatcher(rootPath, storeDir string, extra []string) (*ignoreMatcher, error) {
	patterns := make([]string, 0, len(builtinIgnorePatterns)+len(extra)+16)
	patterns = append(patterns, builtinIgnorePatterns...)

	for _, name := range []string{".gitignore", ".versioningignore"} {
		lines, err := readIgnoreFile(filepath.Join(rootPath, name))
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, lines...)
	}

	// The store itself must never be snapshotted, otherwise every snapshot
	// would contain the previous snapshots.
	if rel := relativeInsideRoot(rootPath, storeDir); rel != "" {
		patterns = append(patterns, rel+"/")
	}

	patterns = append(patterns, extra...)
	return newIgnoreMatcher(patterns), nil
}

// readIgnoreFile returns the meaningful lines of an ignore file. A missing file
// is not an error.
func readIgnoreFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var out []string
	scanner := bufio.NewScanner(file)
	// Ignore files are small, but be tolerant of long generated lines.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		out = append(out, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// relativeInsideRoot returns the slash-separated path of target relative to
// root, or "" when target is not strictly inside root.
func relativeInsideRoot(root, target string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// newIgnoreMatcher compiles raw pattern lines.
func newIgnoreMatcher(patterns []string) *ignoreMatcher {
	m := &ignoreMatcher{}
	for _, raw := range patterns {
		rule, ok := compileIgnoreRule(raw)
		if !ok {
			continue
		}
		m.rules = append(m.rules, rule)
	}
	return m
}

// compileIgnoreRule parses one gitignore-style line. It returns false for blank
// lines, comments and patterns that cannot be compiled.
func compileIgnoreRule(raw string) (ignoreRule, bool) {
	line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}

	rule := ignoreRule{}
	if strings.HasPrefix(line, "!") {
		rule.negate = true
		line = strings.TrimSpace(line[1:])
		if line == "" {
			return ignoreRule{}, false
		}
	}
	// A trailing backslash escapes a literal space; strip the escape.
	if strings.HasSuffix(line, `\ `) {
		line = strings.TrimSuffix(line, `\ `) + " "
	}
	if strings.HasSuffix(line, "/") {
		rule.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if line == "" {
		return ignoreRule{}, false
	}

	anchored := strings.HasPrefix(line, "/") || strings.Contains(line, "/")
	line = strings.TrimPrefix(line, "/")

	re, err := compileIgnoreGlob(line, anchored)
	if err != nil {
		return ignoreRule{}, false
	}
	rule.re = re
	return rule, true
}

// compileIgnoreGlob converts a gitignore glob into an anchored regexp.
//
//   - matches any run of characters except "/"
//     ?  matches one character except "/"
//     ** matches across "/"
func compileIgnoreGlob(pattern string, anchored bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		// An unanchored pattern may match at any depth.
		b.WriteString(`(?:^|.*/)`)
	}

	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			run := i
			for run+1 < len(pattern) && pattern[run+1] == '*' {
				run++
			}
			if run > i {
				// "**/" spans zero or more directories; a bare "**" spans
				// anything at all.
				if run+1 < len(pattern) && pattern[run+1] == '/' {
					b.WriteString(`(?:.*/)?`)
					i = run + 1
				} else {
					b.WriteString(`.*`)
					i = run
				}
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// ignored reports whether a project-relative path is excluded.
func (m *ignoreMatcher) ignored(rel string, isDir bool) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "./")
	if rel == "" || rel == "." {
		return false
	}
	parts := strings.Split(rel, "/")

	ignored := false
	for _, rule := range m.rules {
		if rule.matches(rel, parts, isDir) {
			ignored = !rule.negate
		}
	}
	return ignored
}

// matches evaluates a single rule against a path.
//
// Directory-only rules match the path itself when it is a directory, and any
// proper ancestor directory otherwise — that is what makes "build/" exclude
// "build/out/app.js".
func (r ignoreRule) matches(rel string, parts []string, isDir bool) bool {
	if !r.dirOnly {
		return r.re.MatchString(rel)
	}
	// Test every ancestor directory, plus the path itself when it is a dir.
	for i := 1; i <= len(parts); i++ {
		if i == len(parts) && !isDir {
			break
		}
		if r.re.MatchString(strings.Join(parts[:i], "/")) {
			return true
		}
	}
	return false
}
