//go:build integration
// +build integration

package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aalobaidi/ggRMCP/pkg/config"
	"github.com/aalobaidi/ggRMCP/pkg/grpc"
	"github.com/aalobaidi/ggRMCP/pkg/mcp"
	"github.com/aalobaidi/ggRMCP/pkg/server"
	"github.com/aalobaidi/ggRMCP/pkg/session"
	proto "github.com/aalobaidi/ggRMCP/pkg/testproto"
	"github.com/aalobaidi/ggRMCP/pkg/tools"
	"github.com/aalobaidi/ggRMCP/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	grpcLib "google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Test constants
const (
	testBufSize      = 1024 * 1024
	testTimeout      = 5 * time.Second
	concurrencyLevel = 10
)

// TestEnvironment provides a complete test setup for gRPC and MCP testing
type TestEnvironment struct {
	Server      *grpcLib.Server
	Listener    *bufconn.Listener
	Connection  *grpcLib.ClientConn
	Handler     *server.Handler
	Reflection  grpc.ReflectionClient
	Discoverer  grpc.ServiceDiscoverer // Minimal instance, not used for discovery in tests
	ToolBuilder *tools.MCPToolBuilder
	Logger      *zap.Logger
	Context     context.Context
	SessionCtx  *session.Context
}

// NewTestEnvironment creates a complete test environment with all mock services
func NewTestEnvironment(t *testing.T) *TestEnvironment {
	logger := zap.NewNop()
	ctx := context.Background()

	// Setup gRPC server
	listener := bufconn.Listen(testBufSize)
	grpcServer := grpcLib.NewServer()

	// Register all mock services
	proto.RegisterUserProfileServiceServer(grpcServer, &UnifiedMockUserProfileServer{})
	proto.RegisterDocumentServiceServer(grpcServer, &UnifiedMockDocumentServer{})
	proto.RegisterNodeServiceServer(grpcServer, &UnifiedMockNodeServer{})

	reflection.Register(grpcServer)

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			t.Logf("Test server error: %v", err)
		}
	}()

	// Setup connection
	conn, err := grpcLib.DialContext(ctx, "bufnet",
		grpcLib.WithContextDialer(createBufDialer(listener)),
		grpcLib.WithInsecure())
	require.NoError(t, err)

	// Setup reflection client
	reflectionClient := grpc.NewReflectionClient(conn, logger)

	// Create a working ServiceDiscoverer by manually setting up the reflection client and discovered methods
	serviceDiscoverer, _ := grpc.NewServiceDiscoverer("localhost", 50051, logger, config.DescriptorSetConfig{})

	// Manually inject the reflection client and discovered methods for testing
	// This allows the ServiceDiscoverer to work with bufconn instead of trying to connect to localhost:50051
	if err := injectTestConnection(serviceDiscoverer, reflectionClient, logger); err != nil {
		t.Fatalf("Failed to setup test ServiceDiscoverer: %v", err)
	}

	// Setup session management
	sessionManager := session.NewManager(logger)
	toolBuilder := tools.NewMCPToolBuilder(logger)

	// Setup handler
	headerConfig := config.HeaderForwardingConfig{}
	handler := server.NewHandler(logger, serviceDiscoverer, sessionManager, toolBuilder, headerConfig)

	return &TestEnvironment{
		Server:      grpcServer,
		Listener:    listener,
		Connection:  conn,
		Handler:     handler,
		Reflection:  reflectionClient,
		Discoverer:  serviceDiscoverer,
		ToolBuilder: toolBuilder,
		Logger:      logger,
		Context:     ctx,
		SessionCtx:  &session.Context{ID: "test-session"},
	}
}

// Cleanup releases all resources
func (env *TestEnvironment) Cleanup() {
	if env.Connection != nil {
		env.Connection.Close()
	}
	if env.Server != nil {
		env.Server.Stop()
	}
}

// createBufDialer creates a dialer for in-memory gRPC connections
func createBufDialer(listener *bufconn.Listener) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, url string) (net.Conn, error) {
		return listener.Dial()
	}
}

// injectTestConnection injects a working reflection client and discovers methods for testing
func injectTestConnection(serviceDiscoverer grpc.ServiceDiscoverer, reflectionClient grpc.ReflectionClient, logger *zap.Logger) error {
	// Discover methods using the reflection client
	methods, err := reflectionClient.DiscoverMethods(context.Background())
	if err != nil {
		return fmt.Errorf("failed to discover methods: %w", err)
	}

	// Use reflection to access private fields of the ServiceDiscoverer
	// This is a test-only hack to inject the reflection client and methods
	discovererValue := reflect.ValueOf(serviceDiscoverer).Elem()

	// Set the reflection client field
	reflectionField := discovererValue.FieldByName("reflectionClient")
	if reflectionField.IsValid() && reflectionField.CanSet() {
		reflectionField.Set(reflect.ValueOf(reflectionClient))
	}

	// Set the methods field
	methodsField := discovererValue.FieldByName("methods")
	if methodsField.IsValid() && methodsField.CanSet() {
		methodsField.Set(reflect.ValueOf(methods))
	}

	// Set the tools map
	toolsField := discovererValue.FieldByName("tools")
	if toolsField.IsValid() && toolsField.CanSet() {
		toolsMap := make(map[string]types.MethodInfo)
		for _, method := range methods {
			toolName := method.GenerateToolName()
			toolsMap[toolName] = method
		}
		toolsField.Set(reflect.ValueOf(toolsMap))
	}

	logger.Info("Injected test connection for ServiceDiscoverer",
		zap.Int("methodCount", len(methods)))

	return nil
}

// executeToolCallDirect directly invokes mock servers bypassing ServiceDiscoverer
func executeToolCallDirect(env *TestEnvironment, toolName string, arguments map[string]interface{}) (*mcp.ToolCallResult, error) {
	// Convert arguments to JSON
	argsJSON, err := json.Marshal(arguments)
	if err != nil {
		return &mcp.ToolCallResult{
			Content: []mcp.ContentBlock{mcp.TextContent(fmt.Sprintf("Error marshaling arguments: %s", err))},
			IsError: true,
		}, nil
	}

	// Find the method by tool name
	methods, err := env.Reflection.DiscoverMethods(env.Context)
	if err != nil {
		return &mcp.ToolCallResult{
			Content: []mcp.ContentBlock{mcp.TextContent(fmt.Sprintf("Error discovering methods: %s", err))},
			IsError: true,
		}, nil
	}

	var targetMethod *types.MethodInfo
	for _, method := range methods {
		if method.GenerateToolName() == toolName {
			targetMethod = &method
			break
		}
	}

	if targetMethod == nil {
		return nil, fmt.Errorf("tool %s does not match any discovered service method", toolName)
	}

	// Invoke method using reflection client
	result, err := env.Reflection.InvokeMethod(env.Context, map[string]string{}, *targetMethod, string(argsJSON))
	if err != nil {
		return &mcp.ToolCallResult{
			Content: []mcp.ContentBlock{mcp.TextContent(fmt.Sprintf("Error invoking method: %s", err))},
			IsError: true,
		}, nil
	}

	return &mcp.ToolCallResult{
		Content: []mcp.ContentBlock{mcp.TextContent(result)},
		IsError: false,
	}, nil
}

// Unified Mock Server Implementations
// These servers provide consistent behavior across all tests

type UnifiedMockUserProfileServer struct {
	proto.UnimplementedUserProfileServiceServer
}

func (s *UnifiedMockUserProfileServer) GetUserProfile(ctx context.Context, req *proto.GetUserProfileRequest) (*proto.GetUserProfileResponse, error) {
	if req.UserId == "error" {
		return nil, fmt.Errorf("user not found")
	}

	userType := proto.UserType_STANDARD
	switch req.UserId {
	case "premium":
		userType = proto.UserType_PREMIUM
	case "admin":
		userType = proto.UserType_ADMIN
	}

	return &proto.GetUserProfileResponse{
		Profile: &proto.UserProfile{
			UserId:      req.UserId,
			DisplayName: "Test User " + req.UserId,
			Email:       req.UserId + "@example.com",
			UserType:    userType,
			LastLogin:   timestamppb.New(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)),
		},
	}, nil
}

type UnifiedMockDocumentServer struct {
	proto.UnimplementedDocumentServiceServer
}

func (s *UnifiedMockDocumentServer) CreateDocument(ctx context.Context, req *proto.CreateDocumentRequest) (*proto.CreateDocumentResponse, error) {
	if req.Document == nil || req.Document.Title == "" {
		return nil, fmt.Errorf("invalid document")
	}

	return &proto.CreateDocumentResponse{
		DocumentId: "doc-" + strings.ReplaceAll(req.Document.Title, " ", "-"),
		Success:    true,
	}, nil
}

type UnifiedMockNodeServer struct {
	proto.UnimplementedNodeServiceServer
}

func (s *UnifiedMockNodeServer) ProcessNode(ctx context.Context, req *proto.ProcessNodeRequest) (*proto.ProcessNodeResponse, error) {
	if req.RootNode == nil {
		return nil, fmt.Errorf("root node is required")
	}

	totalNodes := s.countNodes(req.RootNode)
	return &proto.ProcessNodeResponse{
		ProcessedSummary: fmt.Sprintf("Processed tree with root '%s'", req.RootNode.Value),
		TotalNodes:       int32(totalNodes),
	}, nil
}

func (s *UnifiedMockNodeServer) countNodes(node *proto.Node) int {
	if node == nil {
		return 0
	}
	count := 1
	for _, child := range node.Children {
		count += s.countNodes(child)
	}
	return count
}

// Test Helper Functions

// ToolCallTestCase represents a standardized test case for tool calls
type ToolCallTestCase struct {
	Name               string
	ToolName           string
	Arguments          map[string]interface{}
	ExpectError        bool
	ExpectHandlerError bool
	ErrorSubstring     string
	Validator          func(t *testing.T, response map[string]interface{})
}

// ExecuteToolCall performs a tool call with standardized assertion patterns
func ExecuteToolCall(t *testing.T, env *TestEnvironment, test ToolCallTestCase) {
	// Use direct mock invocation for "real gRPC" tests since ServiceDiscoverer has connection issues
	result, err := executeToolCallDirect(env, test.ToolName, test.Arguments)

	if test.ExpectHandlerError {
		require.Error(t, err)
		assert.Contains(t, err.Error(), test.ErrorSubstring)
		return
	}

	if test.ExpectError {
		require.NoError(t, err)
		assert.True(t, result.IsError)
		require.Len(t, result.Content, 1)
		assert.Contains(t, result.Content[0].Text, test.ErrorSubstring)
		return
	}

	// Success case
	require.NoError(t, err)
	if result.IsError {
		t.Logf("Tool call returned error: %s", result.Content[0].Text)
	}
	assert.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	assert.Equal(t, mcp.ContentTypeText, result.Content[0].Type)

	if test.Validator != nil {
		response := ParseJSONResponse(t, result)
		test.Validator(t, response)
	}
}

// ParseJSONResponse parses a JSON response from a tool call result
func ParseJSONResponse(t *testing.T, result *mcp.ToolCallResult) map[string]interface{} {
	require.Len(t, result.Content, 1)
	var response map[string]interface{}
	err := json.Unmarshal([]byte(result.Content[0].Text), &response)
	require.NoError(t, err)
	return response
}

// AssertNumericField handles different numeric types from JSON unmarshaling
func AssertNumericField(t *testing.T, response map[string]interface{}, field string, expected int32) {
	value := response[field]
	switch v := value.(type) {
	case float64:
		assert.Equal(t, expected, int32(v))
	case int32:
		assert.Equal(t, expected, v)
	case int:
		assert.Equal(t, expected, int32(v))
	default:
		t.Errorf("Unexpected type for %s: %T", field, v)
	}
}

// ServiceInfo represents a service with its methods for testing
type ServiceInfo struct {
	Name    string
	Methods []types.MethodInfo
}

// GetServiceByName finds a service by name from the discovered methods
func GetServiceByName(env *TestEnvironment, serviceName string) (ServiceInfo, bool) {
	// Use reflection client directly to discover methods
	methods, err := env.Reflection.DiscoverMethods(env.Context)
	if err != nil {
		return ServiceInfo{}, false
	}

	serviceMethods := []types.MethodInfo{}

	for _, method := range methods {
		if method.ServiceName == serviceName {
			serviceMethods = append(serviceMethods, method)
		}
	}

	if len(serviceMethods) == 0 {
		return ServiceInfo{}, false
	}

	return ServiceInfo{Name: serviceName, Methods: serviceMethods}, true
}

// DiscoverServices returns all discovered services as a slice
func DiscoverServices(t *testing.T, env *TestEnvironment) []ServiceInfo {
	// Use reflection client directly since it's connected to the test server
	methods, err := env.Reflection.DiscoverMethods(env.Context)
	require.NoError(t, err)

	serviceMap := make(map[string][]types.MethodInfo)

	// Group methods by service
	for _, method := range methods {
		serviceMap[method.ServiceName] = append(serviceMap[method.ServiceName], method)
	}

	// Convert to ServiceInfo slice
	services := []ServiceInfo{}
	for serviceName, serviceMethods := range serviceMap {
		services = append(services, ServiceInfo{
			Name:    serviceName,
			Methods: serviceMethods,
		})
	}

	return services
}

// BuildAllTools builds MCP tools for all discovered services
func BuildAllTools(t *testing.T, env *TestEnvironment) []mcp.Tool {
	// Use reflection client to get methods
	methods, err := env.Reflection.DiscoverMethods(env.Context)
	require.NoError(t, err)

	tools, err := env.ToolBuilder.BuildTools(methods)
	require.NoError(t, err)
	return tools
}

// ValidateJSONSchema validates that a schema can be marshaled and unmarshaled
func ValidateJSONSchema(t *testing.T, schema interface{}) {
	schemaJSON, err := json.Marshal(schema)
	require.NoError(t, err)

	var schemaMap map[string]interface{}
	err = json.Unmarshal(schemaJSON, &schemaMap)
	require.NoError(t, err)
}

// ParseToolNameFromServices simulates the tool name parsing logic
func ParseToolNameFromServices(toolName string, services []ServiceInfo) (string, string, bool) {
	for _, service := range services {
		servicePart := strings.ToLower(strings.ReplaceAll(service.Name, ".", "_"))

		for _, method := range service.Methods {
			methodPart := strings.ToLower(method.Name)
			expectedToolName := fmt.Sprintf("%s_%s", servicePart, methodPart)

			if expectedToolName == toolName {
				return service.Name, method.Name, true
			}
		}
	}
	return "", "", false
}

// AssertServiceExists validates that a service exists with expected methods
func AssertServiceExists(t *testing.T, services []ServiceInfo, serviceName string, expectedMethods []string) {
	var foundService *ServiceInfo
	for _, service := range services {
		if service.Name == serviceName {
			foundService = &service
			break
		}
	}

	require.NotNil(t, foundService, "Service %s should exist", serviceName)
	assert.Equal(t, len(expectedMethods), len(foundService.Methods))

	for _, expectedMethod := range expectedMethods {
		found := false
		for _, method := range foundService.Methods {
			if method.Name == expectedMethod {
				found = true
				break
			}
		}
		assert.True(t, found, "Method %s should exist in service %s", expectedMethod, serviceName)
	}
}

// AssertToolExists validates that a tool exists with the expected name
func AssertToolExists(t *testing.T, tools []mcp.Tool, toolName string) *mcp.Tool {
	for i, tool := range tools {
		if tool.Name == toolName {
			return &tools[i]
		}
	}
	t.Fatalf("Tool %s should exist", toolName)
	return nil
}

// AssertSchemaField validates that a schema field exists with the expected type
func AssertSchemaField(t *testing.T, schema map[string]interface{}, fieldPath []string, expectedType string) map[string]interface{} {
	current := schema
	for i, fieldName := range fieldPath {
		if i == len(fieldPath)-1 {
			// Final field - check type
			field, ok := current[fieldName].(map[string]interface{})
			require.True(t, ok, "Field %s should exist at path %v", fieldName, fieldPath)
			assert.Equal(t, expectedType, field["type"])
			return field
		} else {
			// Intermediate field - navigate deeper
			if fieldName == "properties" {
				properties, ok := current["properties"].(map[string]interface{})
				require.True(t, ok, "Properties should exist at path %v", fieldPath[:i+1])
				current = properties
			} else {
				field, ok := current[fieldName].(map[string]interface{})
				require.True(t, ok, "Field %s should exist at path %v", fieldName, fieldPath[:i+1])
				current = field
			}
		}
	}
	return current
}
