package grpc

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/lysfighting/ggRMCP/config"
	"github.com/lysfighting/ggRMCP/descriptors"
	"github.com/lysfighting/ggRMCP/types"
	"go.uber.org/zap"
)

// serviceDiscoverer implements ServiceDiscoverer interface
// Similar to Java ServiceDiscoverer - handles both reflection and file descriptor cases
type serviceDiscoverer struct {
	logger           *zap.Logger
	connManager      ConnectionManager
	reflectionClient ReflectionClient
	tools            atomic.Pointer[map[string]types.MethodInfo]

	// Method extraction components
	descriptorLoader *descriptors.Loader
	descriptorConfig config.DescriptorSetConfig

	// Configuration
	reconnectInterval    time.Duration
	maxReconnectAttempts int
}

// NewServiceDiscoverer creates a new service discoverer with descriptor support
func NewServiceDiscoverer(host string, port int, logger *zap.Logger, descriptorConfig config.DescriptorSetConfig) (ServiceDiscoverer, error) {
	baseConfig := ConnectionManagerConfig{
		Host:           host,
		Port:           port,
		ConnectTimeout: 5 * time.Second,
		KeepAlive: KeepAliveConfig{
			Time:                10 * time.Second,
			Timeout:             5 * time.Second,
			PermitWithoutStream: true,
		},
		MaxMessageSize: 4 * 1024 * 1024, // 4MB
	}

	connManager := NewConnectionManager(baseConfig, logger)

	d := &serviceDiscoverer{
		logger:               logger.Named("discovery"),
		connManager:          connManager,
		descriptorLoader:     descriptors.NewLoader(logger),
		descriptorConfig:     descriptorConfig,
		reconnectInterval:    5 * time.Second,
		maxReconnectAttempts: 5,
	}

	// Initialize with empty tools map
	emptyMap := make(map[string]types.MethodInfo)
	d.tools.Store(&emptyMap)

	return d, nil
}

// Connect establishes connection to the gRPC server
func (d *serviceDiscoverer) Connect(ctx context.Context) error {
	d.logger.Info("Connecting to gRPC server via connection manager")

	// Use connection manager to establish connection
	if err := d.connManager.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect via connection manager: %w", err)
	}

	// Create reflection client with the managed connection
	conn := d.connManager.GetConnection()
	if conn == nil {
		return fmt.Errorf("connection manager returned nil connection")
	}

	d.reflectionClient = NewReflectionClient(conn, d.logger)

	// Verify connection with health check
	if err := d.reflectionClient.HealthCheck(ctx); err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	d.logger.Info("Successfully connected to gRPC server")
	return nil
}

// DiscoverServices discovers all available gRPC services
func (d *serviceDiscoverer) DiscoverServices(ctx context.Context) error {
	if d.reflectionClient == nil {
		return fmt.Errorf("not connected to gRPC server")
	}

	d.logger.Info("Starting service discovery")

	var methods []types.MethodInfo
	var err error

	// Try FileDescriptorSet first if enabled and available
	if d.descriptorConfig.Enabled && d.descriptorConfig.Path != "" {
		methods, err = d.discoverFromFileDescriptor()
		if err == nil {
			d.logger.Info("Successfully discovered services from FileDescriptorSet")
		} else {
			d.logger.Warn("Failed to discover from FileDescriptorSet, falling back to reflection",
				zap.Error(err))
			methods = nil
		}
	}

	// Use reflection discovery if FileDescriptorSet failed or wasn't enabled
	if methods == nil {
		methods, err = d.discoverFromReflection(ctx)
		if err != nil {
			return err
		}
	}

	// Set the discovered tools
	tools := make(map[string]types.MethodInfo)
	for _, method := range methods {
		tools[method.ToolName] = method
	}
	d.tools.Store(&tools)

	return nil
}

// discoverFromFileDescriptor discovers services from FileDescriptorSet
func (d *serviceDiscoverer) discoverFromFileDescriptor() ([]types.MethodInfo, error) {
	d.logger.Info("Discovering services from FileDescriptorSet", zap.String("path", d.descriptorConfig.Path))

	// Load FileDescriptorSet
	fdSet, err := d.descriptorLoader.LoadFromFile(d.descriptorConfig.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to load descriptor set: %w", err)
	}

	// Build registry
	files, err := d.descriptorLoader.BuildRegistry(fdSet)
	if err != nil {
		return nil, fmt.Errorf("failed to build file registry: %w", err)
	}

	// Extract methods directly from file descriptors
	methods, err := d.descriptorLoader.ExtractMethodInfo(files)
	if err != nil {
		return nil, fmt.Errorf("failed to extract method info: %w", err)
	}

	d.logger.Info("FileDescriptorSet discovery completed", zap.Int("methodCount", len(methods)))
	return methods, nil
}

// discoverFromReflection discovers services from reflection
func (d *serviceDiscoverer) discoverFromReflection(ctx context.Context) ([]types.MethodInfo, error) {
	d.logger.Info("Discovering services from reflection")

	methods, err := d.reflectionClient.DiscoverMethods(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to discover services via reflection: %w", err)
	}

	d.logger.Info("Reflection discovery completed", zap.Int("methodCount", len(methods)))
	return methods, nil
}

// GetMethods returns all discovered methods
func (d *serviceDiscoverer) GetMethods() []types.MethodInfo {
	tools := d.tools.Load()
	if tools == nil {
		return []types.MethodInfo{}
	}

	// Build methods slice from tools map
	methods := make([]types.MethodInfo, 0, len(*tools))
	for _, method := range *tools {
		methods = append(methods, method)
	}

	return methods
}

// Reconnect attempts to reconnect to the gRPC server
func (d *serviceDiscoverer) Reconnect(ctx context.Context) error {
	d.logger.Info("Attempting to reconnect to gRPC server")

	var lastErr error
	for i := 0; i < d.maxReconnectAttempts; i++ {
		if i > 0 {
			d.logger.Info("Reconnect attempt",
				zap.Int("attempt", i+1),
				zap.Int("maxAttempts", d.maxReconnectAttempts))

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d.reconnectInterval):
			}
		}

		// Use connection manager to reconnect
		if err := d.connManager.Reconnect(ctx); err != nil {
			lastErr = err
			d.logger.Warn("Reconnect attempt failed",
				zap.Int("attempt", i+1),
				zap.Error(err))
			continue
		}

		// Recreate reflection client with new connection
		conn := d.connManager.GetConnection()
		if conn == nil {
			lastErr = fmt.Errorf("connection manager returned nil connection after reconnect")
			continue
		}
		d.reflectionClient = NewReflectionClient(conn, d.logger)

		// Rediscover services after reconnection
		if err := d.DiscoverServices(ctx); err != nil {
			lastErr = err
			d.logger.Warn("Service rediscovery failed",
				zap.Int("attempt", i+1),
				zap.Error(err))
			continue
		}

		d.logger.Info("Successfully reconnected to gRPC server")
		return nil
	}

	return fmt.Errorf("failed to reconnect after %d attempts: %w", d.maxReconnectAttempts, lastErr)
}

// isConnected checks if the discoverer is connected (private helper)
func (d *serviceDiscoverer) isConnected() bool {
	return d.connManager.IsConnected() && d.reflectionClient != nil
}

// HealthCheck performs a health check
func (d *serviceDiscoverer) HealthCheck(ctx context.Context) error {
	// Check connection manager health first
	if err := d.connManager.HealthCheck(ctx); err != nil {
		return fmt.Errorf("connection manager health check failed: %w", err)
	}

	if d.reflectionClient == nil {
		return fmt.Errorf("reflection client not initialized")
	}

	return d.reflectionClient.HealthCheck(ctx)
}

// Close closes the service discoverer
func (d *serviceDiscoverer) Close() error {
	if d.reflectionClient != nil {
		if err := d.reflectionClient.Close(); err != nil {
			d.logger.Error("Failed to close reflection client", zap.Error(err))
		}
		d.reflectionClient = nil
	}

	// Close connection manager
	if err := d.connManager.Close(); err != nil {
		d.logger.Error("Failed to close connection manager", zap.Error(err))
	}

	// Reset tools to empty map
	emptyMap := make(map[string]types.MethodInfo)
	d.tools.Store(&emptyMap)

	d.logger.Info("Service discoverer closed")
	return nil
}

// GetServiceCount returns the number of discovered services
func (d *serviceDiscoverer) GetServiceCount() int {
	tools := d.tools.Load()
	if tools == nil {
		return 0
	}

	serviceNames := make(map[string]bool)
	for _, method := range *tools {
		serviceNames[method.ServiceName] = true
	}

	return len(serviceNames)
}

// GetMethodCount returns the total number of methods across all services
func (d *serviceDiscoverer) GetMethodCount() int {
	tools := d.tools.Load()
	if tools == nil {
		return 0
	}
	return len(*tools)
}

// GetServiceStats returns statistics about discovered services
func (d *serviceDiscoverer) GetServiceStats() map[string]interface{} {
	tools := d.tools.Load()
	if tools == nil {
		stats := map[string]interface{}{
			"serviceCount": 0,
			"methodCount":  0,
			"isConnected":  d.isConnected(),
			"services":     []string{},
		}
		return stats
	}

	serviceNames := make(map[string]bool)
	for _, method := range *tools {
		serviceNames[method.ServiceName] = true
	}

	serviceList := make([]string, 0, len(serviceNames))
	for name := range serviceNames {
		serviceList = append(serviceList, name)
	}

	stats := map[string]interface{}{
		"serviceCount": len(serviceNames),
		"methodCount":  len(*tools),
		"isConnected":  d.isConnected(),
		"services":     serviceList,
	}

	return stats
}

// getMethodByTool returns information about a method by its tool name (private helper)
func (d *serviceDiscoverer) getMethodByTool(toolName string) (types.MethodInfo, bool) {
	tools := d.tools.Load()
	if tools == nil {
		return types.MethodInfo{}, false
	}
	method, exists := (*tools)[toolName]
	return method, exists
}

// InvokeMethodByTool invokes a gRPC method by tool name with optional headers
func (d *serviceDiscoverer) InvokeMethodByTool(ctx context.Context, headers map[string]string, toolName string, inputJSON string) (string, error) {
	// Get method info by tool name
	method, exists := d.getMethodByTool(toolName)
	if !exists {
		return "", fmt.Errorf("tool %s not found", toolName)
	}

	// Check for streaming methods (not supported in this implementation)
	if method.IsClientStreaming || method.IsServerStreaming {
		return "", fmt.Errorf("streaming methods are not supported")
	}

	if d.reflectionClient == nil {
		return "", fmt.Errorf("not connected to gRPC server")
	}

	d.logger.Debug("Invoking gRPC method by tool",
		zap.String("toolName", toolName),
		zap.String("service", method.FullName),
		zap.Int("headerCount", len(headers)),
		zap.String("input", inputJSON))

	// Invoke the method through the reflection client
	result, err := d.reflectionClient.InvokeMethod(ctx, headers, method, inputJSON)
	if err != nil {
		return "", fmt.Errorf("failed to invoke method: %w", err)
	}

	return result, nil
}

// newServiceDiscovererWithConnManager creates a service discoverer with a custom connection manager (for testing)
func newServiceDiscovererWithConnManager(connManager ConnectionManager, logger *zap.Logger) *serviceDiscoverer {
	d := &serviceDiscoverer{
		logger:               logger.Named("discovery"),
		connManager:          connManager,
		descriptorLoader:     descriptors.NewLoader(logger),
		descriptorConfig:     config.DescriptorSetConfig{},
		reconnectInterval:    5 * time.Second,
		maxReconnectAttempts: 5,
	}

	// Initialize with empty tools map
	emptyMap := make(map[string]types.MethodInfo)
	d.tools.Store(&emptyMap)

	return d
}
