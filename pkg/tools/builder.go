package tools

import (
	"fmt"
	"strings"

	"github.com/aalobaidi/ggRMCP/pkg/mcp"
	"github.com/aalobaidi/ggRMCP/pkg/types"
	"go.uber.org/zap"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// MCPToolBuilder builds MCP tools from gRPC service definitions and handles schema generation
type MCPToolBuilder struct {
	logger *zap.Logger

	// Cache for generated schemas
	schemaCache map[string]interface{}

	// Configuration
	maxRecursionDepth int
	includeComments   bool
}

// NewMCPToolBuilder creates a new MCP tool builder
func NewMCPToolBuilder(logger *zap.Logger) *MCPToolBuilder {
	return &MCPToolBuilder{
		logger:            logger,
		schemaCache:       make(map[string]interface{}),
		maxRecursionDepth: 10,
		includeComments:   true,
	}
}

// BuildTool builds an MCP tool from a gRPC method
func (b *MCPToolBuilder) BuildTool(method types.MethodInfo) (mcp.Tool, error) {
	// Generate tool name
	toolName := method.GenerateToolName()

	// Generate description
	description := b.generateDescription(method)

	// Generate input schema
	b.logger.Debug("Generating input schema",
		zap.String("toolName", toolName),
		zap.String("inputType", string(method.InputDescriptor.FullName())))

	inputSchema, err := b.ExtractMessageSchema(method.InputDescriptor)
	if err != nil {
		b.logger.Error("Failed to generate input schema",
			zap.String("toolName", toolName),
			zap.String("inputType", string(method.InputDescriptor.FullName())),
			zap.Error(err))
		return mcp.Tool{}, fmt.Errorf("failed to generate input schema: %w", err)
	}

	// Generate output schema
	b.logger.Debug("Generating output schema",
		zap.String("toolName", toolName),
		zap.String("outputType", string(method.OutputDescriptor.FullName())))

	outputSchema, err := b.ExtractMessageSchema(method.OutputDescriptor)
	if err != nil {
		b.logger.Error("Failed to generate output schema",
			zap.String("toolName", toolName),
			zap.String("outputType", string(method.OutputDescriptor.FullName())),
			zap.Error(err))
		return mcp.Tool{}, fmt.Errorf("failed to generate output schema: %w", err)
	}

	tool := mcp.Tool{
		Name:         toolName,
		Description:  description,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
	}

	// Validate the tool
	if err := b.validateTool(tool); err != nil {
		return mcp.Tool{}, fmt.Errorf("tool validation failed: %w", err)
	}

	b.logger.Debug("Built tool",
		zap.String("toolName", toolName),
		zap.String("service", method.ServiceName),
		zap.String("method", method.Name))

	return tool, nil
}

// generateDescription generates a tool description
func (b *MCPToolBuilder) generateDescription(method types.MethodInfo) string {
	// Use description from method if available (could be from FileDescriptorSet comments)
	if method.Description != "" {
		return method.Description
	}

	// Fallback to generic description
	return fmt.Sprintf("Calls the %s method of the %s service", method.Name, method.ServiceName)
}

// validateTool validates a generated tool
func (b *MCPToolBuilder) validateTool(tool mcp.Tool) error {
	if tool.Name == "" {
		return fmt.Errorf("tool name cannot be empty")
	}

	if tool.Description == "" {
		return fmt.Errorf("tool description cannot be empty")
	}

	if tool.InputSchema == nil {
		return fmt.Errorf("tool input schema cannot be nil")
	}

	// Validate that the name follows the expected pattern
	if !strings.Contains(tool.Name, "_") {
		return fmt.Errorf("tool name must contain underscore separator")
	}

	return nil
}

// BuildTools builds MCP tools for all methods
func (b *MCPToolBuilder) BuildTools(methods []types.MethodInfo) ([]mcp.Tool, error) {
	var tools []mcp.Tool

	for _, method := range methods {
		// Skip streaming methods
		if method.IsClientStreaming || method.IsServerStreaming {
			b.logger.Debug("Skipping streaming method",
				zap.String("service", method.ServiceName),
				zap.String("method", method.Name))
			continue
		}

		tool, err := b.BuildTool(method)
		if err != nil {
			b.logger.Error("Failed to build tool",
				zap.String("service", method.ServiceName),
				zap.String("method", method.Name),
				zap.Error(err))
			continue
		}

		tools = append(tools, tool)
	}

	b.logger.Info("Built tools", zap.Int("count", len(tools)))
	return tools, nil
}

// ========== Schema Extraction Methods ==========

// ExtractMessageSchema generates a JSON schema for a message with comments
func (b *MCPToolBuilder) ExtractMessageSchema(msgDesc protoreflect.MessageDescriptor) (map[string]interface{}, error) {
	// Use internal method with visited tracking
	return b.extractMessageSchemaInternal(msgDesc, make(map[string]bool))
}

// extractMessageSchemaInternal generates a JSON schema with circular reference detection
func (b *MCPToolBuilder) extractMessageSchemaInternal(msgDesc protoreflect.MessageDescriptor, visited map[string]bool) (map[string]interface{}, error) {
	// Check for circular references
	fullName := string(msgDesc.FullName())
	if visited[fullName] {
		// Return a reference to break the cycle
		b.logger.Debug("Found circular reference, using $ref",
			zap.String("messageType", fullName))
		return map[string]interface{}{
			"$ref": "#/definitions/" + fullName,
		}, nil
	}
	visited[fullName] = true
	defer func() { delete(visited, fullName) }() // Clean up on exit

	schema := map[string]interface{}{
		"type":       "object",
		"properties": make(map[string]interface{}),
	}

	// Add message-level description if available
	if desc := b.extractComments(msgDesc); desc != "" {
		schema["description"] = desc
	}

	required := []string{}
	properties := schema["properties"].(map[string]interface{})

	// Process each field
	for i := 0; i < msgDesc.Fields().Len(); i++ {
		field := msgDesc.Fields().Get(i)
		fieldName := string(field.Name())

		fieldSchema, err := b.extractFieldSchemaInternal(field, visited)
		if err != nil {
			b.logger.Warn("Failed to extract field schema",
				zap.String("message", string(msgDesc.FullName())),
				zap.String("field", fieldName),
				zap.Error(err))
			continue
		}

		properties[fieldName] = fieldSchema

		// Add to required if field is required (not optional)
		if field.HasOptionalKeyword() || field.HasPresence() {
			// Field is optional
		} else {
			required = append(required, fieldName)
		}
	}

	// Process oneofs
	for i := 0; i < msgDesc.Oneofs().Len(); i++ {
		oneof := msgDesc.Oneofs().Get(i)
		oneofName := string(oneof.Name())

		oneofSchema := map[string]interface{}{
			"type":  "object",
			"oneOf": []interface{}{},
		}

		// Add oneof description if available
		if desc := b.extractComments(oneof); desc != "" {
			oneofSchema["description"] = desc
		}

		// Process oneof fields
		for j := 0; j < oneof.Fields().Len(); j++ {
			field := oneof.Fields().Get(j)
			fieldName := string(field.Name())

			fieldSchema, err := b.extractFieldSchemaInternal(field, visited)
			if err != nil {
				b.logger.Warn("Failed to extract field schema for oneof",
					zap.String("field", fieldName),
					zap.Error(err))
				continue
			}

			oneofOption := map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					fieldName: fieldSchema,
				},
				"required": []string{fieldName},
			}

			oneofSchema["oneOf"] = append(oneofSchema["oneOf"].([]interface{}), oneofOption)
		}

		properties[oneofName] = oneofSchema
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema, nil
}

// extractFieldSchemaInternal generates schema for a single field with circular reference detection
func (b *MCPToolBuilder) extractFieldSchemaInternal(field protoreflect.FieldDescriptor, visited map[string]bool) (map[string]interface{}, error) {
	schema := make(map[string]interface{})

	// Add field description if available
	if desc := b.extractComments(field); desc != "" {
		schema["description"] = desc
	}

	// Handle repeated fields
	if field.IsList() {
		itemSchema, err := b.extractFieldTypeSchemaInternal(field, visited)
		if err != nil {
			return nil, err
		}

		schema["type"] = "array"
		schema["items"] = itemSchema
		return schema, nil
	}

	// Handle map fields
	if field.IsMap() {
		valueField := field.MapValue()
		valueSchema, err := b.extractFieldTypeSchemaInternal(valueField, visited)
		if err != nil {
			return nil, err
		}

		schema["type"] = "object"
		schema["patternProperties"] = map[string]interface{}{
			".*": valueSchema,
		}
		schema["additionalProperties"] = false
		return schema, nil
	}

	// Handle regular fields
	return b.extractFieldTypeSchemaInternal(field, visited)
}

// extractFieldTypeSchemaInternal generates schema for the field's type with circular reference detection
func (b *MCPToolBuilder) extractFieldTypeSchemaInternal(field protoreflect.FieldDescriptor, visited map[string]bool) (map[string]interface{}, error) {
	schema := make(map[string]interface{})

	switch field.Kind() {
	case protoreflect.BoolKind:
		schema["type"] = "boolean"

	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		schema["type"] = "integer"
		schema["format"] = "int32"

	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		schema["type"] = "integer"
		schema["format"] = "int64"

	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		schema["type"] = "integer"
		schema["format"] = "uint32"
		schema["minimum"] = 0

	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		schema["type"] = "integer"
		schema["format"] = "uint64"
		schema["minimum"] = 0

	case protoreflect.FloatKind:
		schema["type"] = "number"
		schema["format"] = "float"

	case protoreflect.DoubleKind:
		schema["type"] = "number"
		schema["format"] = "double"

	case protoreflect.StringKind:
		schema["type"] = "string"

	case protoreflect.BytesKind:
		schema["type"] = "string"
		schema["format"] = "byte"

	case protoreflect.EnumKind:
		enumDesc := field.Enum()
		enumValues := []interface{}{}
		enumDescriptions := make(map[string]string)

		for i := 0; i < enumDesc.Values().Len(); i++ {
			enumValue := enumDesc.Values().Get(i)
			valueName := string(enumValue.Name())
			enumValues = append(enumValues, valueName)

			// Add enum value description if available
			if desc := b.extractComments(enumValue); desc != "" {
				enumDescriptions[valueName] = desc
			}
		}

		schema["type"] = "string"
		schema["enum"] = enumValues

		// Add enum description if available
		if desc := b.extractComments(enumDesc); desc != "" {
			schema["description"] = desc
		}

		// Add enum value descriptions
		if len(enumDescriptions) > 0 {
			schema["enumDescriptions"] = enumDescriptions
		}

	case protoreflect.MessageKind:
		msgDesc := field.Message()

		// Handle well-known types
		switch msgDesc.FullName() {
		case "google.protobuf.Any":
			schema["type"] = "object"
			schema["description"] = "Any contains an arbitrary serialized protocol buffer message"

		case "google.protobuf.Timestamp":
			schema["type"] = "string"
			schema["format"] = "date-time"
			schema["description"] = "RFC 3339 formatted timestamp"

		case "google.protobuf.Duration":
			schema["type"] = "string"
			schema["format"] = "duration"
			schema["description"] = "Duration in seconds with up to 9 fractional digits"

		case "google.protobuf.Struct":
			schema["type"] = "object"
			schema["description"] = "Arbitrary JSON-like structure"

		case "google.protobuf.Value":
			schema["description"] = "Any JSON value"

		case "google.protobuf.ListValue":
			schema["type"] = "array"
			schema["description"] = "Array of JSON values"

		case "google.protobuf.StringValue",
			"google.protobuf.BytesValue":
			schema["type"] = "string"

		case "google.protobuf.BoolValue":
			schema["type"] = "boolean"

		case "google.protobuf.Int32Value",
			"google.protobuf.UInt32Value",
			"google.protobuf.Int64Value",
			"google.protobuf.UInt64Value":
			schema["type"] = "integer"

		case "google.protobuf.FloatValue",
			"google.protobuf.DoubleValue":
			schema["type"] = "number"

		default:
			// Custom message type - extract schema recursively
			messageSchema, err := b.extractMessageSchemaInternal(msgDesc, visited)
			if err != nil {
				return nil, fmt.Errorf("failed to extract schema for message %s: %w", msgDesc.FullName(), err)
			}
			return messageSchema, nil
		}

	default:
		return nil, fmt.Errorf("unsupported field kind: %v", field.Kind())
	}

	return schema, nil
}

// ExtractFieldComments extracts field description from comments (trimmed)
func (b *MCPToolBuilder) ExtractFieldComments(field protoreflect.FieldDescriptor) string {
	return strings.TrimSpace(b.extractComments(field))
}

// extractComments extracts comments from a protobuf descriptor
func (b *MCPToolBuilder) extractComments(desc protoreflect.Descriptor) string {
	// Get source location info if available
	loc := desc.ParentFile().SourceLocations().ByDescriptor(desc)
	comments := ""

	// Leading comments
	if leading := loc.LeadingComments; leading != "" {
		comments = leading
	}

	// Trailing comments (append with newline if we have leading comments)
	if trailing := loc.TrailingComments; trailing != "" {
		if comments != "" {
			comments += "\n" + trailing
		} else {
			comments = trailing
		}
	}

	return comments
}
