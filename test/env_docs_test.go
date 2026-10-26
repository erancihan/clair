package test

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEnvExampleDocumentsEveryVariableTheCodeReads: an env var the code reads
// and the example file does not name is a setting nobody can discover.
func TestEnvExampleDocumentsEveryVariableTheCodeReads(t *testing.T) {
	documented := map[string]bool{}
	f, err := os.Open("../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, _, ok := strings.Cut(line, "="); ok {
			documented[strings.TrimSpace(key)] = true
		}
	}

	read := regexp.MustCompile(`(?:os\.Getenv|utils\.GetEnv|GetEnv|os\.LookupEnv)\("([A-Z0-9_]+)"`)
	var missing []string
	seen := map[string]bool{}
	err = filepath.WalkDir("../internal", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range read.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			if !documented[name] {
				missing = append(missing, name+" ("+filepath.Base(path)+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("read by the code but absent from .env.example: %s", strings.Join(missing, ", "))
	}
}
