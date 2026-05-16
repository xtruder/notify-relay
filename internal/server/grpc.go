package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/xtruder/notify-relay/internal/p2p"
	notify_relayv1 "github.com/xtruder/notify-relay/internal/proto/notify_relay/v1"
	"github.com/xtruder/notify-relay/internal/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NotificationRouter is the interface the gRPC server uses to route notifications.
type NotificationRouter interface {
	Notify(ctx context.Context, req protocol.NotifyRequest) (protocol.NotifyResponse, error)
	CloseNotification(ctx context.Context, id uint32) error
	Capabilities(ctx context.Context) ([]string, error)
	ServerInformation(ctx context.Context) (protocol.ServerInfoResponse, error)
}

// GRPCServer implements the gRPC RelayService for the local proxy API.
type GRPCServer struct {
	notify_relayv1.UnimplementedRelayServiceServer

	router     NotificationRouter
	peerTable  *p2p.PeerTable
	config     Config
	grpcServer *grpc.Server
	listener   net.Listener
	mu         sync.RWMutex
}

// NewGRPCServer creates a new gRPC server. peerTable may be nil if peer
// listing should be unavailable.
func NewGRPCServer(cfg Config, r NotificationRouter, peerTable *p2p.PeerTable) (*GRPCServer, error) {
	s := &GRPCServer{
		config:    cfg,
		router:    r,
		peerTable: peerTable,
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
	} else if cfg.Listen != "" {
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return nil, fmt.Errorf("listen on %s: %w", cfg.Listen, err)
		}
		s.listener = ln
	} else {
		return nil, fmt.Errorf("no listen address or unix socket configured")
	}

	return s, nil
}

// Serve starts the gRPC server
func (s *GRPCServer) Serve() error {
	slog.Info("notify-relayd gRPC listening", "address", s.listener.Addr())
	return s.grpcServer.Serve(s.listener)
}

// Address returns the server address (for tests)
func (s *GRPCServer) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Shutdown gracefully stops the server
func (s *GRPCServer) Shutdown(ctx context.Context) error {
	s.grpcServer.GracefulStop()

	if s.config.Unix != "" {
		if err := os.Remove(s.config.Unix); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to remove socket file", "error", err)
		}
	}

	return nil
}

// Notify handles incoming notification requests from proxy
func (s *GRPCServer) Notify(ctx context.Context, req *notify_relayv1.Notification) (*notify_relayv1.NotificationResponse, error) {
	protocolReq := protoToProtocol(req)

	resp, err := s.router.Notify(ctx, protocolReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "notify failed: %v", err)
	}

	return protocolToProto(resp), nil
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

// Connect is no longer used for remote relay (p2p handles that).
// Kept to satisfy the proto interface.
func (s *GRPCServer) Connect(stream notify_relayv1.RelayService_ConnectServer) error {
	return status.Errorf(codes.Unimplemented, "use p2p for remote relay")
}

// ListPeers returns the current p2p peer table snapshot.
func (s *GRPCServer) ListPeers(ctx context.Context, req *notify_relayv1.ListPeersRequest) (*notify_relayv1.ListPeersResponse, error) {
	if s.peerTable == nil {
		return nil, status.Errorf(codes.Unavailable, "peer table not available")
	}

	resp := &notify_relayv1.ListPeersResponse{}
	if local, ok := s.peerTable.GetLocal(); ok {
		resp.Peers = append(resp.Peers, peerInfoToProto(local, true))
	}
	for _, p := range s.peerTable.GetAll() {
		resp.Peers = append(resp.Peers, peerInfoToProto(p, false))
	}
	return resp, nil
}

func peerInfoToProto(info p2p.PeerInfo, isLocal bool) *notify_relayv1.Peer {
	return &notify_relayv1.Peer{
		PeerId:           info.PeerID.String(),
		Hostname:         info.Hostname,
		IsPresent:        info.IsPresent,
		Priority:         int32(info.Priority),
		LastSeenUnixNano: info.LastSeen.UnixNano(),
		IsLocal:          isLocal,
	}
}

// unaryAuthInterceptor checks authentication for unary calls
func (s *GRPCServer) unaryAuthInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if s.config.Token == "" {
		return handler(ctx, req)
	}

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
		result = append(result, protocol.Hint{
			Name:  name,
			Type:  "string",
			Value: value,
		})
	}
	return result
}
