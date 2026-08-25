// SPDX-License-Identifier: GPL-2.0-or-later

package clientconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"eak/internal/action"
	"eak/internal/configfile"
)

const (
	defaultSocketPath  = "/run/eak/eakd.sock"
	defaultMaxParallel = 4
	defaultQueueSize   = 64
)

type File struct {
	SocketPath  string                `json:"socket_path"`
	MaxParallel int                   `json:"max_parallel"`
	QueueSize   int                   `json:"queue_size"`
	Actions     map[string]FileAction `json:"actions"`
}

type FileAction struct {
	Type             string   `json:"type"`
	Command          []string `json:"command"`
	Script           string   `json:"script"`
	WorkingDirectory string   `json:"working_directory"`
}

type Config struct {
	SocketPath  string
	MaxParallel int
	QueueSize   int
	Actions     map[string]Action
}

type Action struct {
	Command          []string
	WorkingDirectory string
}

func DefaultPath() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(directory, "eak", "eakc.json"), nil
}

func Load(path string, allowInsecure bool) (Config, error) {
	file, err := configfile.Open(path, uint32(os.Geteuid()), allowInsecure)
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
	cfg := Config{
		SocketPath:  raw.SocketPath,
		MaxParallel: raw.MaxParallel,
		QueueSize:   raw.QueueSize,
		Actions:     make(map[string]Action, len(raw.Actions)),
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = defaultSocketPath
	}
	if !filepath.IsAbs(cfg.SocketPath) {
		return Config{}, fmt.Errorf("socket_path must be absolute")
	}
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = defaultMaxParallel
	}
	if cfg.MaxParallel < 1 || cfg.MaxParallel > 128 {
		return Config{}, fmt.Errorf("max_parallel must be between 1 and 128")
	}
	if cfg.QueueSize == 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.QueueSize < 1 || cfg.QueueSize > 4096 {
		return Config{}, fmt.Errorf("queue_size must be between 1 and 4096")
	}
	if len(raw.Actions) == 0 {
		return Config{}, fmt.Errorf("at least one action is required")
	}

	for id, rawAction := range raw.Actions {
		if err := action.ValidateID(id); err != nil {
			return Config{}, err
		}
		if rawAction.WorkingDirectory != "" && !filepath.IsAbs(rawAction.WorkingDirectory) {
			return Config{}, fmt.Errorf("action %q: working_directory must be absolute", id)
		}

		var command []string
		switch rawAction.Type {
		case "exec":
			if len(rawAction.Command) == 0 || rawAction.Command[0] == "" {
				return Config{}, fmt.Errorf("action %q: exec requires a non-empty command array", id)
			}
			if rawAction.Script != "" {
				return Config{}, fmt.Errorf("action %q: exec cannot contain script", id)
			}
			command = append([]string(nil), rawAction.Command...)

		case "shell":
			if rawAction.Script == "" {
				return Config{}, fmt.Errorf("action %q: shell requires a non-empty script", id)
			}
			if len(rawAction.Command) != 0 {
				return Config{}, fmt.Errorf("action %q: shell cannot contain command", id)
			}
			command = []string{"/bin/sh", "-c", rawAction.Script}
		default:
			return Config{}, fmt.Errorf("action %q: unsupported type %q", id, rawAction.Type)
		}

		cfg.Actions[id] = Action{
			Command:          command,
			WorkingDirectory: rawAction.WorkingDirectory,
		}
	}
	return cfg, nil
}
