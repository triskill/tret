package brain

// BashToolName is the single tool exposed to the model (D7).
const BashToolName = "bash"

// bashToolDef is the OpenAI function-calling schema for the bash tool
// (also accepted by Ollama's OpenAI-compatible endpoint).
var bashToolDef = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        BashToolName,
		"description": "Run a bash command in the workspace. Returns stdout, stderr and exit code.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The bash command line to run",
				},
			},
			"required": []string{"command"},
			// D7: exactly one tool, max one call per decision.
			"additionalProperties": false,
		},
	},
}
