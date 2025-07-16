package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestFilterInternalServices(t *testing.T) {
	logger := zap.NewNop()
	client := &reflectionClient{
		logger:  logger,
		fdCache: make(map[string]*descriptorpb.FileDescriptorProto),
	}

	services := []string{
		"grpc.reflection.v1alpha.ServerReflection",
		"grpc.health.v1.Health",
		"hello.HelloService",
		"com.example.MyService",
		"grpc.channelz.v1.Channelz",
		"grpc.testing.TestService",
	}

	filtered := client.filterInternalServices(services)

	// Should only have 2 non-internal services
	assert.Len(t, filtered, 2, "Should filter out internal services")
	assert.Contains(t, filtered, "hello.HelloService")
	assert.Contains(t, filtered, "com.example.MyService")

	// Should not contain any internal services
	assert.NotContains(t, filtered, "grpc.reflection.v1alpha.ServerReflection")
	assert.NotContains(t, filtered, "grpc.health.v1.Health")
	assert.NotContains(t, filtered, "grpc.channelz.v1.Channelz")
	assert.NotContains(t, filtered, "grpc.testing.TestService")
}

func TestGetSimpleServiceName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello.HelloService", "HelloService"},
		{"com.example.MyService", "MyService"},
		{"SingleName", "SingleName"},
		{"", ""},
	}

	for _, test := range tests {
		result := getSimpleServiceName(test.input)
		assert.Equal(t, test.expected, result, "Input: %s", test.input)
	}
}
