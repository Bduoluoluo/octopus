package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xuanli27/octopus/internal/model"
	"github.com/xuanli27/octopus/internal/transformer/outbound"
)

func normalizeBaseURL(baseURL, suffix string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if suffix == "" {
		return baseURL
	}
	suffix = strings.Trim(suffix, "/")
	if strings.HasSuffix(baseURL, "/"+suffix) {
		return baseURL
	}
	return baseURL + "/" + suffix
}

// testPrompt 濡€崇€峰ù瀣槸娴ｈ法鏁ら惃鍕祼鐎?prompt閵嗗倸鎮楃粩顖溾€栫紓鏍垳,娑撳秵甯撮崣妤€澧犵粩顖濐洬閻?闁灝鍘ょ悮顐ｄ簰閻劊鈧?
const testPrompt = "translate `octopus` to Espa甯給l"

// testMaxTokens 閸氬嫬宕楃拋顔炬畱閺堚偓婢堆嗙翻閸?tokens 閸婄鈧?
// Anthropic 閸楀繗顔呭鍝勫煑鐟曚焦鐪?max_tokens,閹碘偓娴犮儱鍙忛柈銊ュ礂鐠侇噣鍏樻导鐘插弳閻╃鎮撻惃鍕嚔娑斿鈧?
// 閸氬嫬宕楃拋顔炬畱閸忚渹缍嬬€涙顔岄崥?max_tokens / max_output_tokens / maxOutputTokens)閸?build 閸戣姤鏆熼柌灞藉隘閸掑棎鈧?
const testMaxTokens = 1024

// TestModelResult 閸楁洑閲滃Ο鈥崇€烽惃鍕ゴ鐠囨洜绮ㄩ弸婧库偓?
// 娴犲懍缍旀稉鐑樻拱濞嗏剝绁寸拠鏇犳畱閻剚妞傛穱鈥冲娇,娑撳秵瀵旀稊鍛;閸撳秶顏导姘崇樈閸愬懐顓搁悶鍡愨偓?
type TestModelResult struct {
	Model      string `json:"model"`
	StatusCode int    `json:"status_code"` // 娑撳﹥鐖?HTTP 閻樿埖鈧胶鐖?缂冩垹绮堕柨娆掝嚖/鐡掑懏妞?濞屸剝瀣侀崚鏉挎惙鎼?閺冩湹璐?0
	DelayMS    int64  `json:"delay_ms"`
	Error      string `json:"error,omitempty"`
}

// TestChannelModel 閻劍娓剁亸?chat 鐠囬攱鐪板ù瀣槸濞撶娀浜炬稉濠勬畱閹稿洤鐣惧Ο鈥崇€?鏉╂柨娲栧鎯扮箿娑撳海濮搁幀浣碘偓?
// 娑撳秷铔?relay pipeline(濞屸剝婀侀柌宥堢槸/閻旀梹鏌?缂佺喕顓?,娴犲懎浠涙稉鈧▎鈩冣偓褏婀＄€?API 鐠嬪啰鏁ら妴?
//
// channel 閸欘垯浜掗弰顖氬嚒娣囨繂鐡ㄩ惃?閺?ID),娑旂喎褰叉禒銉︽Ц閺傛澘缂撳鍦崶闁插本婀穱婵嗙摠閻ㄥ嫪澶嶉弮璺侯嚠鐠灺扳偓?
// keyIndex 閺?channel.Keys 閺佹壆绮嶆稉瀣垼,姒涙顓?0;
// 閻㈣精鐨熼悽銊︽煙娣囨繆鐦?0 <= keyIndex < len(channel.Keys)閵?
//
// NOTE(security): channel 娑擃厾娈?ChannelKey 閸欘垵鍏橀弰顖涙閺?閺堫剙鍤遍弫棰佺瑝婢舵牔绱堕崚鏉挎惙鎼?
// 娴犲懐鏁ゆ禍搴㈢€柅鐘差嚠娑撳﹥鐖堕惃?Authorization header閵嗗倸顓哥拋鈩冩）鐢晲绗夐崘娆忓弳閺冦儱绻旈妴?
// 瀹歌尙鐓￠梻顕€顣?`GET /api/v1/channel/list` 閺堫剙姘ㄩ崶鐐扮炊閺勫孩鏋?key,閺堫剙鍤遍弫棰佺瑝閹泛瀵叉担鍡曠瘍娑撳秳鎱ㄦ径宥堫嚉闂?
// 鐎瑰本鏆ｉ弫瀛樻暭鐟?/workspace/octopus鐎瑰鍙忓▔鍕苟闂傤噣顣?md閵?
func TestChannelModel(
	ctx context.Context,
	channel *model.Channel,
	modelName string,
	keyIndex int,
	timeout time.Duration,
) (*TestModelResult, error) {
	result := &TestModelResult{Model: modelName}

	// 閺嶏繝鐛欑拠閿嬬湴
	if channel == nil {
		return nil, errors.New("channel is nil")
	}
	if modelName == "" {
		return nil, errors.New("model is empty")
	}
	if channel.Type == outbound.OutboundTypeOpenAIEmbedding {
		return nil, fmt.Errorf("channel type %d does not support model testing", channel.Type)
	}
	baseURL := channel.GetBaseUrl()
	if baseURL == "" {
		return nil, errors.New("no valid base url")
	}
	if keyIndex < 0 || keyIndex >= len(channel.Keys) {
		return nil, fmt.Errorf("key_index %d out of range (have %d keys)", keyIndex, len(channel.Keys))
	}
	key := strings.TrimSpace(channel.Keys[keyIndex].ChannelKey)
	if key == "" {
		return nil, errors.New("selected key is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 閹稿绗傚〒绋垮礂鐠侇喗鐎柅鐘侯嚞濮?
	req, err := buildTestRequest(ctx, channel.Type, baseURL, key, modelName)
	if err != nil {
		return nil, err
	}
	// 閼奉亜鐣炬稊?Header 閺堚偓閸氬骸绨查悽顭掔礉閸忎浇顔忓ù瀣槸娑撳骸鐤勯梽鍛版祮閸欐垳濞囬悽銊ф祲閸氬瞼娈戦弬鏉款杻閵嗕浇顩惄鏍モ偓浣衡敄閸婄厧鎷伴崚鐘绘珟鐠囶厺绠熼妴?
	for _, header := range channel.CustomHeader {
		if name := strings.TrimSpace(header.HeaderKey); name != "" {
			req.Header.Set(name, header.HeaderValue)
		}
	}

	// 閸欐垿鈧?
	httpClient, err := ChannelHTTPClientWithContext(ctx, channel)
	if err != nil {
		return nil, fmt.Errorf("build http client: %w", err)
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	result.DelayMS = time.Since(start).Milliseconds()
	if err != nil {
		// 缂冩垹绮堕柨娆掝嚖 / DNS 婢惰精瑙?/ 鐡掑懏妞傜粵?閹峰じ绗夐崚?HTTP 閸濆秴绨?StatusCode 娣囨繃瀵?0
		result.Error = truncateErr(err.Error(), 200)
		return result, nil
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode

	// 鐠?body 閻劋绨崚銈嗘焽;闂?2xx 閺冭埖濡告稉濠冪埗闁挎瑨顕ら幗妯款洣閺€鎹愮箻 Error
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if readErr != nil {
		result.Error = truncateErr(fmt.Sprintf("read body: %v", readErr), 200)
		return result, nil
	}

	if resp.StatusCode >= 400 {
		result.Error = summarizeUpstreamError(resp.StatusCode, bodyBytes)
	}
	return result, nil
}

// buildTestRequest 閹稿绗傚〒绋垮礂鐠侇喗鐎柅鐘虫付鐏忓繒娈?chat 鐠囬攱鐪?閸欘亜鎯堣箛鍛綖 + max_tokens 鐎涙顔岄妴?
func buildTestRequest(
	ctx context.Context,
	channelType outbound.OutboundType,
	baseURL, key, modelName string,
) (*http.Request, error) {
	switch channelType {
	case outbound.OutboundTypeOpenAIChat:
		// POST {url}/v1/chat/completions
		// body: {model, messages:[{role:user, content}], max_tokens}
		url := normalizeBaseURL(baseURL, "v1") + "/chat/completions"
		body := map[string]any{
			"model": modelName,
			"messages": []map[string]string{
				{"role": "user", "content": testPrompt},
			},
			"max_tokens": testMaxTokens,
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+key)
		})

	case outbound.OutboundTypeOpenAIResponse:
		// POST {url}/v1/responses
		// body: {model, input, max_output_tokens}
		url := normalizeBaseURL(baseURL, "v1") + "/responses"
		body := map[string]any{
			"model":             modelName,
			"input":             testPrompt,
			"max_output_tokens": testMaxTokens,
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+key)
		})

	case outbound.OutboundTypeAnthropic:
		// POST {url}/v1/messages
		// body: {model, messages:[{role:user, content}], max_tokens}
		// headers: x-api-key, anthropic-version (韫囧懎锝?
		url := normalizeBaseURL(baseURL, "v1") + "/messages"
		body := map[string]any{
			"model": modelName,
			"messages": []map[string]string{
				{"role": "user", "content": testPrompt},
			},
			"max_tokens": testMaxTokens,
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("X-Api-Key", key)
			r.Header.Set("Anthropic-Version", "2023-06-01")
		})

	case outbound.OutboundTypeGemini:
		// POST {url}/v1beta/models/{model}:generateContent
		// headers: X-Goog-Api-Key
		// body: {contents:[{parts:[{text}]}], generationConfig:{maxOutputTokens}}
		// Gemini transformer 婢跺嫮鎮婃潻鍥ㄦ▔瀵?/v1 閸氬海绱戦弮鏈电瑝閸愬秵瀚?v1beta,鏉╂瑩鍣锋穱婵囧瘮娑撯偓閼锋番鈧?
		var url string
		if strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/v1") {
			url = normalizeBaseURL(baseURL, "") + "/models/" + modelName + ":generateContent"
		} else {
			url = normalizeBaseURL(baseURL, "v1beta") + "/models/" + modelName + ":generateContent"
		}
		body := map[string]any{
			"contents": []map[string]any{
				{"parts": []map[string]string{{"text": testPrompt}}},
			},
			"generationConfig": map[string]any{
				"maxOutputTokens": testMaxTokens,
			},
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("X-Goog-Api-Key", key)
		})

	case outbound.OutboundTypeVolcengine:
		// POST {url}/v3/chat/completions  鐠炲棗瀵橀崡蹇氼唴閸?OpenAI Chat Completions,娴?base 姒涙顓?/v3
		url := normalizeBaseURL(baseURL, "v3") + "/chat/completions"
		body := map[string]any{
			"model": modelName,
			"messages": []map[string]string{
				{"role": "user", "content": testPrompt},
			},
			"max_tokens": testMaxTokens,
		}
		return newJSONRequest(ctx, http.MethodPost, url, body, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+key)
		})

	default:
		return nil, fmt.Errorf("unsupported channel type: %d", channelType)
	}
}

// newJSONRequest 閹?body 鎼村繐鍨崠鏍﹁礋 JSON,閺嬪嫰鈧?POST 鐠囬攱鐪?Content-Type 閼奉亜濮╃拋鍓х枂閵?
// extraHeaders 閻劋绨幐澶婂礂鐠侇喖鍨庨弨顖澦夐崗鍛村閺夊啰鐡?header閵?
func newJSONRequest(
	ctx context.Context,
	method, url string,
	body any,
	extraHeaders func(*http.Request),
) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if extraHeaders != nil {
		extraHeaders(req)
	}
	return req, nil
}

// summarizeUpstreamError 娴犲酣娼?2xx 閸濆秴绨?body 闁插本褰侀崣鏍暆鐟曚線鏁婄拠?鏉╂柨娲栨稉宥堢Т鏉?200 鐎涙顑侀惃鍕伎鏉╄埇鈧?
// 闁氨鏁ら崗婊冪俺:閻樿埖鈧胶鐖?+ body 婢舵潙鍤戞稉顏勭摟缁?闁灝鍘ゅ▔鍕苟鏉╁洤顦挎稉濠冪埗閺佸繑鍔呮穱鈩冧紖閵?
func summarizeUpstreamError(statusCode int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		return fmt.Sprintf("upstream returned %d with empty body", statusCode)
	}
	// 鐏忔繆鐦憴锝嗙€介柅姘辨暏 error message 缂佹挻鐎?OpenAI / Anthropic / Gemini 婢堆傜秼閺?{error: {message:...}} 閻ㄥ嫭鐓囩粔宥呭綁娴?
	var probe struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"` // Gemini 妞嬪孩鐗?{message: ..., code: ...}
	}
	if jsonErr := json.Unmarshal(body, &probe); jsonErr == nil {
		msg := probe.Error.Message
		if msg == "" {
			msg = probe.Message
		}
		if msg != "" {
			return truncateErr(fmt.Sprintf("%d: %s", statusCode, msg), 200)
		}
	}
	// 鐟欙絾鐎芥稉宥呭毉閺夈儱姘ㄩ幋顏呮焽 raw body
	return truncateErr(fmt.Sprintf("%d: %s", statusCode, trimmed), 200)
}

func truncateErr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
