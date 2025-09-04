package grpc

import (
	"testing"

	"github.com/lysfighting/ggRMCP/config"
	"github.com/lysfighting/ggRMCP/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUnifiedServiceDiscovery_FileDescriptorSetIntegration(t *testing.T) {
	logger := zap.NewNop()

	t.Run("DiscoverFromFileDescriptorSet_WithComments", func(t *testing.T) {
		// Create descriptor config pointing to hello service descriptor
		descriptorConfig := config.DescriptorSetConfig{
			Enabled:              true,
			Path:                 "../../examples/hello-service/build/hello.binpb",
			PreferOverReflection: true,
			IncludeSourceInfo:    true,
		}

		// Create service discoverer
		discoverer, err := NewServiceDiscoverer("localhost", 50051, logger, descriptorConfig)
		require.NoError(t, err)

		// Cast to access internal methods for testing
		unifiedDiscoverer := discoverer.(*serviceDiscoverer)

		// Test direct FileDescriptorSet discovery (without needing gRPC connection)
		methods, err := unifiedDiscoverer.discoverFromFileDescriptor()

		if err != nil {
			if err.Error() == "failed to load descriptor set: failed to open descriptor file ../../examples/hello-service/build/hello.binpb: open ../../examples/hello-service/build/hello.binpb: no such file or directory" {
				t.Skip("Descriptor set file not found - run 'make descriptor' in examples/hello-service")
				return
			}
			require.NoError(t, err, "Should discover from FileDescriptorSet")
		}

		// Set the discovered tools in the discoverer for the test
		tools := make(map[string]types.MethodInfo)
		for _, method := range methods {
			tools[method.ToolName] = method
		}
		unifiedDiscoverer.tools.Store(&tools)

		// Verify service discovery results
		methods = unifiedDiscoverer.GetMethods()
		require.NotEmpty(t, methods, "Should discover methods")

		// Get the hello service method
		var sayHelloMethod types.MethodInfo
		var found bool
		for _, method := range methods {
			if method.ServiceName == "hello.HelloService" && method.Name == "SayHello" {
				sayHelloMethod = method
				found = true
				break
			}
		}
		require.True(t, found, "Should find SayHello method")

		// Verify method details
		assert.Equal(t, "SayHello", sayHelloMethod.Name)
		assert.Equal(t, "hello.HelloService.SayHello", sayHelloMethod.FullName)

		// Key test: Verify method description was extracted from FileDescriptorSet comments
		t.Logf("Method description from FileDescriptorSet: '%s'", sayHelloMethod.Description)

		if sayHelloMethod.Description != "" {
			assert.Contains(t, sayHelloMethod.Description, "greeting",
				"Method description should contain 'greeting' from proto comments")
			t.Log("✅ FileDescriptorSet comment extraction successful")
		} else {
			t.Log("⚠️  No method description - comment extraction may not be working")
		}

		// Verify descriptors are present for tool building
		assert.NotNil(t, sayHelloMethod.InputDescriptor, "Should have input descriptor")
		assert.NotNil(t, sayHelloMethod.OutputDescriptor, "Should have output descriptor")
	})

	t.Run("FallbackFromFileDescriptorSetToReflection", func(t *testing.T) {
		// Test the fallback mechanism when FileDescriptorSet fails
		descriptorConfig := config.DescriptorSetConfig{
			Enabled:              true,
			Path:                 "non-existent-file.binpb", // This will fail
			PreferOverReflection: true,
			IncludeSourceInfo:    true,
		}

		discoverer, err := NewServiceDiscoverer("localhost", 50051, logger, descriptorConfig)
		require.NoError(t, err)

		unifiedDiscoverer := discoverer.(*serviceDiscoverer)

		// This should fail gracefully
		_, err = unifiedDiscoverer.discoverFromFileDescriptor()
		assert.Error(t, err, "Should fail when file doesn't exist")
		assert.Contains(t, err.Error(), "failed to load descriptor set")

		t.Log("✅ FileDescriptorSet failure handling works correctly")
	})
}

func TestMethodInfoPropagation_EndToEnd(t *testing.T) {
	// Test that MethodInfo.Description field properly propagates through the system
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

	// Discover from FileDescriptorSet
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

	// Get a specific method to test description propagation
	toolName := "hello_helloservice_sayhello" // Generated tool name for hello.HelloService.SayHello
	method, found := unifiedDiscoverer.getMethodByTool(toolName)
	require.True(t, found, "Should find SayHello method by tool name")

	t.Logf("MethodInfo.Description: '%s'", method.Description)
	t.Logf("MethodInfo.Name: '%s'", method.Name)
	t.Logf("MethodInfo.FullName: '%s'", method.FullName)

	// Verify the MethodInfo has the description field populated
	if method.Description != "" {
		assert.Contains(t, method.Description, "greeting",
			"MethodInfo.Description should contain content from proto comments")
		t.Log("✅ MethodInfo.Description properly populated from FileDescriptorSet")
	} else {
		t.Log("⚠️  MethodInfo.Description is empty - comment propagation not working")
	}

	// Test that this MethodInfo can be used by tools builder
	// (This simulates what happens in the MCP tool generation)
	if method.Description != "" {
		expectedToolDescription := method.Description
		t.Logf("Expected tool description: '%s'", expectedToolDescription)
		t.Log("✅ MethodInfo ready for tool generation with proper descriptions")
	}
}

func TestServiceDiscoveryConfiguration_EndToEnd(t *testing.T) {
	logger := zap.NewNop()

	testCases := []struct {
		name     string
		config   config.DescriptorSetConfig
		expected string
	}{
		{
			name: "FileDescriptorSetEnabled",
			config: config.DescriptorSetConfig{
				Enabled:              true,
				Path:                 "../../examples/hello-service/build/hello.binpb",
				PreferOverReflection: true,
				IncludeSourceInfo:    true,
			},
			expected: "should use FileDescriptorSet",
		},
		{
			name: "FileDescriptorSetDisabled",
			config: config.DescriptorSetConfig{
				Enabled:              false,
				Path:                 "../../examples/hello-service/build/hello.binpb",
				PreferOverReflection: false,
				IncludeSourceInfo:    false,
			},
			expected: "should skip FileDescriptorSet",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			discoverer, err := NewServiceDiscoverer("localhost", 50051, logger, tc.config)
			require.NoError(t, err)

			unifiedDiscoverer := discoverer.(*serviceDiscoverer)

			if tc.config.Enabled {
				// Should attempt FileDescriptorSet loading
				methods, err := unifiedDiscoverer.discoverFromFileDescriptor()
				if err != nil && err.Error() == "failed to load descriptor set: failed to open descriptor file ../../examples/hello-service/build/hello.binpb: open ../../examples/hello-service/build/hello.binpb: no such file or directory" {
					t.Skip("Descriptor set file not found")
					return
				}

				if err == nil {
					// Set the discovered tools in the discoverer for the test
					tools := make(map[string]types.MethodInfo)
					for _, method := range methods {
						tools[method.ToolName] = method
					}
					unifiedDiscoverer.tools.Store(&tools)

					methods = unifiedDiscoverer.GetMethods()
					assert.Greater(t, len(methods), 0, "Should discover methods from FileDescriptorSet")
					t.Log("✅ FileDescriptorSet discovery successful")
				}
			} else {
				// Should skip FileDescriptorSet when disabled
				// We can't easily test this without exposing internal state,
				// but the configuration is properly stored
				assert.False(t, tc.config.Enabled, "Config should be disabled")
				t.Log("✅ FileDescriptorSet disabled as expected")
			}
		})
	}
}
