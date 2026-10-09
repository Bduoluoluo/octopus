package outbound

import (
	"net/http"

	"github.com/xuanli27/octopus/internal/transformer/model"
)

func applyOpenAIOrgProjectHeaders(request *http.Request, internal *model.InternalLLMRequest) {
	if request == nil || internal == nil {
		return
	}
	if organization := internal.TransformerMetadataValue(model.TransformerMetadataOpenAIOrganization); organization != "" {
		request.Header.Set("OpenAI-Organization", organization)
	}
	if project := internal.TransformerMetadataValue(model.TransformerMetadataOpenAIProject); project != "" {
		request.Header.Set("OpenAI-Project", project)
	}
}
