package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gofrs/flock"
	"github.com/shirou/gopsutil/v4/process"
)

// HostTarget stores connection parameters, never the remote command or process
// environment. A diagnostic-only candidate is visible but never connected.
type HostTarget struct {
	Target     string   `json:"target"`
	Arguments  []string `json:"arguments,omitempty"`
	Diagnostic string   `json:"diagnostic,omitempty"`
}

func validTargetName(s string) bool {
	return utf8.ValidString(s) && s != "" && len(s) <= 255 && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "*?!") && strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func validateTarget(name string, target HostTarget) error {
	if !validTargetName(name) || !validTargetName(target.Target) {
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

func discoveryPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "discovered-hosts.json")
}
func loadDiscovered(configPath string) (map[string]HostTarget, error) {
	targets := make(map[string]HostTarget)
	content, err := os.ReadFile(discoveryPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return targets, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &targets); err != nil {
		return nil, err
	}
	if targets == nil {
		targets = make(map[string]HostTarget)
	}
	for name, target := range targets {
		if err := validateTarget(name, target); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

// parseSSHProcess uses exact argv from the OS, never shell-splits ps output.
// Unsupported connection settings become candidates for manual completion.
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
	if !validTargetName(target.Target) {
		return HostTarget{}, false
	}
	if unsupported || validateTarget(target.Target, target) != nil {
		target.Arguments = nil
		target.Diagnostic = "discovered_unsupported"
	}
	return target, true
}

type hostProcess struct {
	pid, ppid int32
	uid       uint32
	name      string
}

func hostProcesses(ctx context.Context) ([]hostProcess, error) {
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return nil, err
	}
	processes := make([]hostProcess, 0, len(pids))
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := process.Process{Pid: pid}
		name, err := p.NameWithContext(ctx)
		if err != nil {
			continue
		}
		ppid, err := p.PpidWithContext(ctx)
		if err != nil {
			continue
		}
		entry := hostProcess{pid: pid, ppid: ppid, name: name}
		if name == "ssh" {
			uids, err := p.UidsWithContext(ctx)
			if err != nil || len(uids) == 0 {
				continue
			}
			entry.uid = uids[0]
			// Linux exposes real/effective/saved UIDs; Darwin returns only effective.
			if len(uids) > 1 {
				entry.uid = uids[1]
			}
		}
		processes = append(processes, entry)
	}
	return processes, nil
}

func processArguments(ctx context.Context, pid int32) ([]string, error) {
	p := process.Process{Pid: pid}
	return p.CmdlineSliceWithContext(ctx)
}

func discoverProcesses(processes []hostProcess, ownPID int32, uid uint32, readArgs func(int32) ([]string, error)) map[string]HostTarget {
	excluded := map[int32]bool{ownPID: true}
	for _, p := range processes {
		if p.name == "ssh-forward" {
			excluded[p.pid] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range processes {
			if excluded[p.ppid] && !excluded[p.pid] {
				excluded[p.pid] = true
				changed = true
			}
		}
	}
	targets := make(map[string]HostTarget)
	for _, p := range processes {
		if p.uid != uid || excluded[p.pid] || p.name != "ssh" {
			continue
		}
		args, err := readArgs(p.pid)
		if err != nil || len(args) == 0 {
			continue
		}
		if target, ok := parseSSHProcess(args); ok {
			targets[targetID(target)] = target
		}
	}
	return targets
}

func DiscoverHosts(ctx context.Context, configPath string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	processes, err := hostProcesses(ctx)
	if err != nil {
		return err
	}
	found := discoverProcesses(processes, int32(os.Getpid()), uint32(os.Geteuid()), func(pid int32) ([]string, error) { return processArguments(ctx, pid) })
	return rememberDiscovered(ctx, configPath, found)
}

func rememberDiscovered(ctx context.Context, configPath string, found map[string]HostTarget) error {
	// The background scan and an explicit `host discover` can run together.
	// Lock only the registry merge; main config remains owned by CLI mutations.
	if len(found) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	lock := flock.New(discoveryPath(configPath) + ".lock")
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	existing, err := loadDiscovered(configPath)
	if err != nil {
		return err
	}
	changed := false
	for name, target := range found {
		if old, ok := existing[name]; ok && (old.Diagnostic == "" || target.Diagnostic != "") {
			continue
		}
		existing[name] = target
		changed = true
	}
	if !changed {
		return nil
	}
	return writeJSONC(discoveryPath(configPath), existing)
}

func SetHost(path, name string, target HostTarget, ignore bool) error {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return err
	}
	if !validTargetName(name) {
		return errors.New("invalid host name")
	}
	config.IgnoredHosts = slices.DeleteFunc(config.IgnoredHosts, func(s string) bool { return s == name })
	if ignore {
		config.IgnoredHosts = append(config.IgnoredHosts, name)
	} else {
		if target.Target == "" {
			target.Target = name
		}
		if err := validateTarget(name, target); err != nil {
			return err
		}
		if config.Hosts == nil {
			config.Hosts = make(map[string]HostTarget)
		}
		config.Hosts[name] = target
	}
	slices.Sort(config.IgnoredHosts)
	return config.save(path)
}

func HostList(path string) (map[string]HostTarget, []string, error) {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return nil, nil, err
	}
	targets, err := config.hostTargets(path)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range config.IgnoredHosts {
		if _, ok := targets[name]; !ok {
			targets[name] = HostTarget{Target: name}
		}
	}
	return targets, config.IgnoredHosts, nil
}

func EnableHost(path, name string) error {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return err
	}
	if !validTargetName(name) {
		return errors.New("invalid host name")
	}
	config.IgnoredHosts = slices.DeleteFunc(config.IgnoredHosts, func(s string) bool { return s == name })
	return config.save(path)
}

func (config configuration) hostTargets(path string) (map[string]HostTarget, error) {
	targets, err := loadDiscovered(path)
	if err != nil {
		return nil, err
	}
	for name, target := range config.Hosts {
		targets[name] = target
	}
	return targets, nil
}
