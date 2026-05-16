package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
)

func newPeersCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "peers",
		Short:         "List peers known to the local relay daemon",
		Long:          "Connects to the local relay's gRPC service and prints the current peer table.",
		RunE:          runPeers,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}

func runPeers(cmd *cobra.Command, _ []string) error {
	endpoint, token := relayTarget()

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()

	conn, err := dialRelay(ctx, endpoint)
	if err != nil {
		return err
	}
	defer conn.Close()

	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}

	client := notify_relayv1.NewRelayServiceClient(conn)
	resp, err := client.ListPeers(ctx, &notify_relayv1.ListPeersRequest{})
	if err != nil {
		return fmt.Errorf("list peers: %w", err)
	}

	printPeers(os.Stdout, resp.Peers)
	return nil
}

func dialRelay(ctx context.Context, endpoint string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	if strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, ".") {
		return grpc.DialContext(ctx, "passthrough:///unix://"+endpoint, opts...)
	}
	if !strings.Contains(endpoint, ":") {
		endpoint = endpoint + ":8787"
	}
	return grpc.DialContext(ctx, endpoint, opts...)
}

func printPeers(w *os.File, peers []*notify_relayv1.Peer) {
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].IsLocal != peers[j].IsLocal {
			return peers[i].IsLocal
		}
		if peers[i].Priority != peers[j].Priority {
			return peers[i].Priority < peers[j].Priority
		}
		return peers[i].Hostname < peers[j].Hostname
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLAST SEEN\tPRESENCE\tPRIORITY")
	if len(peers) == 0 {
		fmt.Fprintln(tw, "(no peers)\t\t\t")
		tw.Flush()
		return
	}
	now := time.Now()
	for _, p := range peers {
		name := p.Hostname
		if name == "" {
			name = shortPeerID(p.PeerId)
		}
		if p.IsLocal {
			name += " (local)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n",
			name,
			formatLastSeen(p.LastSeenUnixNano, now),
			formatPresence(p.IsPresent),
			p.Priority,
		)
	}
	tw.Flush()
}

func formatLastSeen(unixNano int64, now time.Time) string {
	if unixNano == 0 {
		return "never"
	}
	d := now.Sub(time.Unix(0, unixNano))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatPresence(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

func shortPeerID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:6] + "…" + id[len(id)-6:]
}
