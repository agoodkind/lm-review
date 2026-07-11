package lmstudio

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

const reviewResultJSONSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": {
      "type": "string",
      "enum": ["pass", "warn", "block", "skip"]
    },
    "summary": {
      "type": "string"
    },
    "issues": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "severity": {
            "type": "string",
            "enum": ["error", "warning", "info"]
          },
          "category": {
            "type": "string",
            "enum": [
              "style",
              "security",
              "performance",
              "correctness",
              "readability",
              "maintainability",
              "dependency",
              "testing"
            ]
          },
          "file": {
            "type": "string"
          },
          "line": {
            "type": "integer"
          },
          "end_line": {
            "type": "integer"
          },
          "rule": {
            "type": "string"
          },
          "message": {
            "type": "string"
          },
          "suggestion": {
            "type": "string"
          },
          "confidence": {
            "type": "string",
            "enum": ["high", "medium", "low"]
          }
        },
        "required": ["severity", "file", "line", "rule", "message"]
      }
    },
    "stats": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "errors": {
          "type": "integer"
        },
        "warnings": {
          "type": "integer"
        },
        "infos": {
          "type": "integer"
        }
      },
      "required": ["errors", "warnings", "infos"]
    },
    "highlights": {
      "type": "array",
      "items": {
        "type": "string"
      }
    },
    "tech_debt": {
      "type": "string"
    }
  },
  "required": ["verdict", "summary", "issues"]
}`

func reviewResultResponseFormat() openai.ChatCompletionNewParamsResponseFormatUnion {
	responseFormat := jsonSchemaResponseFormat("review_result", json.RawMessage(reviewResultJSONSchema))
	responseFormat.OfJSONSchema.JSONSchema.Description = param.NewOpt("Structured review result payload.")
	return responseFormat
}

func jsonSchemaResponseFormat(name string, schema json.RawMessage) openai.ChatCompletionNewParamsResponseFormatUnion {
	return openai.ChatCompletionNewParamsResponseFormatUnion{
		OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
			JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
				Name:   name,
				Schema: append(json.RawMessage(nil), schema...),
				Strict: param.NewOpt(true),
			},
		},
	}
}
