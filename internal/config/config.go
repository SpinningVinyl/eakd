package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"eak/internal/configfile"
	"eak/internal/keycode"
)

type File struct {
	CandidateTimeout string       `json:"candidate_timeout"`
	SequenceTimeout  string       `json:"sequence_timeout"`
	SocketPath       string       `json:"socket_path"`
	AllowedUIDs      []uint32     `json:"allowed_uids"`
	Prefixes         []FilePrefix `json:"prefixes"`
}

type FilePrefix struct {
	Keys     []string      `json:"keys"`
	Bindings []FileBinding `json:"bindings"`
}

type FileBinding struct {
	Keys   []string `json:"keys"`
	Action string   `json:"action"`
}

type Config struct {
	CandidateTimeout time.Duration
	SequenceTimeout  time.Duration
	SocketPath       string
	AllowedUIDs      []uint32
	Prefixes         []Prefix
}

type Prefix struct {
	Keys     []keycode.Logical
	Bindings []Binding
}

type Binding struct {
	Keys   []keycode.Logical
	Action string
}

func Load(path string, allowInsecure bool) (Config, error) {
	file, err := configfile.Open(path, 0, allowInsecure)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	var raw File
	if err := configfile.Decode(file, &raw); err != nil {
		return Config{}, err
	}
	return compile(raw)
}

func compile(raw File) (Config, error) {
	candidateTimeout, err := parseDuration(raw.CandidateTimeout, 750*time.Millisecond)
	if err != nil {
		return Config{}, fmt.Errorf("candidate_timeout: %w", err)
	}
	sequenceTimeout, err := parseDuration(raw.SequenceTimeout, 750*time.Millisecond)
	if err != nil {
		return Config{}, fmt.Errorf("sequence_timeout: %w", err)
	}
	if candidateTimeout <= 0 || sequenceTimeout <= 0 {
		return Config{}, fmt.Errorf("timeouts must be positive")
	}

	cfg := Config{
		CandidateTimeout: candidateTimeout,
		SequenceTimeout:  sequenceTimeout,
		SocketPath:       raw.SocketPath,
		AllowedUIDs:      append([]uint32(nil), raw.AllowedUIDs...),
	}
	if len(cfg.AllowedUIDs) == 0 {
		return Config{}, fmt.Errorf("allowed_uids must contain at least one eakc user")
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = "/run/eak/eakd.sock"
	}
	if !filepath.IsAbs(cfg.SocketPath) {
		return Config{}, fmt.Errorf("socket_path must be absolute")
	}
	if len(raw.Prefixes) == 0 {
		return Config{}, fmt.Errorf("at least one prefix is required")
	}

	prefixSeen := make(map[string]bool)
	for pi, rawPrefix := range raw.Prefixes {
		keys, err := parseChord(rawPrefix.Keys)
		if err != nil {
			return Config{}, fmt.Errorf("prefix %d: %w", pi, err)
		}
		if !slices.ContainsFunc(keys, keycode.IsLogicalModifier) {
			return Config{}, fmt.Errorf("prefix %d must contain CTRL, SHIFT, ALT, or LOGO", pi)
		}
		sig := fmt.Sprint(keys)
		if prefixSeen[sig] {
			return Config{}, fmt.Errorf("duplicate prefix %d", pi)
		}
		prefixSeen[sig] = true

		prefix := Prefix{Keys: keys}
		bindingSeen := make(map[string]bool)
		for bi, rawBinding := range rawPrefix.Bindings {
			bindingKeys, err := parseChord(rawBinding.Keys)
			if err != nil {
				return Config{}, fmt.Errorf("prefix %d binding %d: %w", pi, bi, err)
			}
			if rawBinding.Action == "" {
				return Config{}, fmt.Errorf("prefix %d binding %d has an empty action", pi, bi)
			}
			bindingSig := fmt.Sprint(bindingKeys)
			if bindingSeen[bindingSig] {
				return Config{}, fmt.Errorf("prefix %d has a duplicate binding", pi)
			}
			bindingSeen[bindingSig] = true
			prefix.Bindings = append(prefix.Bindings, Binding{Keys: bindingKeys, Action: rawBinding.Action})
		}
		if len(prefix.Bindings) == 0 {
			return Config{}, fmt.Errorf("prefix %d has no bindings", pi)
		}
		cfg.Prefixes = append(cfg.Prefixes, prefix)
	}

	for i := range cfg.Prefixes {
		for j := i + 1; j < len(cfg.Prefixes); j++ {
			if subset(cfg.Prefixes[i].Keys, cfg.Prefixes[j].Keys) || subset(cfg.Prefixes[j].Keys, cfg.Prefixes[i].Keys) {
				return Config{}, fmt.Errorf("prefixes %d and %d are ambiguous subsets", i, j)
			}
		}
	}
	return cfg, nil
}

func parseDuration(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func parseChord(names []string) ([]keycode.Logical, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("empty key combination")
	}
	seen := make(map[keycode.Logical]bool)
	keys := make([]keycode.Logical, 0, len(names))
	for _, name := range names {
		key, err := keycode.Parse(name)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q", name)
		}
		seen[key] = true
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

func subset(a, b []keycode.Logical) bool {
	if len(a) >= len(b) {
		return false
	}
	for _, key := range a {
		if !slices.Contains(b, key) {
			return false
		}
	}
	return true
}
