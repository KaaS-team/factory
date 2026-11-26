// Package mcp serves the factory to Claude Code over stdio, using the tool
// names of KaaS-team/factory-mcp. It reaches the core only through api.Control.
package mcp

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/KaaS-team/factory/internal/api"
	"github.com/KaaS-team/factory/internal/oidc"
	"github.com/KaaS-team/factory/internal/serve/hostguard"
)

// PlanInput is the input of the plan tool.
type PlanInput struct {
	Goal string `json:"goal" jsonschema:"what to plan"`
}

// ApprovePlanInput is the input of the approve_plan tool.
type ApprovePlanInput struct {
	Goal string `json:"goal" jsonschema:"the goal to plan and approve in one call"`
}

// ExplainConfigInput is the input of the explain_config tool.
type ExplainConfigInput struct {
	Key string `json:"key" jsonschema:"config key to explain, such as tracker"`
}

// Handlers holds the optional tool implementations; a nil func makes its tool
// answer "not available in this build".
type Handlers struct {
	Plan          func(ctx context.Context, in PlanInput) (string, error)
	ApprovePlan   func(ctx context.Context, in ApprovePlanInput) (string, error)
	ExplainConfig func(ctx context.Context, in ExplainConfigInput) (string, error)
	FirstRun      func(ctx context.Context) (string, error)
	Report        func(ctx context.Context) (string, error)
	Version       func(ctx context.Context) (string, error)
	Doctor        func(ctx context.Context) (string, error)
	Packs         func(ctx context.Context) (string, error)
	Shadow        func(ctx context.Context) (string, error)
	Repomap       func(ctx context.Context, in RepomapInput) (string, error)
	// ReadOnly hides every write tool, so a call to one is refused (the --read-only flag).
	ReadOnly bool
}

// RepomapInput bounds the repository map.
type RepomapInput struct {
	Ticket string `json:"ticket,omitempty" jsonschema:"ticket text whose files and identifiers seed the ranking"`
	Budget int    `json:"budget,omitempty" jsonschema:"token budget of the map; default 1024"`
}

// Empty is the input of tools that take no arguments.
type Empty struct{}

// TicketInput names one ticket.
type TicketInput struct {
	Ticket string `json:"ticket" jsonschema:"ticket id"`
}

// SteerInput sends a message to a running ticket's session.
type SteerInput struct {
	Ticket  string `json:"ticket" jsonschema:"ticket id"`
	Message string `json:"message" jsonschema:"message for the session"`
}

// CreateTaskInput is the input of create_task.
type CreateTaskInput struct {
	Title string `json:"title" jsonschema:"Short task title"`
	Body  string `json:"body,omitempty" jsonschema:"Longer description"`
}

// HandOffInput is the input of hand_off_task.
type HandOffInput struct {
	ID   string `json:"id" jsonschema:"Task id"`
	To   string `json:"to" jsonschema:"New assignee"`
	Note string `json:"note,omitempty" jsonschema:"Context for the new assignee"`
}

// FollowupInput is the input of queue_followup.
type FollowupInput struct {
	ID      string `json:"id" jsonschema:"Task id"`
	Message string `json:"message" jsonschema:"Message text"`
}

// IDInput is the input of list_events.
type IDInput struct {
	ID string `json:"id" jsonschema:"Task id"`
}

// CreatedOutput is the answer of create_task.
type CreatedOutput struct {
	ID string `json:"id"`
}

// EventsOutput lists a task's events without their free-form data.
type EventsOutput struct {
	Events []EventView `json:"events"`
}

// ReposOutput lists the repositories the factory works on.
type ReposOutput struct {
	Repos []string `json:"repos"`
}

// TextOutput is a plain text answer.
type TextOutput struct {
	Text string `json:"text"`
}

// OK is the answer of a mutating tool.
type OK struct {
	Ticket string `json:"ticket"`
	Done   string `json:"done"`
}

// SlotView is a board slot without usage, cost or budget.
type SlotView struct {
	Ticket   api.Ticket `json:"ticket"`
	Phase    api.Phase  `json:"phase"`
	Session  string     `json:"session,omitempty"`
	LastPush time.Time  `json:"last_push,omitempty"`
}

// BoardView is the board as MCP clients see it: no token counts, cost or model.
type BoardView struct {
	Slots      []SlotView     `json:"slots"`
	Ready      []api.Ticket   `json:"ready"`
	MergeQueue []api.TicketID `json:"merge_queue"`
	FiresLeft  int            `json:"fires_left"`
}

// TicketsOutput lists the tickets on the board.
type TicketsOutput struct {
	Tickets []api.Ticket `json:"tickets"`
}

// QueueOutput lists the merge queue.
type QueueOutput struct {
	MergeQueue []api.TicketID `json:"merge_queue"`
}

// EventView is an event without its free-form data.
type EventView struct {
	Time   time.Time    `json:"time"`
	Kind   string       `json:"kind"`
	Ticket api.TicketID `json:"ticket,omitempty"`
}

// TicketView is the ticket resource: the ticket, its phase and its past events.
// HistoryAvailable is false when the Control keeps no history; HistoryNote then says so.
type TicketView struct {
	Ticket           api.Ticket  `json:"ticket"`
	Phase            api.Phase   `json:"phase"`
	Events           []EventView `json:"events"`
	HistoryAvailable bool        `json:"history_available"`
	HistoryNote      string      `json:"history_note,omitempty"`
}

// StatusInput selects the board (no ticket) or one ticket's detail.
type StatusInput struct {
	Ticket string `json:"ticket,omitempty" jsonschema:"ticket id; leave out for the whole board"`
}

// StatusOutput is the board, plus the named ticket's detail when one was asked for.
type StatusOutput struct {
	BoardView
	Detail *TicketView `json:"detail,omitempty"`
}

func boardView(b api.Board) BoardView {
	v := BoardView{Slots: []SlotView{}, Ready: nonNil(b.Ready), MergeQueue: nonNil(b.MergeQueue), FiresLeft: b.FiresLeft}
	for _, s := range b.Slots {
		sv := SlotView{Ticket: s.Ticket, Phase: s.Phase, LastPush: s.LastPush}
		if s.Session != nil {
			sv.Session = s.Session.Key
		}
		v.Slots = append(v.Slots, sv)
	}
	return v
}

func nonNil[T any](t []T) []T {
	if t == nil {
		return []T{}
	}
	return t
}

// safe hides the cause of err from the client: it names the tool and nothing else,
// so paths, hosts and stack text from the core never reach a tool result.
func safe(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed; the operator can see why in the factory log", name)
}

func unavailable(name string) error {
	return fmt.Errorf("%s: not available in this build", name)
}

// textTool wraps an optional handler: nil answers a tool error naming the tool.
func textTool[In any](name string, h func(context.Context, In) (string, error)) sdk.ToolHandlerFor[In, TextOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, TextOutput, error) {
		if h == nil {
			return nil, TextOutput{}, unavailable(name)
		}
		s, err := h(ctx, in)
		return nil, TextOutput{Text: s}, safe(name, err)
	}
}

func noArg(h func(context.Context) (string, error)) func(context.Context, Empty) (string, error) {
	if h == nil {
		return nil
	}
	return func(ctx context.Context, _ Empty) (string, error) { return h(ctx) }
}

func act(name string, f func(context.Context, TicketInput) error) sdk.ToolHandlerFor[TicketInput, OK] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in TicketInput) (*sdk.CallToolResult, OK, error) {
		if err := f(ctx, in); err != nil {
			return nil, OK{}, safe(name, err)
		}
		return nil, OK{Ticket: in.Ticket, Done: name}, nil
	}
}

// Annotations of the factory-mcp tools, carried over unchanged. The hints are explicit because the
// protocol's defaults for destructive and open-world are true.
var (
	no, yes     = false, true
	readHint    = &sdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, IdempotentHint: true, OpenWorldHint: &no}
	writeHint   = &sdk.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no}
	handOffHint = &sdk.ToolAnnotations{DestructiveHint: &yes, IdempotentHint: true, OpenWorldHint: &no}
)

func required(tool string, fields ...[2]string) error {
	for _, f := range fields {
		if strings.TrimSpace(f[1]) == "" {
			return fmt.Errorf("%s: %s must not be empty", tool, f[0])
		}
	}
	return nil
}

// NewServer returns an MCP server exposing the seventeen factory tools and the
// factory://board and factory://ticket/{id} resources.
func NewServer(ctl api.Control, extra Handlers) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "factory", Version: "v9"}, nil)
	addRegisteredTools(s, ctl, extra)
	addResources(s, ctl)
	return s
}

// addResources registers the factory://board and factory://ticket/{id} resources.
func addResources(s *sdk.Server, ctl api.Control) {
	s.AddResource(&sdk.Resource{URI: "factory://board", Name: "board", MIMEType: "application/json", Description: "The factory board"},
		func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			b, err := ctl.Board(ctx)
			if err != nil {
				return nil, safe("board", err)
			}
			return jsonResource(req.Params.URI, boardView(b))
		})
	s.AddResourceTemplate(&sdk.ResourceTemplate{URITemplate: "factory://ticket/{id}", Name: "ticket", MIMEType: "application/json", Description: "A ticket and its events"},
		func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
			return ticketResource(ctx, ctl, req.Params.URI)
		})
}

func jsonResource(uri string, v any) (*sdk.ReadResourceResult, error) {
	b, err := jsonIndent(v)
	if err != nil {
		return nil, err
	}
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: uri, MIMEType: "application/json", Text: b}}}, nil
}

func ticketResource(ctx context.Context, ctl api.Control, uri string) (*sdk.ReadResourceResult, error) {
	id, ok := strings.CutPrefix(uri, "factory://ticket/")
	if !ok || id == "" {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	b, err := ctl.Board(ctx)
	if err != nil {
		return nil, safe("ticket", err)
	}
	v, found, err := ticketView(ctx, ctl, b, id)
	if err != nil {
		return nil, safe("ticket", err)
	}
	if !found {
		return nil, sdk.ResourceNotFoundError(uri)
	}
	return jsonResource(uri, v)
}

// ticketView finds the ticket on the board and adds its history when the Control is an api.Historian.
func ticketView(ctx context.Context, ctl api.Control, b api.Board, id string) (TicketView, bool, error) {
	v := TicketView{Events: []EventView{}}
	found := false
	for _, sl := range b.Slots {
		if string(sl.Ticket.ID) == id {
			v.Ticket, v.Phase, found = sl.Ticket, sl.Phase, true
		}
	}
	for _, t := range b.Ready {
		if !found && string(t.ID) == id {
			v.Ticket, v.Phase, found = t, api.PhaseReady, true
		}
	}
	if !found {
		return v, false, nil
	}
	h, ok := ctl.(api.Historian)
	if !ok {
		v.HistoryNote = "history is unavailable: this control plane does not keep past events"
		return v, true, nil
	}
	evs, err := h.History(ctx, api.TicketID(id))
	if err != nil {
		return v, true, err
	}
	v.HistoryAvailable = true
	for _, e := range evs {
		v.Events = append(v.Events, EventView{Time: e.Time, Kind: e.Kind, Ticket: e.Ticket})
	}
	return v, true, nil
}

// TokenEnv is the environment variable holding the bearer token of the HTTP transport.
// The token is never a flag and is never written to a log or an error.
const TokenEnv = "FACTORY_MCP_TOKEN"

// Handler serves srv over streamable HTTP at /mcp. Every request needs "Authorization: Bearer <token>"
// (401 otherwise); any other path is 404. The token is compared in constant time.
func Handler(srv *sdk.Server, token string) (http.Handler, error) {
	if token == "" {
		return nil, fmt.Errorf("mcp: %s must not be empty", TokenEnv)
	}
	want := sha256.Sum256([]byte("Bearer " + token))
	mcp := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		mcp.ServeHTTP(w, r)
	}), nil
}

var portOnly = regexp.MustCompile(`^\d+$`)

// BindAddr completes a listen address: a bare port or an empty host binds loopback, never every interface.
func BindAddr(addr string) string {
	if portOnly.MatchString(addr) {
		return "127.0.0.1:" + addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "" {
		return "127.0.0.1:" + port
	}
	return addr
}

// Serve answers on ln until ctx ends, refusing a foreign Host or Origin with 403 (hostguard).
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	hs := &http.Server{Handler: hostguard.New(ln.Addr().String(), os.Getenv)(h), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Option configures NewCommand.
type Option func(*cmdConfig)

type cmdConfig struct {
	open func() (api.Control, Handlers, error)
}

// WithControl supplies the control plane and optional handlers the command serves.
func WithControl(open func() (api.Control, Handlers, error)) Option {
	return func(c *cmdConfig) { c.open = open }
}

// NewCommand returns `factory mcp`, which serves over stdio. Without
// WithControl it fails, since the command cannot reach a core by itself.
func NewCommand(opts ...Option) *cobra.Command {
	var cfg cmdConfig
	for _, o := range opts {
		o(&cfg)
	}
	var httpAddr string
	var readOnly bool
	var resource string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the factory to Claude Code over stdio, or over HTTP with --http (MCP)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cfg.open == nil {
				return errors.New("mcp: no control plane wired in this build")
			}
			token := os.Getenv(TokenEnv)
			issuer, audience := os.Getenv("FACTORY_OIDC_ISSUER"), os.Getenv("FACTORY_OIDC_AUDIENCE")
			if httpAddr != "" && token == "" && (issuer == "" || audience == "") {
				return errors.New("mcp: --http needs FACTORY_OIDC_ISSUER and FACTORY_OIDC_AUDIENCE (OAuth), or " + TokenEnv + " as a static bearer token")
			}
			c, h, err := cfg.open()
			if err != nil {
				return err
			}
			h.ReadOnly = readOnly
			srv := NewServer(c, h)
			if httpAddr == "" {
				return srv.Run(cmd.Context(), &sdk.StdioTransport{})
			}
			ln, err := net.Listen("tcp", BindAddr(httpAddr))
			if err != nil {
				return fmt.Errorf("mcp: listen: %w", err)
			}
			var handler http.Handler
			if issuer != "" && audience != "" {
				v, err := oidc.New(cmd.Context(), oidc.Config{Issuer: issuer, Audience: audience})
				if err != nil {
					return fmt.Errorf("mcp: oidc: %w", err)
				}
				if resource == "" {
					resource = audience
					if !strings.HasPrefix(resource, "http") {
						resource = "http://" + ln.Addr().String() + "/mcp"
					}
				}
				hro := h
				hro.ReadOnly = true
				ro := NewServer(c, hro)
				h, err := AuthHandler(srv, AuthConfig{Identity: OIDCIdentity(v, "default"), Resource: resource, AuthorizationServers: []string{issuer}, ReadOnly: ro})
				if err != nil {
					return err
				}
				handler = h
			} else {
				h, err := Handler(srv, token)
				if err != nil {
					return err
				}
				handler = h
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "mcp: serving http://%s/mcp\n", ln.Addr())
			return Serve(cmd.Context(), ln, handler)
		},
	}
	cmd.Flags().StringVar(&resource, "resource-url", "", "public URL of the /mcp endpoint, published in the protected resource metadata (default: FACTORY_OIDC_AUDIENCE when it is a URL)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "serve only the read tools; write tools are refused")
	cmd.Flags().StringVar(&httpAddr, "http", "", "serve streamable HTTP at /mcp on this address (default host 127.0.0.1); with FACTORY_OIDC_ISSUER and FACTORY_OIDC_AUDIENCE it requires OIDC bearer tokens; otherwise the static token is read from "+TokenEnv)
	return cmd
}
