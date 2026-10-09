package openaichat

type ReasoningField string

const (
	ReasoningFieldAll       ReasoningField = "all"
	ReasoningFieldContent   ReasoningField = "content"
	ReasoningFieldReasoning ReasoningField = "reasoning"
	ReasoningFieldNone      ReasoningField = "none"
)

type ToolCallGoogleExtraContent struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}
