package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/yanpgwang/mango/internal/mcpclient"
	"github.com/yanpgwang/mango/internal/sandbox"
)

// ProjectMCPResult separates a protocol-native MCP result from the content sent
// to the model. _meta and other control fields remain only in raw/rawPath;
// text and structured content are projected, while binary content is written to
// the Session sandbox and represented by a readable path.
func ProjectMCPResult(
	ctx context.Context,
	sb sandbox.Sandbox,
	toolUseID string,
	input mcpclient.Result,
) (result Result, raw json.RawMessage, rawPath string, err error) {
	var wire struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent any               `json:"structuredContent"`
	}
	if err := json.Unmarshal(input.Raw, &wire); err != nil {
		return Result{}, nil, "", fmt.Errorf("decode MCP result: %w", err)
	}
	var textParts []string
	for index, content := range wire.Content {
		var block map[string]any
		if err := json.Unmarshal(content, &block); err != nil {
			return Result{}, nil, "", fmt.Errorf(
				"decode MCP content block %d: %w",
				index,
				err,
			)
		}
		typ, _ := block["type"].(string)
		switch typ {
		case "text":
			if text, _ := block["text"].(string); text != "" {
				textParts = append(textParts, text)
			}
		case "image", "audio":
			location, err := persistMCPBase64(
				ctx,
				sb,
				toolUseID,
				index,
				typ,
				block,
			)
			if err != nil {
				return Result{}, nil, "", err
			}
			textParts = append(
				textParts,
				fmt.Sprintf("MCP %s content saved to %s", typ, location),
			)
		case "resource":
			resource, _ := block["resource"].(map[string]any)
			uri, _ := resource["uri"].(string)
			if text, _ := resource["text"].(string); text != "" {
				if uri != "" {
					textParts = append(textParts, "Resource "+uri+":\n"+text)
				} else {
					textParts = append(textParts, text)
				}
				continue
			}
			if _, ok := resource["blob"].(string); ok {
				location, err := persistMCPBase64(
					ctx,
					sb,
					toolUseID,
					index,
					"resource",
					resource,
				)
				if err != nil {
					return Result{}, nil, "", err
				}
				textParts = append(
					textParts,
					fmt.Sprintf("MCP resource %s saved to %s", uri, location),
				)
			}
		case "resource_link":
			uri, _ := block["uri"].(string)
			name, _ := block["name"].(string)
			textParts = append(
				textParts,
				fmt.Sprintf("MCP resource link %s: %s", name, uri),
			)
		default:
			// Unknown protocol content remains available in the raw diagnostic
			// result but is not implicitly trusted as model context. In
			// particular, this prevents future control/_meta fields from becoming
			// prompt content before the projection policy understands them.
			if typ != "" {
				textParts = append(
					textParts,
					"MCP returned unsupported "+typ+" content.",
				)
			}
		}
	}
	if wire.StructuredContent != nil {
		structured, err := json.MarshalIndent(wire.StructuredContent, "", "  ")
		if err != nil {
			return Result{}, nil, "", fmt.Errorf(
				"encode MCP structured content: %w",
				err,
			)
		}
		textParts = append(textParts, "Structured content:\n"+string(structured))
	}
	if len(textParts) == 0 {
		textParts = append(textParts, "MCP tool returned no model-visible content.")
	}
	result = textResult(strings.Join(textParts, "\n\n"), input.IsError)
	result, err = MaterializeLargeResult(ctx, sb, toolUseID, result)
	if err != nil {
		return Result{}, nil, "", err
	}

	if len(input.Raw) > MaxInlineResultChars {
		rawPath = path.Join(
			ToolResultsDirectory,
			safeResultFilename(toolUseID)+".mcp.json",
		)
		if err := sb.WriteFile(ctx, rawPath, input.Raw); err != nil {
			return Result{}, nil, "", fmt.Errorf(
				"persist raw MCP result %q: %w",
				rawPath,
				err,
			)
		}
	} else {
		raw = append(json.RawMessage(nil), input.Raw...)
	}
	return result, raw, rawPath, nil
}

func persistMCPBase64(
	ctx context.Context,
	sb sandbox.Sandbox,
	toolUseID string,
	index int,
	kind string,
	block map[string]any,
) (string, error) {
	encoded, _ := block["data"].(string)
	if encoded == "" {
		encoded, _ = block["blob"].(string)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode MCP %s content: %w", kind, err)
	}
	mimeType, _ := block["mimeType"].(string)
	extension := extensionForMIME(mimeType)
	location := path.Join(
		ToolResultsDirectory,
		fmt.Sprintf("%s-%d%s", safeResultFilename(toolUseID), index, extension),
	)
	if err := sb.WriteFile(ctx, location, data); err != nil {
		return "", fmt.Errorf("persist MCP %s content: %w", kind, err)
	}
	return location, nil
}

func extensionForMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "application/json":
		return ".json"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}
