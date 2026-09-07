package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/workflow"
)

// ErrExtractionUnsupported is returned by ExtractFields when llm does not
// implement rag.StructuredLLMClient — the same "schema-constrained output
// unavailable" signal go-rag's route package uses for its own extraction
// call. Callers should surface this as "this document type needs an
// LLM-capable model configured," not retry.
var ErrExtractionUnsupported = errors.New("importer: llm does not support structured output (rag.StructuredLLMClient)")

// ExtractFields asks llm to fill a workflow's custom fields from free-form
// document text (DOCX/PDF, via parse.Document.Text) — a JSON Schema built
// from defs (field key, type, enum options) constrains the model to emit
// exactly the fields the workflow defines, nothing else, the same
// technique go-rag's route package uses for query routing. A field the
// model can't find in the text is simply absent from the result, the same
// as an empty cell in a CSV row — never guessed.
func ExtractFields(ctx context.Context, llm ragcore.LLMClient, defs []workflow.FieldDefinition, text string) (map[string]any, error) {
	structured, ok := llm.(ragcore.StructuredLLMClient)
	if !ok {
		return nil, ErrExtractionUnsupported
	}
	if len(defs) == 0 {
		return map[string]any{}, nil
	}

	const system = `Extract the requested fields from the document text below. Respond with JSON matching the schema only, no prose. Omit a field entirely if the document does not state it; never guess or invent a value.`
	msg := ragcore.Message{Role: "user", Content: "Document text:\n" + text}

	raw, err := structured.ChatJSON(ctx, system, []ragcore.Message{msg}, buildFieldSchema(defs))
	if err != nil {
		return nil, fmt.Errorf("importer: extract fields: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("importer: malformed extraction output: %w", err)
	}
	return out, nil
}

// buildFieldSchema builds a JSON Schema object whose properties are exactly
// defs' keys, typed per DataType — an enum field's Options become a JSON
// Schema enum, constraining the model to one of the workflow's own allowed
// values rather than free text.
func buildFieldSchema(defs []workflow.FieldDefinition) []byte {
	props := make(map[string]any, len(defs))
	for _, d := range defs {
		prop := map[string]any{"type": jsonSchemaType(d.DataType)}
		if d.DataType == workflow.TypeEnum && len(d.Options) > 0 {
			prop["enum"] = d.Options
		}
		props[d.Key] = prop
	}
	schema := map[string]any{"type": "object", "properties": props}
	b, _ := json.Marshal(schema) // props are built from plain Go values above; Marshal cannot fail here
	return b
}

// jsonSchemaType maps a workflow.DataType to its JSON Schema type name.
func jsonSchemaType(dt workflow.DataType) string {
	switch dt {
	case workflow.TypeNumber:
		return "number"
	case workflow.TypeBool:
		return "boolean"
	default: // string, date, email, enum
		return "string"
	}
}
