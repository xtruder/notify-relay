package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func runSend(cmd *cobra.Command, args []string) error {
	f := cmd.Flags()

	req := protocol.NotifyRequest{
		Summary: args[0],
	}
	if len(args) > 1 {
		req.Body = args[1]
	}

	req.AppName, _ = f.GetString("app-name")
	req.AppIcon, _ = f.GetString("icon")
	expireDur, _ := f.GetDuration("expire-time")
	if expireDur > 0 {
		req.ExpireTimeout = int32(expireDur.Milliseconds())
	}
	req.ReplacesID, _ = f.GetUint32("replace-id")
	req.PrintID, _ = f.GetBool("print-id")
	req.Wait, _ = f.GetBool("wait")

	if urgency, _ := f.GetString("urgency"); urgency != "" {
		val, err := urgencyValue(urgency)
		if err != nil {
			return err
		}
		req.Hints = append(req.Hints, protocol.Hint{Name: "urgency", Type: "byte", Value: val})
	}

	if category, _ := f.GetString("category"); category != "" {
		req.Hints = append(req.Hints, protocol.Hint{Name: "category", Type: "string", Value: category})
	}

	if transient, _ := f.GetBool("transient"); transient {
		req.Hints = append(req.Hints, protocol.Hint{Name: "transient", Type: "bool", Value: "true"})
	}

	hints, _ := f.GetStringArray("hint")
	for _, raw := range hints {
		hint, err := parseHint(raw)
		if err != nil {
			return err
		}
		req.Hints = append(req.Hints, hint)
	}

	actions, _ := f.GetStringArray("action")
	for i, raw := range actions {
		key, label := parseAction(raw, i)
		req.Actions = append(req.Actions, key, label)
	}

	if len(req.Actions) > 0 && !req.Wait {
		fmt.Fprintln(os.Stderr, "warn: actions require --wait to receive callbacks; buttons will not be shown")
	}

	endpoint, token := relayTarget()
	resp, err := sendNotification(endpoint, token, req)
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.DeadlineExceeded {
			return fmt.Errorf("timed out waiting for action")
		}
		if os.Getenv("NOTIFY_SEND_FALLBACK") == "1" {
			code, fallbackErr := fallbackNotifySend(os.Args[1:])
			if fallbackErr != nil {
				return fallbackErr
			}
			os.Exit(code)
		}
		return err
	}

	if req.PrintID {
		fmt.Println(resp.ID)
	}
	if req.Wait {
		switch resp.Event {
		case "action_invoked":
			if resp.ActionKey != "" {
				fmt.Println(resp.ActionKey)
			}
		case "closed":
			if resp.Reason != 0 {
				fmt.Fprintf(os.Stderr, "closed:%d\n", resp.Reason)
			}
		}
	}

	return nil
}

func urgencyValue(value string) (string, error) {
	switch value {
	case "low":
		return "0", nil
	case "normal":
		return "1", nil
	case "critical":
		return "2", nil
	default:
		return "", fmt.Errorf("invalid urgency %q", value)
	}
}

func parseHint(raw string) (protocol.Hint, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return protocol.Hint{}, fmt.Errorf("invalid hint %q (expected TYPE:NAME:VALUE)", raw)
	}
	return protocol.Hint{Name: parts[1], Type: parts[0], Value: parts[2]}, nil
}

func parseAction(raw string, index int) (string, string) {
	parts := strings.SplitN(raw, "=", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return fmt.Sprintf("action%d", index+1), raw
}

func relayTarget() (string, string) {
	if socketPath := os.Getenv("NOTIFY_RELAY_SOCKET"); socketPath != "" {
		return socketPath, ""
	}
	if runtime.GOOS == "linux" {
		socketPath := fmt.Sprintf("/run/user/%d/notify-relay.sock", os.Getuid())
		if _, err := os.Stat(socketPath); err == nil {
			return socketPath, ""
		}
	}

	endpoint := os.Getenv("NOTIFY_RELAY_URL")
	if endpoint == "" {
		endpoint = "127.0.0.1:8787"
	}
	token := os.Getenv("NOTIFY_RELAY_TOKEN")
	return endpoint, token
}

func sendNotification(endpoint, token string, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	timeout := 30 * time.Second
	if req.Wait {
		if req.ExpireTimeout > 0 {
			timeout = time.Duration(req.ExpireTimeout) * time.Millisecond
		} else {
			timeout = 1 * time.Hour
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	var conn *grpc.ClientConn
	var err error

	if strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, ".") {
		conn, err = grpc.DialContext(ctx, "passthrough:///unix://"+endpoint, opts...)
	} else {
		if !strings.Contains(endpoint, ":") {
			endpoint = endpoint + ":8787"
		}
		conn, err = grpc.DialContext(ctx, endpoint, opts...)
	}
	if err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	client := notify_relayv1.NewRelayServiceClient(conn)

	grpcReq := &notify_relayv1.Notification{
		AppName:       req.AppName,
		ReplacesId:    req.ReplacesID,
		AppIcon:       req.AppIcon,
		Summary:       req.Summary,
		Body:          req.Body,
		Actions:       req.Actions,
		ExpireTimeout: req.ExpireTimeout,
		Wait:          req.Wait,
		PrintId:       req.PrintID,
	}
	for _, hint := range req.Hints {
		grpcReq.Hints = append(grpcReq.Hints, &notify_relayv1.Hint{
			Name:  hint.Name,
			Type:  hint.Type,
			Value: hint.Value,
		})
	}

	if token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	}

	resp, err := client.Notify(ctx, grpcReq)
	if err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("notify: %w", err)
	}

	return protocol.NotifyResponse{
		ID:        resp.Id,
		Event:     resp.Event,
		Reason:    resp.Reason,
		ActionKey: resp.ActionKey,
	}, nil
}

func fallbackNotifySend(args []string) (int, error) {
	path, err := execLookPath("/usr/bin/notify-send")
	if err != nil {
		return 1, errors.New("relay unavailable and local notify-send missing")
	}
	return runProgram(path, args)
}

func execLookPath(preferred string) (string, error) {
	if preferred != "" {
		if _, err := os.Stat(preferred); err == nil {
			return preferred, nil
		}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, "notify-send")
		if _, err := os.Stat(candidate); err == nil && !sameFile(candidate, os.Args[0]) {
			return candidate, nil
		}
	}
	return "", errors.New("notify-send not found")
}

func sameFile(a, b string) bool {
	aInfo, errA := os.Stat(a)
	bInfo, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(aInfo, bInfo)
}

func runProgram(path string, args []string) (int, error) {
	proc, err := os.StartProcess(path, append([]string{path}, args...), &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		return 1, err
	}
	state, err := proc.Wait()
	if err != nil {
		return 1, err
	}
	return state.ExitCode(), nil
}
