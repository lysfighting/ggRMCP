package grpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lysfighting/ggRMCP/types"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// reflectionClient implements ReflectionClient interface
type reflectionClient struct {
	conn   *grpc.ClientConn
	client grpc_reflection_v1alpha.ServerReflectionClient
	logger *zap.Logger

	// Cache for resolved file descriptors
	fdCache map[string]*descriptorpb.FileDescriptorProto
	mu      sync.RWMutex
}

// NewReflectionClient creates a new reflection client
func NewReflectionClient(conn *grpc.ClientConn, logger *zap.Logger) ReflectionClient {
	return &reflectionClient{
		conn:    conn,
		client:  grpc_reflection_v1alpha.NewServerReflectionClient(conn),
		logger:  logger,
		fdCache: make(map[string]*descriptorpb.FileDescriptorProto),
	}
}

type MethodInfo = types.MethodInfo
type SourceLocation = types.SourceLocation

// DiscoverMethods discovers all available gRPC methods
func (r *reflectionClient) DiscoverMethods(ctx context.Context) ([]types.MethodInfo, error) {
	r.logger.Info("Starting method discovery via gRPC reflection")

	// Get list of services
	serviceNames, err := r.listServices(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list services: %w", err)
	}

	r.logger.Info("Found services", zap.Strings("services", serviceNames))

	// Filter out internal gRPC services
	filteredServices := r.filterInternalServices(serviceNames)
	r.logger.Info("Filtered services",
		zap.Strings("originalServices", serviceNames),
		zap.Strings("filteredServices", filteredServices))

	// Group services by file descriptor to avoid redundant lookups
	fileDescriptorMap := make(map[string]*descriptorpb.FileDescriptorProto)
	serviceToFileMap := make(map[string]string)

	// Get file descriptors for all services
	for _, serviceName := range filteredServices {
		fileDescriptor, err := r.getFileDescriptorBySymbol(ctx, serviceName)
		if err != nil {
			r.logger.Error("Failed to get file descriptor for service",
				zap.String("service", serviceName),
				zap.Error(err))
			continue
		}

		fileName := fileDescriptor.GetName()
		if fileName == "" {
			fileName = serviceName // fallback to service name if no file name
		}

		// Only add to map if we haven't seen this file before
		if _, exists := fileDescriptorMap[fileName]; !exists {
			fileDescriptorMap[fileName] = fileDescriptor
		}
		serviceToFileMap[serviceName] = fileName
	}

	// Process all methods from each file descriptor
	var methods []types.MethodInfo

	for fileName, fileDescriptor := range fileDescriptorMap {
		r.logger.Info("Processing file descriptor", zap.String("file", fileName))

		// Extract all methods from this file descriptor
		fileMethods := r.extractMethodsFromFileDescriptor(ctx, fileDescriptor, filteredServices)
		methods = append(methods, fileMethods...)
	}

	r.logger.Info("Successfully discovered methods", zap.Int("count", len(methods)))
	return methods, nil
}

// listServices gets the list of all available services
func (r *reflectionClient) listServices(ctx context.Context) ([]string, error) {
	stream, err := r.client.ServerReflectionInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create reflection stream: %w", err)
	}
	defer func() {
		if closeErr := stream.CloseSend(); closeErr != nil {
			r.logger.Warn("Failed to close reflection stream", zap.Error(closeErr))
		}
	}()

	// Request service list
	req := &grpc_reflection_v1alpha.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1alpha.ServerReflectionRequest_ListServices{
			ListServices: "",
		},
	}

	if sendErr := stream.Send(req); sendErr != nil {
		return nil, fmt.Errorf("failed to send list services request: %w", sendErr)
	}

	resp, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("failed to receive list services response: %w", err)
	}

	listServicesResp := resp.GetListServicesResponse()
	if listServicesResp == nil {
		return nil, fmt.Errorf("received invalid response type")
	}

	var serviceNames []string
	for _, service := range listServicesResp.Service {
		serviceNames = append(serviceNames, service.Name)
	}

	return serviceNames, nil
}

// extractMethodsFromFileDescriptor extracts all methods from a file descriptor
func (r *reflectionClient) extractMethodsFromFileDescriptor(ctx context.Context, fileDescriptor *descriptorpb.FileDescriptorProto, targetServices []string) []types.MethodInfo {
	var methods []types.MethodInfo

	// Create a map of target services for quick lookup
	targetServiceMap := make(map[string]bool)
	for _, serviceName := range targetServices {
		targetServiceMap[serviceName] = true
	}

	// Extract all methods from the file descriptor
	for _, service := range fileDescriptor.Service {
		// Construct the full service name
		packageName := fileDescriptor.GetPackage()
		var fullServiceName string
		if packageName != "" {
			fullServiceName = packageName + "." + service.GetName()
		} else {
			fullServiceName = service.GetName()
		}

		// Only process if this service is in our target list
		if !targetServiceMap[fullServiceName] {
			continue
		}

		r.logger.Debug("Processing service from file descriptor",
			zap.String("serviceName", fullServiceName),
			zap.String("simpleServiceName", service.GetName()))

		// Extract method information directly into flat list
		for _, method := range service.Method {
			methodInfo, err := r.createMethodInfoWithServiceContext(ctx, fullServiceName, service, method, fileDescriptor)
			if err != nil {
				r.logger.Error("Failed to create method info",
					zap.String("service", fullServiceName),
					zap.String("method", method.GetName()),
					zap.Error(err))
				continue
			}
			methods = append(methods, methodInfo)
		}
	}

	return methods
}

// getFileDescriptorBySymbol gets a file descriptor by symbol name
func (r *reflectionClient) getFileDescriptorBySymbol(ctx context.Context, symbol string) (*descriptorpb.FileDescriptorProto, error) {
	// Check cache first
	r.mu.RLock()
	if fd, exists := r.fdCache[symbol]; exists {
		r.mu.RUnlock()
		return fd, nil
	}
	r.mu.RUnlock()

	stream, err := r.client.ServerReflectionInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create reflection stream: %w", err)
	}
	defer func() {
		if closeErr := stream.CloseSend(); closeErr != nil {
			r.logger.Warn("Failed to close reflection stream", zap.Error(closeErr))
		}
	}()

	req := &grpc_reflection_v1alpha.ServerReflectionRequest{
		MessageRequest: &grpc_reflection_v1alpha.ServerReflectionRequest_FileContainingSymbol{
			FileContainingSymbol: symbol,
		},
	}

	if sendErr := stream.Send(req); sendErr != nil {
		return nil, fmt.Errorf("failed to send file containing symbol request: %w", sendErr)
	}

	resp, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("failed to receive file containing symbol response: %w", err)
	}

	fileDescResp := resp.GetFileDescriptorResponse()
	if fileDescResp == nil {
		return nil, fmt.Errorf("received invalid response type")
	}

	if len(fileDescResp.FileDescriptorProto) == 0 {
		return nil, fmt.Errorf("no file descriptor found for symbol %s", symbol)
	}

	// Parse the file descriptor
	var fileDescriptor descriptorpb.FileDescriptorProto
	if err := proto.Unmarshal(fileDescResp.FileDescriptorProto[0], &fileDescriptor); err != nil {
		return nil, fmt.Errorf("failed to unmarshal file descriptor: %w", err)
	}

	// Cache the result by both symbol and file name
	r.mu.Lock()
	r.fdCache[symbol] = &fileDescriptor
	if fileName := fileDescriptor.GetName(); fileName != "" {
		r.fdCache[fileName] = &fileDescriptor
	}
	r.mu.Unlock()

	return &fileDescriptor, nil
}

// createMethodInfoWithServiceContext creates a MethodInfo with service context included
func (r *reflectionClient) createMethodInfoWithServiceContext(ctx context.Context, serviceName string, service *descriptorpb.ServiceDescriptorProto, method *descriptorpb.MethodDescriptorProto, fileDescriptor *descriptorpb.FileDescriptorProto) (types.MethodInfo, error) {
	// Create basic method info
	methodInfo := types.MethodInfo{
		Name:              method.GetName(),
		FullName:          fmt.Sprintf("%s.%s", serviceName, method.GetName()),
		ServiceName:       serviceName,
		InputType:         method.GetInputType(),
		OutputType:        method.GetOutputType(),
		IsClientStreaming: method.GetClientStreaming(),
		IsServerStreaming: method.GetServerStreaming(),
		FileDescriptor:    fileDescriptor,
	}

	// Generate tool name
	methodInfo.ToolName = methodInfo.GenerateToolName()

	// Add service description if available
	if service.GetOptions() != nil {
		// Extract service-level comments and options if needed
		// This could be enhanced to parse service-level documentation
	}

	// Resolve input and output descriptors from file descriptor
	inputDescriptor, err := r.resolveMessageDescriptor(method.GetInputType(), fileDescriptor)
	if err != nil {
		return types.MethodInfo{}, fmt.Errorf("failed to resolve input descriptor for %s: %w", method.GetInputType(), err)
	}
	methodInfo.InputDescriptor = inputDescriptor

	outputDescriptor, err := r.resolveMessageDescriptor(method.GetOutputType(), fileDescriptor)
	if err != nil {
		return types.MethodInfo{}, fmt.Errorf("failed to resolve output descriptor for %s: %w", method.GetOutputType(), err)
	}
	methodInfo.OutputDescriptor = outputDescriptor

	return methodInfo, nil
}

// resolveMessageDescriptor resolves a message descriptor from type name and file descriptor
func (r *reflectionClient) resolveMessageDescriptor(typeName string, fileDescriptor *descriptorpb.FileDescriptorProto) (protoreflect.MessageDescriptor, error) {
	// Remove leading dot if present
	typeName = strings.TrimPrefix(typeName, ".")

	// Create a file descriptor using protodesc.NewFile
	// For dependency resolution, we can use the global registry as resolver
	fileDesc, err := protodesc.NewFile(fileDescriptor, protoregistry.GlobalFiles)
	if err != nil {
		return nil, fmt.Errorf("failed to create file descriptor: %w", err)
	}

	// Create a temporary registry to register this file descriptor
	files := &protoregistry.Files{}
	if regErr := files.RegisterFile(fileDesc); regErr != nil {
		// If registration fails, try to use the global registry
		r.logger.Warn("Failed to register file descriptor, using global registry", zap.Error(regErr))
	}

	// Find the message descriptor
	messageDesc, err := files.FindDescriptorByName(protoreflect.FullName(typeName))
	if err != nil {
		// Try global registry as fallback
		messageDesc, err = protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(typeName))
		if err != nil {
			return nil, fmt.Errorf("failed to find message descriptor for %s: %w", typeName, err)
		}
	}

	msgDesc, ok := messageDesc.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("descriptor for %s is not a message descriptor", typeName)
	}

	return msgDesc, nil
}

// InvokeMethod invokes a gRPC method dynamically with optional headers
func (r *reflectionClient) InvokeMethod(ctx context.Context, headers map[string]string, method MethodInfo, inputJSON string) (string, error) {
	// Add headers to context metadata if provided
	if len(headers) > 0 {
		for key, value := range headers {
			ctx = metadata.AppendToOutgoingContext(ctx, key, value)
		}
		r.logger.Debug("Forwarding headers to gRPC server",
			zap.String("method", method.FullName),
			zap.Int("headerCount", len(headers)))
	}

	r.logger.Debug("Starting dynamic method invocation",
		zap.String("method", method.FullName),
		zap.String("inputType", string(method.InputDescriptor.FullName())),
		zap.String("outputType", string(method.OutputDescriptor.FullName())),
		zap.String("inputJSON", inputJSON))

	// 1. Create dynamic input message
	inputMsg := dynamicpb.NewMessage(method.InputDescriptor)

	// 2. Parse JSON input into the dynamic message
	if inputJSON != "" && inputJSON != "{}" {
		if err := protojson.Unmarshal([]byte(inputJSON), inputMsg); err != nil {
			return "", fmt.Errorf("failed to parse input JSON: %w", err)
		}
	}

	r.logger.Debug("Created input message", zap.String("message", inputMsg.String()))

	// 3. Create dynamic output message
	outputMsg := dynamicpb.NewMessage(method.OutputDescriptor)

	// 4. Invoke the gRPC method using generic invoke
	// Convert method name to gRPC format: /package.Service/Method
	grpcMethodName := fmt.Sprintf("/%s/%s", method.FullName[:strings.LastIndex(method.FullName, ".")], method.Name)

	r.logger.Debug("Invoking gRPC method",
		zap.String("grpcMethodName", grpcMethodName),
		zap.String("originalFullName", method.FullName))

	err := r.conn.Invoke(ctx, grpcMethodName, inputMsg, outputMsg)
	if err != nil {
		return "", fmt.Errorf("gRPC call failed: %w", err)
	}

	r.logger.Debug("Received output message", zap.String("message", outputMsg.String()))

	// 5. Convert output to JSON
	outputJSON, err := protojson.Marshal(outputMsg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal output to JSON: %w", err)
	}

	r.logger.Debug("Method invocation successful",
		zap.String("method", method.FullName),
		zap.String("outputJSON", string(outputJSON)))

	return string(outputJSON), nil
}

// filterInternalServices filters out internal gRPC services
func (r *reflectionClient) filterInternalServices(services []string) []string {
	var filtered []string

	internalPrefixes := []string{
		"grpc.reflection.",
		"grpc.health.",
		"grpc.channelz.",
		"grpc.testing.",
	}

	for _, service := range services {
		isInternal := false
		for _, prefix := range internalPrefixes {
			if strings.HasPrefix(service, prefix) {
				isInternal = true
				break
			}
		}

		if !isInternal {
			filtered = append(filtered, service)
		}
	}

	return filtered
}

// getSimpleServiceName extracts simple service name from full name
func getSimpleServiceName(fullName string) string {
	parts := strings.Split(fullName, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return fullName
}

// Close closes the reflection client
func (r *reflectionClient) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// HealthCheck for the gRPC connection
func (r *reflectionClient) HealthCheck(ctx context.Context) error {
	// Create a context with timeout
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Try to list services as a health check
	_, err := r.listServices(ctx)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	return nil
}
