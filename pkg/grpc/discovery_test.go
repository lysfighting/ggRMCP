package grpc

import (
	"context"
	"testing"

	"github.com/aalobaidi/ggRMCP/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
	grpcLib "google.golang.org/grpc"
)

// Mock implementations for testing

type mockConnectionManager struct {
	mock.Mock
}

func (m *mockConnectionManager) Connect(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockConnectionManager) GetConnection() *grpcLib.ClientConn {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*grpcLib.ClientConn)
}

func (m *mockConnectionManager) IsConnected() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *mockConnectionManager) Reconnect(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockConnectionManager) HealthCheck(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockConnectionManager) Close() error {
	args := m.Called()
	return args.Error(0)
}

type mockReflectionClient struct {
	mock.Mock
}

func (m *mockReflectionClient) DiscoverMethods(ctx context.Context) ([]types.MethodInfo, error) {
	args := m.Called(ctx)
	return args.Get(0).([]types.MethodInfo), args.Error(1)
}

func (m *mockReflectionClient) InvokeMethod(ctx context.Context, headers map[string]string, method types.MethodInfo, inputJSON string) (string, error) {
	args := m.Called(ctx, headers, method, inputJSON)
	return args.String(0), args.Error(1)
}

func (m *mockReflectionClient) HealthCheck(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *mockReflectionClient) Close() error {
	args := m.Called()
	return args.Error(0)
}

func TestServiceDiscoverer_InvokeMethodByTool(t *testing.T) {
	// Create logger
	logger := zap.NewNop()

	// Create mock connection manager
	mockConnMgr := &mockConnectionManager{}
	mockConnMgr.On("IsConnected").Return(true)

	// Create service discoverer
	discoverer := newServiceDiscovererWithConnManager(mockConnMgr, logger)

	// Create mock reflection client
	mockReflClient := &mockReflectionClient{}

	// Set up test data
	toolName := "test_service_testmethod"
	methodInfo := types.MethodInfo{
		Name:              "TestMethod",
		FullName:          "test.Service.TestMethod",
		ServiceName:       "test.Service",
		ToolName:          toolName,
		InputType:         "test.Request",
		OutputType:        "test.Response",
		IsClientStreaming: false,
		IsServerStreaming: false,
	}

	// Populate tools in discoverer
	tools := map[string]types.MethodInfo{
		toolName: methodInfo,
	}
	discoverer.tools.Store(&tools)

	// Set mock reflection client
	discoverer.reflectionClient = mockReflClient

	// Test headers to forward
	headers := map[string]string{
		"authorization": "Bearer token123",
		"x-trace-id":    "trace-456",
		"user-agent":    "test-client",
	}

	// Expected method invocation
	mockReflClient.On("InvokeMethod",
		mock.Anything, // context
		headers,
		methodInfo,
		`{"input":"test"}`,
	).Return(`{"output":"result"}`, nil)

	// Test the method invocation by tool name
	result, err := discoverer.InvokeMethodByTool(
		context.Background(),
		headers,
		toolName,
		`{"input":"test"}`,
	)

	// Assertions
	assert.NoError(t, err)
	assert.Equal(t, `{"output":"result"}`, result)

	// Verify all expectations were met
	mockReflClient.AssertExpectations(t)
}
