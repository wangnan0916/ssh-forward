package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ConfiguredHosts returns literal Host aliases in OpenSSH file order.
// Include directives are walked here; Host tokens are split on whitespace.
func ConfiguredHosts(path string) ([]string, error) {
	hosts, err := configuredHosts(path, make(map[string]bool), 0)
	return dedupeHosts(hosts), err
}

func configuredHosts(path string, seen map[string]bool, depth int) ([]string, error) {
	if depth > 5 {
		return nil, nil
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if seen[resolved] {
		return nil, nil
	}
	content, err := os.ReadFile(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen[resolved] = true

	var hosts []string
	for line := range strings.SplitSeq(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch {
		case strings.EqualFold(fields[0], "Host"):
			for _, alias := range fields[1:] {
				if strings.HasPrefix(alias, "#") {
					break
				}
				if alias = strings.Trim(alias, `"'`); literalHost(alias) {
					hosts = append(hosts, alias)
				}
			}
		case strings.EqualFold(fields[0], "Include"):
			for _, pattern := range fields[1:] {
				if pattern == "=" {
					continue
				}
				if strings.HasPrefix(pattern, "#") {
					break
				}
				matches, err := expandInclude(strings.Trim(pattern, `"`))
				if err != nil {
					continue
				}
				for _, match := range matches {
					included, err := configuredHosts(match, seen, depth+1)
					if err == nil {
						hosts = append(hosts, included...)
					}
				}
			}
		}
	}
	return hosts, nil
}

func literalHost(name string) bool {
	return name != "" && !strings.ContainsAny(name, "*?!")
}

func expandInclude(pattern string) ([]string, error) {
	if !filepath.IsAbs(pattern) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		if rest, ok := strings.CutPrefix(pattern, "~/"); ok {
			pattern = filepath.Join(home, rest)
		} else {
			pattern = filepath.Join(home, ".ssh", pattern)
		}
	}
	return filepath.Glob(pattern)
}

func dedupeHosts(hosts []string) []string {
	seen := make(map[string]bool, len(hosts))
	unique := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if !seen[host] {
			seen[host] = true
			unique = append(unique, host)
		}
	}
	return unique
}
