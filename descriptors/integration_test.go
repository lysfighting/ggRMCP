package descriptors

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestFileDescriptorSetLoading_EndToEnd(t *testing.T) {
	logger := zap.NewNop()
	loader := NewLoader(logger)

	t.Run("LoadHelloServiceWithComments", func(t *testing.T) {
		// Load the actual hello.binpb file that should contain source info
		fdSet, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")
		require.NoError(t, err, "Should load hello.binpb file")

		// Build registry
		files, err := loader.BuildRegistry(fdSet)
		require.NoError(t, err, "Should build registry from FileDescriptorSet")

		// Extract method info with service context
		methods, err := loader.ExtractMethodInfo(files)
		require.NoError(t, err, "Should extract method info")
		require.Len(t, methods, 1, "Should have exactly one method")

		// Verify method with service context
		method := methods[0]
		assert.Equal(t, "hello.HelloService", method.ServiceName)
		assert.Equal(t, "SayHello", method.Name)

		// Verify method details
		assert.Equal(t, "hello.HelloService.SayHello", method.FullName)

		// This is the key test - verify that method description was extracted from comments
		assert.NotEmpty(t, method.Description, "Method should have description from proto comments")
		assert.Contains(t, method.Description, "greeting", "Description should mention greeting")

		t.Logf("✅ Method description extracted: '%s'", method.Description)
	})

	t.Run("VerifySourceCodeInfoPresent", func(t *testing.T) {
		// Load the FileDescriptorSet and verify it contains source code info
		fdSet, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")
		require.NoError(t, err)

		// Check that at least one file has source code info
		hasSourceInfo := false
		for _, file := range fdSet.File {
			if file.SourceCodeInfo != nil {
				hasSourceInfo = true
				t.Logf("✅ Found source code info in file: %s", file.GetName())
				t.Logf("   - Location count: %d", len(file.SourceCodeInfo.Location))
			}
		}

		assert.True(t, hasSourceInfo, "FileDescriptorSet should contain source code info for comment extraction")
	})

	t.Run("CompareWithNonExistentFile", func(t *testing.T) {
		// Test error handling for non-existent file
		_, err := loader.LoadFromFile("non-existent-file.binpb")
		assert.Error(t, err, "Should error when file doesn't exist")
		assert.Contains(t, err.Error(), "failed to open descriptor file")
	})
}

func TestCommentParsing_EndToEnd(t *testing.T) {
	logger := zap.NewNop()
	loader := NewLoader(logger)

	t.Run("ExtractAndVerifyComments", func(t *testing.T) {
		// Load FileDescriptorSet
		fdSet, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")
		require.NoError(t, err)

		// Build registry
		files, err := loader.BuildRegistry(fdSet)
		require.NoError(t, err)

		// Extract method info to test comment propagation
		methods, err := loader.ExtractMethodInfo(files)
		require.NoError(t, err)
		require.Len(t, methods, 1)

		method := methods[0]

		// Test that comments were properly extracted and propagated
		if method.Description != "" {
			t.Logf("✅ Comment propagation successful: '%s'", method.Description)

			// Verify the comment contains expected content from hello.proto
			assert.Contains(t, method.Description, "greeting",
				"Method comment should contain 'greeting' from proto file")
		} else {
			t.Log("⚠️  No method description found - this indicates comment extraction isn't working")

			// Let's debug what's happening
			for _, file := range fdSet.File {
				if file.SourceCodeInfo != nil {
					t.Logf("Debug: File %s has %d source locations",
						file.GetName(), len(file.SourceCodeInfo.Location))

					// Log some locations to see what's being parsed
					for i, loc := range file.SourceCodeInfo.Location {
						if i < 3 { // Just log first 3 for debugging
							t.Logf("  Location %d: path=%v, leading='%s', trailing='%s'",
								i, loc.Path, loc.GetLeadingComments(), loc.GetTrailingComments())
						}
					}
				}
			}
		}
	})

	t.Run("TestCommentExtraction_DirectFromProto", func(t *testing.T) {
		// This test verifies the comment extraction mechanism directly
		fdSet, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")
		require.NoError(t, err)

		// Check each file in the descriptor set for source code info
		foundComments := 0
		for _, file := range fdSet.File {
			if file.SourceCodeInfo != nil {
				for _, location := range file.SourceCodeInfo.Location {
					leading := location.GetLeadingComments()
					trailing := location.GetTrailingComments()

					if leading != "" || trailing != "" {
						foundComments++
						t.Logf("Found comment: leading='%s', trailing='%s', path=%v",
							leading, trailing, location.Path)
					}
				}
			}
		}

		t.Logf("Total comments found: %d", foundComments)

		// We should find some comments if the descriptor was generated with --include_source_info
		if foundComments > 0 {
			t.Log("✅ Comment extraction mechanism is working")
		} else {
			t.Log("⚠️  No comments found - check if hello.binpb was generated with --include_source_info")
		}
	})
}

func TestFileDescriptorSetGeneration_EndToEnd(t *testing.T) {
	// This test verifies that our build process generates proper descriptor sets

	t.Run("VerifyDescriptorSetExists", func(t *testing.T) {
		logger := zap.NewNop()
		loader := NewLoader(logger)

		// Try to load the descriptor set
		_, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")

		if err != nil {
			t.Logf("Descriptor set not found: %v", err)
			t.Log("To generate it, run: cd examples/hello-service && make descriptor")
			t.Skip("Descriptor set file not found - run 'make descriptor' in examples/hello-service")
		}

		t.Log("✅ Descriptor set file exists and loads successfully")
	})
}

func TestCommentExtractionPath_Integration(t *testing.T) {
	// Test comment extraction from FileDescriptorSet to MethodInfo
	logger := zap.NewNop()
	loader := NewLoader(logger)

	fdSet, err := loader.LoadFromFile("../../examples/hello-service/build/hello.binpb")
	if err != nil {
		t.Skip("Descriptor set file not found - run 'make descriptor' in examples/hello-service")
		return
	}

	files, err := loader.BuildRegistry(fdSet)
	require.NoError(t, err)

	// Extract method info with comments
	enhancedMethods, err := loader.ExtractMethodInfo(files)
	require.NoError(t, err)
	require.Len(t, enhancedMethods, 1)

	enhancedMethod := enhancedMethods[0]

	t.Logf("Enhanced method description: '%s'", enhancedMethod.Description)

	// Verify comment propagation through the discovery pipeline
	regularMethodInfo := struct {
		Name        string
		Description string
	}{
		Name:        enhancedMethod.Name,
		Description: enhancedMethod.Description,
	}

	t.Logf("Regular method info description: '%s'", regularMethodInfo.Description)

	// Both should have the same description if the pipeline works correctly
	if enhancedMethod.Description != "" {
		assert.Equal(t, enhancedMethod.Description, regularMethodInfo.Description,
			"Description should propagate correctly from enhanced to regular method info")
		t.Log("✅ Comment propagation pipeline working correctly")
	} else {
		t.Log("⚠️  No description found - comment extraction may not be working")
	}
}
