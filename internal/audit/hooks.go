package audit

import "github.com/spf13/cobra"

var (
	extraSinks []func() (Sink, error)
	extraCmds  []func() *cobra.Command
)

// RegisterSink adds a sink that Install puts beside the file and OTLP sinks. The ee build uses it
// for the hash chain. A build with no registration records exactly as before.
func RegisterSink(open func() (Sink, error)) { extraSinks = append(extraSinks, open) }

// RegisterSubcommand adds a subcommand to `factory audit`, such as verify or search.
func RegisterSubcommand(build func() *cobra.Command) { extraCmds = append(extraCmds, build) }
