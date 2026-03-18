package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/xtruder/notify-relay/internal/buildinfo"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify-send-proxy: %v\n", err)
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	parsed, err := parseArgs(args)
	if err != nil {
		if errors.Is(err, errHelp) {
			printHelp()
			return 0, nil
		}
		if errors.Is(err, errVersion) {
			fmt.Println(buildinfo.String("notify-send"))
			return 0, nil
		}
		return 1, err
	}

	endpoint, token := relayTarget()
	resp, err := send(endpoint, token, parsed.Request)
	if err != nil {
		if os.Getenv("NOTIFY_SEND_FALLBACK") == "1" {
			return fallbackNotifySend(args)
		}
		return 1, err
	}

	if parsed.Request.PrintID {
		fmt.Println(resp.ID)
	}
	if parsed.Request.Wait {
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

	return 0, nil
}

var (
	errHelp    = errors.New("help requested")
	errVersion = errors.New("version requested")
)

type parsedArgs struct {
	Request protocol.NotifyRequest
	Summary string
	Body    string
}

func parseArgs(args []string) (parsedArgs, error) {
	req := protocol.NotifyRequest{
		AppName:       "notify-send",
		ExpireTimeout: -1,
	}

	var positional []string
	actionIndex := 0
	for i := 0; i < len(args); i++ {
		arg, inlineValue, hasInlineValue := splitInlineValue(args[i])
		if len(positional) > 0 && !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		switch {
		case arg == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case arg == "-u" || arg == "--urgency":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			urgency, err := urgencyValue(value)
			if err != nil {
				return parsedArgs{}, err
			}
			req.Hints = append(req.Hints, protocol.Hint{Name: "urgency", Type: "byte", Value: urgency})
		case arg == "-t" || arg == "--expire-time":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			v, err := strconv.ParseInt(value, 10, 32)
			if err != nil {
				return parsedArgs{}, fmt.Errorf("invalid expire time %q", value)
			}
			req.ExpireTimeout = int32(v)
		case arg == "-i" || arg == "--icon":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			req.AppIcon = value
		case arg == "-c" || arg == "--category":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			req.Hints = append(req.Hints, protocol.Hint{Name: "category", Type: "string", Value: value})
		case arg == "-h" || arg == "--hint":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			hint, err := parseHint(value)
			if err != nil {
				return parsedArgs{}, err
			}
			req.Hints = append(req.Hints, hint)
		case arg == "-a" || arg == "--app-name":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			req.AppName = value
		case arg == "-r" || arg == "--replace-id":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			v, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return parsedArgs{}, fmt.Errorf("invalid replace id %q", value)
			}
			req.ReplacesID = uint32(v)
		case arg == "-p" || arg == "--print-id":
			req.PrintID = true
		case arg == "-w" || arg == "--wait":
			req.Wait = true
		case arg == "-e" || arg == "--transient":
			req.Hints = append(req.Hints, protocol.Hint{Name: "transient", Type: "bool", Value: "true"})
		case arg == "--help":
			return parsedArgs{}, errHelp
		case arg == "--version":
			return parsedArgs{}, errVersion
		case arg == "-A" || arg == "--action":
			value, next, err := needValue(args, i, inlineValue, hasInlineValue)
			if err != nil {
				return parsedArgs{}, err
			}
			i = next
			key, label := parseAction(value, actionIndex)
			actionIndex++
			req.Actions = append(req.Actions, key, label)
		case strings.HasPrefix(arg, "-"):
			return parsedArgs{}, fmt.Errorf("unsupported flag %q", arg)
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) == 0 {
		return parsedArgs{}, errors.New("missing summary")
	}
	req.Summary = positional[0]
	if len(positional) > 1 {
		req.Body = positional[1]
	}
	if len(positional) > 2 {
		return parsedArgs{}, errors.New("too many positional arguments")
	}

	return parsedArgs{Request: req, Summary: req.Summary, Body: req.Body}, nil
}

func splitInlineValue(arg string) (string, string, bool) {
	if !strings.HasPrefix(arg, "--") {
		return arg, "", false
	}
	name, value, ok := strings.Cut(arg, "=")
	if !ok {
		return arg, "", false
	}
	return name, value, true
}

func needValue(args []string, index int, inlineValue string, hasInlineValue bool) (string, int, error) {
	if hasInlineValue {
		return inlineValue, index, nil
	}
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("missing value for %q", args[index])
	}
	return args[index+1], index + 1, nil
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
		return protocol.Hint{}, fmt.Errorf("invalid hint %q", raw)
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
	// Check for socket path first
	if socketPath := os.Getenv("NOTIFY_RELAY_SOCKET"); socketPath != "" {
		return socketPath, ""
	}
	if runtime.GOOS == "linux" {
		socketPath := fmt.Sprintf("/run/user/%d/notify-relay.sock", os.Getuid())
		if _, err := os.Stat(socketPath); err == nil {
			return socketPath, ""
		}
	}

	// Fall back to TCP
	endpoint := os.Getenv("NOTIFY_RELAY_URL")
	if endpoint == "" {
		endpoint = "127.0.0.1:8787"
	}
	token := os.Getenv("NOTIFY_RELAY_TOKEN")
	return endpoint, token
}

func send(endpoint, token string, req protocol.NotifyRequest) (protocol.NotifyResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Determine if this is a Unix socket or TCP
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))

	var conn *grpc.ClientConn
	var err error

	// Check if endpoint looks like a Unix socket path
	if strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, ".") {
		// Unix socket
		conn, err = grpc.DialContext(ctx, "passthrough:///unix://"+endpoint, opts...)
	} else {
		// TCP
		if !strings.Contains(endpoint, ":") {
			endpoint = endpoint + ":8787"
		}
		conn, err = grpc.DialContext(ctx, endpoint, opts...)
	}

	if err != nil {
		return protocol.NotifyResponse{}, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	// Create client
	client := notify_relayv1.NewRelayServiceClient(conn)

	// Build gRPC request
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

	// Convert hints to map
	grpcReq.Hints = make(map[string]string)
	for _, hint := range req.Hints {
		grpcReq.Hints[hint.Name] = hint.Value
	}

	// Add auth token if provided
	if token != "" {
		// gRPC metadata for auth
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

func printHelp() {
	fmt.Println(`Usage: notify-send-proxy [OPTION...] <SUMMARY> [BODY]

Drop-in notify-send proxy for a remote notification relay.

Environment:
  NOTIFY_RELAY_SOCKET Relay Unix socket path
  NOTIFY_RELAY_URL    Relay host:port (default: 127.0.0.1:8787)
  NOTIFY_RELAY_TOKEN  Bearer token for authentication
  NOTIFY_SEND_FALLBACK=1  Fall back to local notify-send if relay fails

Supported options:
  -u, --urgency=LEVEL      low, normal, critical
  -t, --expire-time=TIME   timeout in milliseconds
  -i, --icon=ICON          icon path or icon name
  -c, --category=TYPE      notification category hint
  -h, --hint=TYPE:NAME:VAL custom typed hint
  -a, --app-name=APP       application name
  -r, --replace-id=ID      replace an existing notification
  -A, --action=[NAME=]TXT  add an action button
  -p, --print-id           print the notification id
  -w, --wait               wait for close or action signals
  -e, --transient          set transient=true hint
      --help               show this help
      --version            show version`)
}

func init() {
	flag.CommandLine.SetOutput(io.Discard)
}
