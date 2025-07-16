package grpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	grpcLib "google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// connectionManager implements ConnectionManager interface
type connectionManager struct {
	config ConnectionManagerConfig
	logger *zap.Logger

	mu   sync.RWMutex
	conn *grpcLib.ClientConn
}

// NewConnectionManager creates a new connection manager
func NewConnectionManager(config ConnectionManagerConfig, logger *zap.Logger) ConnectionManager {
	return &connectionManager{
		config: config,
		logger: logger.Named("connection"),
	}
}

// Connect establishes a connection to the gRPC server
func (cm *connectionManager) Connect(ctx context.Context) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Close existing connection if any
	if cm.conn != nil {
		_ = cm.conn.Close()
	}

	target := fmt.Sprintf("%s:%d", cm.config.Host, cm.config.Port)
	cm.logger.Info("Connecting to gRPC server", zap.String("target", target))

	// Configure connection options
	opts := []grpcLib.DialOption{
		grpcLib.WithTransportCredentials(insecure.NewCredentials()),
		grpcLib.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cm.config.KeepAlive.Time,
			Timeout:             cm.config.KeepAlive.Timeout,
			PermitWithoutStream: cm.config.KeepAlive.PermitWithoutStream,
		}),
		grpcLib.WithDefaultCallOptions(
			grpcLib.MaxCallRecvMsgSize(cm.config.MaxMessageSize),
			grpcLib.MaxCallSendMsgSize(cm.config.MaxMessageSize),
		),
	}

	// Create context with timeout
	connectCtx, cancel := context.WithTimeout(ctx, cm.config.ConnectTimeout)
	defer cancel()

	conn, err := grpcLib.DialContext(connectCtx, target, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to gRPC server: %w", err)
	}

	cm.conn = conn

	// Verify connection with health check
	if err := cm.healthCheckLocked(ctx); err != nil {
		_ = cm.conn.Close()
		cm.conn = nil
		return fmt.Errorf("health check failed: %w", err)
	}

	cm.logger.Info("Successfully connected to gRPC server")
	return nil
}

// GetConnection returns the current connection
func (cm *connectionManager) GetConnection() *grpcLib.ClientConn {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.conn
}

// IsConnected checks if the connection is healthy
func (cm *connectionManager) IsConnected() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.conn == nil {
		return false
	}

	state := cm.conn.GetState()
	return state == connectivity.Ready || state == connectivity.Idle
}

// Reconnect attempts to reconnect to the server
func (cm *connectionManager) Reconnect(ctx context.Context) error {
	cm.logger.Info("Attempting to reconnect to gRPC server")
	return cm.Connect(ctx)
}

// HealthCheck performs a health check on the connection
func (cm *connectionManager) HealthCheck(ctx context.Context) error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.healthCheckLocked(ctx)
}

// healthCheckLocked performs health check without acquiring mutex (caller must hold lock)
func (cm *connectionManager) healthCheckLocked(ctx context.Context) error {
	if cm.conn == nil {
		return fmt.Errorf("no connection available")
	}

	// Check connection state
	state := cm.conn.GetState()
	if state == connectivity.TransientFailure || state == connectivity.Shutdown {
		return fmt.Errorf("connection is in unhealthy state: %v", state)
	}

	// Try to wait for connection to be ready
	if state != connectivity.Ready {
		healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		if !cm.conn.WaitForStateChange(healthCtx, state) {
			return fmt.Errorf("connection state did not change within timeout")
		}

		if cm.conn.GetState() != connectivity.Ready {
			return fmt.Errorf("connection failed to become ready")
		}
	}

	return nil
}

// Close closes the connection
func (cm *connectionManager) Close() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.conn != nil {
		err := cm.conn.Close()
		cm.conn = nil
		if err != nil {
			cm.logger.Error("Failed to close gRPC connection", zap.Error(err))
			return err
		}
		cm.logger.Info("gRPC connection closed")
	}

	return nil
}
