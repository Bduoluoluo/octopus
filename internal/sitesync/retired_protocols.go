package sitesync

import (
	"strings"

	"github.com/xuanli27/octopus/internal/model"
)

func isRetiredSiteRoute(routeType model.SiteModelRouteType) bool {
	return routeType == model.SiteModelRouteTypeGemini || routeType == model.SiteModelRouteTypeVolcengine || routeType == model.SiteModelRouteTypeOpenAIEmbedding
}

func hasRetiredEndpointTypes(values []string) bool {
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch {
		case normalized == "embedding", normalized == "embeddings", normalized == "openai_embedding", normalized == "openai/embeddings", strings.Contains(normalized, "/v1/embeddings"),
			normalized == "gemini", normalized == "generatecontent", normalized == "streamgeneratecontent", normalized == "counttokens", strings.Contains(normalized, ":generatecontent"), strings.Contains(normalized, ":streamgeneratecontent"), strings.Contains(normalized, ":counttokens"),
			normalized == "volcengine", normalized == "ark", strings.Contains(normalized, "volcengine"):
			return true
		}
	}
	return false
}

func hasRetiredEndpointMetadata(rawPayload string) bool {
	metadata, ok := model.ParseSiteModelRouteMetadata(rawPayload)
	return ok && hasRetiredEndpointTypes(metadata.SupportedEndpointTypes) && !metadata.RouteSupported
}
