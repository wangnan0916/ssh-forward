package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// HostTarget stores connection parameters, never commands or process environments.
// Diagnostic-only candidates are visible but never connected.
type HostTarget struct {
	Target     string   `json:"target"`
	Arguments  []string `json:"arguments,omitempty"`
	Diagnostic string   `json:"diagnostic,omitempty"`
}

func validateTarget(name string, target HostTarget) error {
	if !core.ValidHostName(name) || !core.ValidHostName(target.Target) {
		return errors.New("invalid SSH target")
	}
	for i := 0; i < len(target.Arguments); i += 2 {
		if i+1 >= len(target.Arguments) {
			return errors.New("incomplete SSH connection option")
		}
		flag, value := target.Arguments[i], target.Arguments[i+1]
		if strings.ContainsAny(value, "\n\r\x00") || value == "" {
			return errors.New("invalid SSH connection option")
		}
		switch flag {
		case "-p":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 65535 {
				return errors.New("invalid SSH port")
			}
		case "-F", "-i":
			if !filepath.IsAbs(value) {
				return errors.New("SSH config and identity paths must be absolute")
			}
		case "-l", "-J":
		case "-o":
			key, _, ok := strings.Cut(value, "=")
			if !ok || !slices.Contains([]string{"user", "port", "hostname", "identityfile", "identitiesonly", "proxyjump", "hostkeyalias", "addressfamily"}, strings.ToLower(key)) {
				return errors.New("unsupported SSH option; use an SSH config alias")
			}
			if strings.EqualFold(key, "IdentityFile") {
				_, path, _ := strings.Cut(value, "=")
				if !filepath.IsAbs(path) {
					return errors.New("identity path must be absolute")
				}
			}
		default:
			return errors.New("unsupported SSH option; use an SSH config alias")
		}
	}
	return nil
}

func targetID(target HostTarget) string {
	if len(target.Arguments) == 0 {
		return target.Target
	}
	encoded, _ := json.Marshal(target)
	hash := sha256.Sum256(encoded)
	prefix := target.Target
	for len(prefix) > 220 {
		_, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
	}
	return fmt.Sprintf("%s-%x", prefix, hash[:6])
}

// parseSSHProcess consumes exact OS argv. Unsupported options require manual completion.
func parseSSHProcess(args []string) (HostTarget, bool) {
	target := HostTarget{}
	unsupported := false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			i++
			if i >= len(args) {
				return HostTarget{}, false
			}
			target.Target = args[i]
			break
		}
		if !strings.HasPrefix(arg, "-") {
			target.Target = arg
			break
		}
		if arg == "-4" || arg == "-6" {
			family := "inet"
			if arg == "-6" {
				family = "inet6"
			}
			target.Arguments = append(target.Arguments, "-o", "AddressFamily="+family)
			continue
		}
		if arg == "-G" || arg == "-O" || arg == "-V" || arg == "-Q" {
			return HostTarget{}, false
		}
		if len(arg) < 2 {
			return HostTarget{}, false
		}
		flag := arg[:2]
		if strings.Contains("pFilJoLRDWSEbwcm", string(arg[1])) {
			value := arg[2:]
			if value == "" {
				i++
				if i >= len(args) {
					return HostTarget{}, false
				}
				value = args[i]
			}
			if flag == "-S" && strings.HasPrefix(value, "master-") {
				return HostTarget{}, false
			}
			switch flag {
			case "-p", "-F", "-i", "-l", "-J", "-o":
				target.Arguments = append(target.Arguments, flag, value)
			case "-L", "-R", "-D": // User forwards are not copied.
			default:
				unsupported = true
			}
		} else if strings.Trim(arg[1:], "1246AaCfgKkMNnqsTtVvXxYy") != "" {
			unsupported = true
		}
	}
	if !core.ValidHostName(target.Target) {
		return HostTarget{}, false
	}
	if unsupported || validateTarget(target.Target, target) != nil {
		target.Arguments = nil
		target.Diagnostic = "discovered_unsupported"
	}
	return target, true
}
