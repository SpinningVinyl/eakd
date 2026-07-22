package executor

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"

	"eak/internal/clientconfig"
)

type Runner struct {
	actions     map[string]clientconfig.Action
	parallelism int
	logger      *log.Logger
}

func New(actions map[string]clientconfig.Action, parallelism int, logger *log.Logger) *Runner {
	return &Runner{actions: actions, parallelism: parallelism, logger: logger}
}

// Run dispatches actions through a bounded worker pool. Individual command
// failures are logged and do not terminate the client or suppress later
// actions. Context cancellation terminates running commands through os/exec.
func (r *Runner) Run(ctx context.Context, input <-chan string) {
	jobs := make(chan job)
	var workers sync.WaitGroup
	for range r.parallelism {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range jobs {
				r.execute(ctx, task)
			}
		}()
	}

	func() {
		defer close(jobs)
		for {
			select {
			case <-ctx.Done():
				return
			case id, ok := <-input:
				if !ok {
					return
				}
				action, exists := r.actions[id]
				if !exists {
					r.logger.Printf("ignore unconfigured action %q", id)
					continue
				}
				select {
				case jobs <- job{id: id, action: action}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	workers.Wait()
}

type job struct {
	id     string
	action clientconfig.Action
}

func (r *Runner) execute(ctx context.Context, task job) {
	command, err := buildCommand(ctx, task.action)
	if err != nil {
		r.logger.Printf("action %q: %v", task.id, err)
		return
	}
	command.Dir = task.action.WorkingDirectory
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	r.logger.Printf("start action %q", task.id)
	if err := command.Run(); err != nil {
		r.logger.Printf("action %q failed: %v", task.id, err)
		return
	}
	r.logger.Printf("action %q completed", task.id)
}

func buildCommand(ctx context.Context, action clientconfig.Action) (*exec.Cmd, error) {
	switch action.Type {
	case "exec":
		return exec.CommandContext(ctx, action.Command[0], action.Command[1:]...), nil
	case "shell":
		return exec.CommandContext(ctx, "/bin/sh", "-c", action.Script), nil
	default:
		return nil, fmt.Errorf("unsupported action type %q", action.Type)
	}
}
