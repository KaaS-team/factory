package enterprise

import (
	"context"
	"io"
	"strings"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/api/porttest"
)

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

// core is the smallest serve.Core: a fake control plane whose loop waits for shutdown.
type core struct{ *porttest.Control }

func newCore() core { return core{porttest.NewControl(api.Board{})} }

func (core) Run(ctx context.Context) error { <-ctx.Done(); return nil }
