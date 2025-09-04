package tools

import (
	"testing"

	"github.com/lysfighting/ggRMCP/descriptors"
	"github.com/lysfighting/ggRMCP/grpc"
	"github.com/lysfighting/ggRMCP/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestToolBuilder_CommentPropagation(t *testing.T) {
	logger := zap.NewNop()
	builder := NewMCPToolBuilder(logger)

	t.Run("MethodDescriptionPropagation", func(t *testing.T) {
		// Test that MethodInfo.Description properly flows to tool description
		methodWithDescription := grpc.MethodInfo{
			Name:        "SayHello",
			FullName:    "hello.HelloService.SayHello",
			ServiceName: "hello.HelloService",
			Description: "Sends a personalized greeting to the user", // From FileDescriptorSet
			// ... other fields would be set by real discovery
		}

		methodWithoutDescription := grpc.MethodInfo{
			Name:        "SayHello",
			FullName:    "hello.HelloService.SayHello",
			ServiceName: "hello.HelloService",
			Description: "", // No description
		}

		// Test with description
		descWithComments := builder.generateDescription(methodWithDescription)
		assert.Equal(t, "Sends a personalized greeting to the user", descWithComments)
		t.Logf("✅ Description with comments: '%s'", descWithComments)

		// Test without description (fallback)
		descWithoutComments := builder.generateDescription(methodWithoutDescription)
		assert.Equal(t, "Calls the SayHello method of the hello.HelloService service", descWithoutComments)
		t.Logf("✅ Fallback description: '%s'", descWithoutComments)
	})

	t.Run("FieldDescriptionExtraction", func(t *testing.T) {
		// Test that field descriptions are extracted from FileDescriptorSet comments
		// Load the FileDescriptorSet with comments
		loader := descriptors.NewLoader(zap.NewNop())

		// Load the hello.binpb file with comments
		fdSet, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")
		require.NoError(t, err, "Should load FileDescriptorSet")

		// Build file registry from FileDescriptorSet
		files, err := loader.BuildRegistry(fdSet)
		require.NoError(t, err, "Should build file registry")

		// Extract method information with service context
		methods, err := loader.ExtractMethodInfo(files)
		require.NoError(t, err, "Should extract method info")
		require.NotEmpty(t, methods, "Should have methods")

		// Find the HelloService method
		var sayHelloMethod *types.MethodInfo
		for _, method := range methods {
			if method.ServiceName == "hello.HelloService" && method.Name == "SayHello" {
				sayHelloMethod = &method
				break
			}
		}
		require.NotNil(t, sayHelloMethod, "Should find SayHello method")

		// Test field description extraction from input message (HelloRequest)
		inputDesc := sayHelloMethod.InputDescriptor

		// Test name field
		nameField := inputDesc.Fields().ByName("name")
		require.NotNil(t, nameField, "Should find name field")
		nameDescription := builder.ExtractFieldComments(nameField)
		assert.Equal(t, "The name of the user", nameDescription, "Should extract name field comment")

		// Test email field
		emailField := inputDesc.Fields().ByName("email")
		require.NotNil(t, emailField, "Should find email field")
		emailDescription := builder.ExtractFieldComments(emailField)
		assert.Equal(t, "The email of the user", emailDescription, "Should extract email field comment")

		// Test field description extraction from output message (HelloReply)
		outputDesc := sayHelloMethod.OutputDescriptor

		// Test message field
		messageField := outputDesc.Fields().ByName("message")
		require.NotNil(t, messageField, "Should find message field")
		messageDescription := builder.ExtractFieldComments(messageField)
		assert.Equal(t, "The greeting message", messageDescription, "Should extract message field comment")

		t.Log("✅ Field descriptions extracted successfully:")
		t.Logf("   - name: '%s'", nameDescription)
		t.Logf("   - email: '%s'", emailDescription)
		t.Logf("   - message: '%s'", messageDescription)
	})
}
