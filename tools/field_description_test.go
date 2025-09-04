package tools

import (
	"testing"

	"github.com/lysfighting/ggRMCP/grpc"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestGenerateDescription_WithMethodComments(t *testing.T) {
	logger := zap.NewNop()
	builder := NewMCPToolBuilder(logger)

	// Test that method descriptions come from MethodInfo.Description field
	methodInfo := grpc.MethodInfo{
		Name:        "SayHello",
		ServiceName: "hello.HelloService",
		Description: "Sends a greeting to the user", // This should be used
	}

	description := builder.generateDescription(methodInfo)

	// Verify that the method description is used when available
	assert.Equal(t, "Sends a greeting to the user", description)
}

func TestGenerateDescription_WithoutComments(t *testing.T) {
	logger := zap.NewNop()
	builder := NewMCPToolBuilder(logger)

	// Test fallback when no description is available
	methodInfo := grpc.MethodInfo{
		Name:        "SayHello",
		ServiceName: "hello.HelloService",
		Description: "", // No description
	}

	description := builder.generateDescription(methodInfo)

	// Verify fallback description
	assert.Equal(t, "Calls the SayHello method of the hello.HelloService service", description)
}
