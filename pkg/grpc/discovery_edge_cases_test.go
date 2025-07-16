package grpc

import (
	"context"
	"testing"

	"github.com/aalobaidi/ggRMCP/pkg/config"
	"github.com/aalobaidi/ggRMCP/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestDiscoverMethods_MultipleServicesInOneFile tests the critical scenario
// where multiple gRPC services are defined in a single .proto file
func TestDiscoverMethods_MultipleServicesInOneFile(t *testing.T) {
	logger := zap.NewNop()

	// Use the complex service descriptor file which contains multiple services
	descriptorConfig := config.DescriptorSetConfig{
		Enabled:              true,
		Path:                 "../../examples/hello-service/build/complex_service.binpb",
		PreferOverReflection: true,
		IncludeSourceInfo:    true,
	}

	discoverer, err := NewServiceDiscoverer("localhost", 50051, logger, descriptorConfig)
	require.NoError(t, err)

	unifiedDiscoverer := discoverer.(*serviceDiscoverer)

	// Test direct FileDescriptorSet discovery
	methods, err := unifiedDiscoverer.discoverFromFileDescriptor()
	if err != nil {
		t.Skip("Complex service descriptor file not found - run 'make descriptor' in examples/hello-service")
		return
	}

	// Set the discovered tools in the discoverer for the test
	tools := make(map[string]types.MethodInfo)
	for _, method := range methods {
		tools[method.ToolName] = method
	}
	unifiedDiscoverer.tools.Store(&tools)

	methods = unifiedDiscoverer.GetMethods()
	require.NotEmpty(t, methods, "Should discover methods from multiple services")

	// Verify we discover methods from multiple services in the same file
	serviceNames := make(map[string]bool)
	methodsByService := make(map[string][]types.MethodInfo)

	for _, method := range methods {
		serviceNames[method.ServiceName] = true
		methodsByService[method.ServiceName] = append(methodsByService[method.ServiceName], method)
	}

	// Should discover at least 3 services from the complex service file
	assert.GreaterOrEqual(t, len(serviceNames), 3, "Should discover multiple services from one file")

	expectedServices := []string{
		"complex.UserProfileService",
		"complex.DocumentService",
		"complex.NodeService",
	}

	for _, expectedService := range expectedServices {
		assert.True(t, serviceNames[expectedService], "Should discover service: %s", expectedService)
		assert.NotEmpty(t, methodsByService[expectedService], "Service %s should have methods", expectedService)
	}

	// Verify each service has the expected methods
	assert.Contains(t, getMethodNames(methodsByService["complex.UserProfileService"]), "GetUserProfile")
	assert.Contains(t, getMethodNames(methodsByService["complex.DocumentService"]), "CreateDocument")
	assert.Contains(t, getMethodNames(methodsByService["complex.NodeService"]), "ProcessNode")

	t.Logf("✅ Successfully discovered %d methods from %d services in one file", len(methods), len(serviceNames))
}

// TestDiscoverMethods_NoPackage tests discovery of services without package names
func TestDiscoverMethods_NoPackage(t *testing.T) {
	logger := zap.NewNop()
	client := &reflectionClient{
		logger:  logger,
		fdCache: make(map[string]*descriptorpb.FileDescriptorProto),
	}

	// Create a mock file descriptor without package
	fileDescriptor := &descriptorpb.FileDescriptorProto{
		Name: stringPtr("simple.proto"),
		// No package field
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: stringPtr("SimpleRequest"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   stringPtr("id"),
						Number: int32Ptr(1),
						Type:   fieldTypePtr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					},
				},
			},
			{
				Name: stringPtr("SimpleResponse"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:   stringPtr("result"),
						Number: int32Ptr(1),
						Type:   fieldTypePtr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
					},
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: stringPtr("SimpleService"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       stringPtr("SimpleMethod"),
						InputType:  stringPtr(".SimpleRequest"),
						OutputType: stringPtr(".SimpleResponse"),
					},
				},
			},
		},
	}

	ctx := context.Background()
	targetServices := []string{"SimpleService"}

	// Test that services without packages can be discovered
	methods := client.extractMethodsFromFileDescriptor(ctx, fileDescriptor, targetServices)

	require.Len(t, methods, 1, "Should discover one method from service without package")

	method := methods[0]
	assert.Equal(t, "SimpleService", method.ServiceName)
	assert.Equal(t, "SimpleMethod", method.Name)
	assert.Equal(t, "SimpleService.SimpleMethod", method.FullName)
	assert.Equal(t, "simpleservice_simplemethod", method.ToolName)

	t.Logf("✅ Successfully discovered method from service without package: %s", method.FullName)
}

// TestToolNameGeneration_EdgeCases tests tool name generation for various edge cases
func TestToolNameGeneration_EdgeCases(t *testing.T) {
	tests := []struct {
		name             string
		serviceName      string
		methodName       string
		expectedToolName string
	}{
		{
			name:             "Service without package",
			serviceName:      "SimpleService",
			methodName:       "SimpleMethod",
			expectedToolName: "simpleservice_simplemethod",
		},
		{
			name:             "Service with single package",
			serviceName:      "hello.HelloService",
			methodName:       "SayHello",
			expectedToolName: "hello_helloservice_sayhello",
		},
		{
			name:             "Service with multiple packages",
			serviceName:      "com.example.complex.UserProfileService",
			methodName:       "GetUserProfile",
			expectedToolName: "com_example_complex_userprofileservice_getuserprofile",
		},
		{
			name:             "Service with underscores",
			serviceName:      "com.example.user_service.UserService",
			methodName:       "Get_User_Profile",
			expectedToolName: "com_example_user_service_userservice_get_user_profile",
		},
		{
			name:             "Service with numbers",
			serviceName:      "api.v1.UserService",
			methodName:       "GetUser",
			expectedToolName: "api_v1_userservice_getuser",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create method info and generate tool name
			method := types.MethodInfo{
				ServiceName: tt.serviceName,
				Name:        tt.methodName,
			}
			actualToolName := method.GenerateToolName()

			assert.Equal(t, tt.expectedToolName, actualToolName,
				"Tool name mismatch for service %s, method %s", tt.serviceName, tt.methodName)
		})
	}
}

// TestPartialServiceDiscovery tests selective discovery based on target service list
func TestPartialServiceDiscovery(t *testing.T) {
	logger := zap.NewNop()
	client := &reflectionClient{
		logger:  logger,
		fdCache: make(map[string]*descriptorpb.FileDescriptorProto),
	}

	// Create a mock file descriptor with multiple services
	fileDescriptor := &descriptorpb.FileDescriptorProto{
		Name:    stringPtr("multi.proto"),
		Package: stringPtr("com.example"),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: stringPtr("Request1")},
			{Name: stringPtr("Response1")},
			{Name: stringPtr("Request2")},
			{Name: stringPtr("Response2")},
			{Name: stringPtr("Request3")},
			{Name: stringPtr("Response3")},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{
			{
				Name: stringPtr("Service1"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       stringPtr("Method1"),
						InputType:  stringPtr(".com.example.Request1"),
						OutputType: stringPtr(".com.example.Response1"),
					},
				},
			},
			{
				Name: stringPtr("Service2"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       stringPtr("Method2"),
						InputType:  stringPtr(".com.example.Request2"),
						OutputType: stringPtr(".com.example.Response2"),
					},
				},
			},
			{
				Name: stringPtr("Service3"),
				Method: []*descriptorpb.MethodDescriptorProto{
					{
						Name:       stringPtr("Method3"),
						InputType:  stringPtr(".com.example.Request3"),
						OutputType: stringPtr(".com.example.Response3"),
					},
				},
			},
		},
	}

	ctx := context.Background()

	// Test 1: Discover all services
	allServices := []string{"com.example.Service1", "com.example.Service2", "com.example.Service3"}
	allMethods := client.extractMethodsFromFileDescriptor(ctx, fileDescriptor, allServices)
	assert.Len(t, allMethods, 3, "Should discover all 3 methods when all services are targeted")

	// Test 2: Discover only specific services
	partialServices := []string{"com.example.Service1", "com.example.Service3"}
	partialMethods := client.extractMethodsFromFileDescriptor(ctx, fileDescriptor, partialServices)
	assert.Len(t, partialMethods, 2, "Should discover only 2 methods when 2 services are targeted")

	// Verify correct services were discovered
	discoveredServices := make(map[string]bool)
	for _, method := range partialMethods {
		discoveredServices[method.ServiceName] = true
	}
	assert.True(t, discoveredServices["com.example.Service1"], "Should discover Service1")
	assert.True(t, discoveredServices["com.example.Service3"], "Should discover Service3")
	assert.False(t, discoveredServices["com.example.Service2"], "Should not discover Service2")

	// Test 3: Discover non-existent service (should not crash)
	nonExistentServices := []string{"com.example.NonExistent"}
	noMethods := client.extractMethodsFromFileDescriptor(ctx, fileDescriptor, nonExistentServices)
	assert.Len(t, noMethods, 0, "Should discover no methods when targeting non-existent service")

	t.Logf("✅ Partial service discovery working correctly")
}

// TestMethodInvocationByToolName tests tool-based method invocation
func TestMethodInvocationByToolName(t *testing.T) {
	logger := zap.NewNop()

	descriptorConfig := config.DescriptorSetConfig{
		Enabled:              true,
		Path:                 "../../examples/hello-service/build/hello.binpb",
		PreferOverReflection: true,
		IncludeSourceInfo:    true,
	}

	discoverer, err := NewServiceDiscoverer("localhost", 50051, logger, descriptorConfig)
	require.NoError(t, err)

	unifiedDiscoverer := discoverer.(*serviceDiscoverer)

	methods, err := unifiedDiscoverer.discoverFromFileDescriptor()
	if err != nil {
		t.Skip("Descriptor set file not found - run 'make descriptor' in examples/hello-service")
		return
	}

	// Set the discovered tools in the discoverer for the test
	tools := make(map[string]types.MethodInfo)
	for _, method := range methods {
		tools[method.ToolName] = method
	}
	unifiedDiscoverer.tools.Store(&tools)

	methods = unifiedDiscoverer.GetMethods()
	require.NotEmpty(t, methods, "Should have discovered methods")

	// Find the hello service method
	var helloMethod *types.MethodInfo
	for _, method := range methods {
		if method.ServiceName == "hello.HelloService" && method.Name == "SayHello" {
			helloMethod = &method
			break
		}
	}
	require.NotNil(t, helloMethod, "Should find SayHello method")

	// Verify the method can be looked up by tool name
	foundMethod, exists := unifiedDiscoverer.getMethodByTool(helloMethod.ToolName)
	require.True(t, exists, "Should find method by tool name")
	assert.Equal(t, helloMethod.FullName, foundMethod.FullName)
	assert.Equal(t, helloMethod.ServiceName, foundMethod.ServiceName)
	assert.Equal(t, helloMethod.Name, foundMethod.Name)

	t.Logf("✅ Method invocation by tool name works: %s -> %s", helloMethod.ToolName, helloMethod.FullName)
}

// TestComplexServiceCoverage tests discovery of complex services with multiple methods
func TestComplexServiceCoverage(t *testing.T) {
	logger := zap.NewNop()

	// Test with complex service descriptor file (multiple services in one file)
	descriptorConfig := config.DescriptorSetConfig{
		Enabled:              true,
		Path:                 "../../examples/hello-service/build/complex_service.binpb",
		PreferOverReflection: true,
		IncludeSourceInfo:    true,
	}

	discoverer, err := NewServiceDiscoverer("localhost", 50051, logger, descriptorConfig)
	require.NoError(t, err)

	unifiedDiscoverer := discoverer.(*serviceDiscoverer)

	methods, err := unifiedDiscoverer.discoverFromFileDescriptor()
	if err != nil {
		t.Skip("Complex service descriptor file not found - run 'make descriptor' in examples/hello-service")
		return
	}

	// Set the discovered tools in the discoverer for the test
	tools := make(map[string]types.MethodInfo)
	for _, method := range methods {
		tools[method.ToolName] = method
	}
	unifiedDiscoverer.tools.Store(&tools)

	methods = unifiedDiscoverer.GetMethods()
	require.NotEmpty(t, methods, "Should discover methods")

	// Test the same expectations as the integration tests
	expectedMethodsByService := map[string]string{
		"complex.UserProfileService": "GetUserProfile",
		"complex.DocumentService":    "CreateDocument",
		"complex.NodeService":        "ProcessNode",
	}

	discoveredMethods := make(map[string]string)
	for _, method := range methods {
		discoveredMethods[method.ServiceName] = method.Name
	}

	for expectedService, expectedMethod := range expectedMethodsByService {
		assert.Equal(t, expectedMethod, discoveredMethods[expectedService],
			"Service %s should have method %s", expectedService, expectedMethod)
	}

	// Verify tool name generation follows expected pattern
	for _, method := range methods {
		expectedToolName := method.GenerateToolName()
		assert.Equal(t, expectedToolName, method.ToolName,
			"Tool name should match expected pattern for %s.%s", method.ServiceName, method.Name)
	}

	assert.Equal(t, len(expectedMethodsByService), len(methods),
		"Should discover exactly %d methods from complex services", len(expectedMethodsByService))

	t.Logf("✅ Complex service discovery maintains integration test coverage: %d services, %d methods",
		len(expectedMethodsByService), len(methods))
}

// Helper functions

func getMethodNames(methods []types.MethodInfo) []string {
	names := make([]string, len(methods))
	for i, method := range methods {
		names[i] = method.Name
	}
	return names
}
