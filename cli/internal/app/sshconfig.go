package app

import (
	"os"
	"path/filepath"
	"strings"
)

// ConfiguredHosts reads the user's SSH client configuration and returns
// the literal Host aliases defined there, in file order. Patterns (Host
// *, Host *.example.com) are excluded: they name many machines, not one.
// Include directives are followed recursively; missing include files are
// ignored (like ssh), a missing top-level file means "no hosts
// configured". Only the Host and Include directives are read — everything
// else in the file is configuration for ssh itself, not for us.
func ConfiguredHosts(configPath string) ([]string, error) {
	return configuredHosts(configPath, make(map[string]bool), 0)
}

func configuredHosts(configPath string, seen map[string]bool, depth int) ([]string, error) {
	if depth > 8 {
		return nil, nil // include depth guard; deeper files are ignored
	}
	resolved, err := expandUserPath(configPath)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if seen[resolved] {
		return nil, nil // include cycle guard
	}
	seen[resolved] = true

	var hosts []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		switch {
		case strings.EqualFold(fields[0], "Host"):
			for _, name := range fields[1:] {
				if literalHost(name) {
					hosts = append(hosts, name)
				}
			}
		case strings.EqualFold(fields[0], "Include"):
			for _, pattern := range fields[1:] {
				included, err := expandInclude(pattern, filepath.Dir(resolved))
				if err != nil {
					continue // unreadable includes are ignored, like ssh
				}
				for _, path := range included {
					nested, err := configuredHosts(path, seen, depth+1)
					if err != nil {
						continue
					}
					hosts = append(hosts, nested...)
				}
			}
		}
	}
	return dedupeHosts(hosts), nil
}

// literalHost reports whether a Host value names one machine: patterns
// (with *, ?, or !) are excluded.
func literalHost(name string) bool {
	return !strings.ContainsAny(name, "*?!")
}

// expandUserPath resolves a leading ~/ against the user's home directory.
func expandUserPath(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, path[2:]), nil
}

// expandInclude resolves one Include value into concrete file paths:
// relative patterns are based on ~/.ssh (like ssh), ~/ expands, and glob
// characters expand. Unmatched globs yield nothing, silently.
func expandInclude(pattern, dir string) ([]string, error) {
	resolved := pattern
	if strings.HasPrefix(resolved, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		resolved = filepath.Join(home, resolved[2:])
	} else if !filepath.IsAbs(resolved) {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		resolved = filepath.Join(home, ".ssh", resolved)
	}
	matches, err := filepath.Glob(resolved)
	if err != nil {
		return nil, err
	}
	return matches, nil
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
