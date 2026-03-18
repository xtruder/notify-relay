package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/xtruder/notify-relay/internal/channel"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/protocol"
	"github.com/xtruder/notify-relay/internal/remotes"
	"github.com/xtruder/notify-relay/internal/router"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GRPCServer implements the gRPC RelayService
type GRPCServer struct {
	notify_relayv1.UnimplementedRelayServiceServer

	router       *router.Router
	manager      *remotes.Manager
	remoteServer *remotes.Server
	config       Config
	grpcServer   *grpc.Server
	listener     net.Listener
	mu           sync.RWMutex
	token        string
}

// NewGRPCServer creates a new gRPC server
func NewGRPCServer(cfg Config, r *router.Router) (*GRPCServer, error) {
	s := &GRPCServer{
		config: cfg,
		router: r,
	}

	// Setup interceptor for authentication
	var opts []grpc.ServerOption
	opts = append(opts, grpc.UnaryInterceptor(s.unaryAuthInterceptor))
	opts = append(opts, grpc.StreamInterceptor(s.streamAuthInterceptor))

	s.grpcServer = grpc.NewServer(opts...)
	notify_relayv1.RegisterRelayServiceServer(s.grpcServer, s)

	// Create listener
	if cfg.Unix != "" {
		if err := os.RemoveAll(cfg.Unix); err != nil {
			return nil, fmt.Errorf("clear unix socket: %w", err)
		}
		ln, err := net.Listen("unix", cfg.Unix)
		if err != nil {
			return nil, fmt.Errorf("listen on unix socket: %w", err)
		}
		s.listener = ln
	} else {
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
		}
		s.listener = ln
	}

	return s, nil
}

// SetRemoteManager sets the remote client manager (for server mode)
func (s *GRPCServer) SetRemoteManager(m *remotes.Manager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manager = m
	if m != nil {
		s.remoteServer = remotes.NewServer(m)
	}
}

// Serve starts the gRPC server
func (s *GRPCServer) Serve() error {
	log.Printf("notify-relayd gRPC listening on %s", s.listener.Addr())
	if s.config.Token != "" {
		log.Printf("notify-relayd bearer token: %s", s.config.Token)
	}
	return s.grpcServer.Serve(s.listener)
}

// Shutdown gracefully stops the server
func (s *GRPCServer) Shutdown(ctx context.Context) error {
	s.grpcServer.GracefulStop()

	// Clean up Unix socket file if applicable
	if s.config.Unix != "" {
		if err := os.Remove(s.config.Unix); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove socket file: %v", err)
		}
	}

	return nil
}

// Notify handles incoming notification requests from proxy
func (s *GRPCServer) Notify(ctx context.Context, req *notify_relayv1.Notification) (*notify_relayv1.NotificationResponse, error) {
	// Convert proto to protocol request
	protocolReq := protoToProtocol(req)

	// Try local routing first
	resp, err := s.router.Notify(ctx, protocolReq)
	if err != nil {
		// Check if we should forward to remote
		if forwardErr, ok := err.(router.ErrForwardToRemote); ok {
			// Forward to remote client
			client, exists := s.manager.GetClient(forwardErr.Hostname)
			if !exists {
				return nil, status.Errorf(codes.Unavailable, "remote client %s disconnected", forwardErr.Hostname)
			}

			forwarded := &notify_relayv1.ForwardedNotification{
				SourceHostname: "server",
				Notification:   req,
			}

			_, err := s.manager.ForwardNotification(ctx, forwardErr.Hostname, forwarded)
			if err != nil {
				// Remote failed, fall back to local routing
				log.Printf("Remote %s failed, falling back to local: %v", forwardErr.Hostname, err)
				return s.routeLocally(ctx, protocolReq)
			}

			// Wait for client response
			select {
			case resp := <-client.ResponseChan:
				return &notify_relayv1.NotificationResponse{
					Id:        resp.Id,
					Event:     resp.Event,
					Reason:    resp.Reason,
					ActionKey: resp.ActionKey,
				}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
				return nil, status.Errorf(codes.DeadlineExceeded, "timeout waiting for remote response")
			}
		}

		return nil, status.Errorf(codes.Internal, "notify failed: %v", err)
	}

	return protocolToProto(resp), nil
}

// routeLocally routes a notification using only local channels (skips remote conditions)
func (s *GRPCServer) routeLocally(ctx context.Context, req protocol.NotifyRequest) (*notify_relayv1.NotificationResponse, error) {
	// Find first local route that matches (skip remote_* conditions)
	routes := []struct {
		condition string
		channel   string
	}{
		{"screen_locked", "phone"},
		{"always", "dbus"},
	}

	for _, route := range routes {
		switch route.condition {
		case "screen_locked":
			// Check lock state from router's condition evaluator
			// For now, just try the channel directly
			// TODO: Get lock state from condition evaluator
		case "always":
			// Always try this channel
		}

		ch, ok := s.getChannel(route.channel)
		if ok {
			resp, err := ch.Send(ctx, req)
			if err != nil {
				continue // Try next channel
			}
			return protocolToProto(resp), nil
		}
	}

	return nil, status.Errorf(codes.Internal, "no local route available")
}

func (s *GRPCServer) getChannel(name string) (channel.Channel, bool) {
	// This needs access to router channels
	// For now, we'll use the router's Notify but with modified behavior
	// TODO: Implement proper channel access
	return nil, false
}

// CloseNotification handles close requests
func (s *GRPCServer) CloseNotification(ctx context.Context, req *notify_relayv1.CloseRequest) (*notify_relayv1.CloseResponse, error) {
	err := s.router.CloseNotification(ctx, req.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "close failed: %v", err)
	}
	return &notify_relayv1.CloseResponse{Success: true}, nil
}

// GetCapabilities returns server capabilities
func (s *GRPCServer) GetCapabilities(ctx context.Context, req *notify_relayv1.CapabilitiesRequest) (*notify_relayv1.CapabilitiesResponse, error) {
	caps, err := s.router.Capabilities(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get capabilities failed: %v", err)
	}
	return &notify_relayv1.CapabilitiesResponse{Capabilities: caps}, nil
}

// GetServerInfo returns server information
func (s *GRPCServer) GetServerInfo(ctx context.Context, req *notify_relayv1.ServerInfoRequest) (*notify_relayv1.ServerInfoResponse, error) {
	info, err := s.router.ServerInformation(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get server info failed: %v", err)
	}
	return &notify_relayv1.ServerInfoResponse{
		Name:    info.Name,
		Vendor:  info.Vendor,
		Version: info.Version,
		Spec:    info.Spec,
	}, nil
}

// Health returns server health status
func (s *GRPCServer) Health(ctx context.Context, req *notify_relayv1.HealthRequest) (*notify_relayv1.HealthResponse, error) {
	return &notify_relayv1.HealthResponse{Status: "ok"}, nil
}

// Connect handles bidirectional streams from remote clients
func (s *GRPCServer) Connect(stream notify_relayv1.RelayService_ConnectServer) error {
	s.mu.RLock()
	remoteServer := s.remoteServer
	s.mu.RUnlock()

	if remoteServer == nil {
		return status.Errorf(codes.Unimplemented, "remote connections not enabled")
	}

	return remoteServer.HandleConnect(stream)
}

// unaryAuthInterceptor checks authentication for unary calls
func (s *GRPCServer) unaryAuthInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if s.config.Token == "" {
		return handler(ctx, req)
	}

	// Skip auth for health checks
	if info.FullMethod == "/notify_relay.v1.RelayService/Health" {
		return handler(ctx, req)
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	tokens := md.Get("authorization")
	if len(tokens) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "missing authorization")
	}

	token := tokens[0]
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}

	if token != s.config.Token {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token")
	}

	return handler(ctx, req)
}

// streamAuthInterceptor checks authentication for stream calls
func (s *GRPCServer) streamAuthInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if s.config.Token == "" {
		return handler(srv, stream)
	}

	ctx := stream.Context()
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	tokens := md.Get("authorization")
	if len(tokens) == 0 {
		return status.Errorf(codes.Unauthenticated, "missing authorization")
	}

	token := tokens[0]
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}

	if token != s.config.Token {
		return status.Errorf(codes.Unauthenticated, "invalid token")
	}

	return handler(srv, stream)
}

// Helper functions to convert between proto and protocol types

func protoToProtocol(req *notify_relayv1.Notification) protocol.NotifyRequest {
	return protocol.NotifyRequest{
		AppName:       req.AppName,
		ReplacesID:    req.ReplacesId,
		AppIcon:       req.AppIcon,
		Summary:       req.Summary,
		Body:          req.Body,
		Actions:       req.Actions,
		Hints:         hintsFromProto(req.Hints),
		ExpireTimeout: req.ExpireTimeout,
		Wait:          req.Wait,
		PrintID:       req.PrintId,
	}
}

func protocolToProto(resp protocol.NotifyResponse) *notify_relayv1.NotificationResponse {
	return &notify_relayv1.NotificationResponse{
		Id:        resp.ID,
		Event:     resp.Event,
		Reason:    resp.Reason,
		ActionKey: resp.ActionKey,
	}
}

func hintsFromProto(hints map[string]string) []protocol.Hint {
	result := make([]protocol.Hint, 0, len(hints))
	for name, value := range hints {
		// Default to string type for simplicity
		result = append(result, protocol.Hint{
			Name:  name,
			Type:  "string",
			Value: value,
		})
	}
	return result
}
