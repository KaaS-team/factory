package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/KaaS-team/factory/internal/dispatch/broker"
)

// Command is the payload ExecRunner understands.
type Command struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir,omitempty"`
}

// ExecRunner runs payload.argv on this host when its program name is in allow. The control plane
// cannot make a worker run anything else.
func ExecRunner(allow []string) Runner {
	return func(ctx context.Context, job broker.Job, out io.Writer) (json.RawMessage, error) {
		var c Command
		if err := json.Unmarshal(job.Payload, &c); err != nil || len(c.Argv) == 0 {
			return nil, fmt.Errorf("job %s has no command", job.ID)
		}
		if !slices.Contains(allow, filepath.Base(c.Argv[0])) {
			return nil, fmt.Errorf("program %q is not allowed on this worker", filepath.Base(c.Argv[0]))
		}
		cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = c.Dir, out, out
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("command failed: %w", err)
		}
		return json.RawMessage(`{"exit":0}`), nil
	}
}
