package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/xtruder/notify-relay/internal/buildinfo"
)

func main() {
	rootCmd := newRootCmd()
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newPeersCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "notify-relay: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify-relay [flags] SUMMARY [BODY]",
		Short: "P2P notification relay",
		Long: `notify-relay — send and relay desktop notifications across devices.

By default, acts as a drop-in replacement for notify-send, forwarding
notifications to the relay daemon via gRPC.

Use "notify-relay serve" to start the relay daemon.`,
		Version:       buildinfo.String("notify-relay"),
		Args:          cobra.RangeArgs(1, 2),
		RunE:          runSend,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.SetVersionTemplate("{{.Version}}\n")

	f := cmd.Flags()
	f.StringP("urgency", "u", "", "Urgency level: low, normal, critical")
	f.Int32P("expire-time", "t", -1, "Timeout in milliseconds")
	f.StringP("icon", "i", "", "Icon path or icon name")
	f.StringP("category", "c", "", "Notification category hint")
	f.StringArrayP("hint", "H", nil, "Custom typed hint (TYPE:NAME:VALUE)")
	f.StringP("app-name", "a", "notify-send", "Application name")
	f.Uint32P("replace-id", "r", 0, "Replace an existing notification")
	f.StringArrayP("action", "A", nil, "Add an action button ([NAME=]TEXT)")
	f.BoolP("print-id", "p", false, "Print the notification ID")
	f.BoolP("wait", "w", false, "Wait for close or action signals")
	f.BoolP("transient", "e", false, "Set transient=true hint")

	return cmd
}
