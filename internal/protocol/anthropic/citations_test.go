package anthropic

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCitationUnionRoundTrip(t *testing.T) {
	fixtures := []string{
		`{"type":"document","source":{"type":"text","media_type":"text/plain","data":"source"},"citations":{"enabled":true}}`,
		`{"type":"document","citations":{"enabled":false,"future":0}}`,
		`{"type":"document","citations":{"enabled":null}}`,
		`{"type":"text","text":"claim","citations":[{"type":"char_location","document_index":0,"document_title":"source","start_char_index":0,"end_char_index":6,"cited_text":"source","future":false}]}`,
		`{"type":"text","text":"claim","citations":[{"type":"page_location","document_index":0,"start_page_number":0,"end_page_number":1}]}`,
		`{"type":"text","text":"claim","citations":[{"type":"content_block_location","document_index":0,"start_block_index":0,"end_block_index":1}]}`,
		`{"type":"text","text":"claim","citations":[{"type":"search_result_location","search_result_index":0,"start_block_index":0,"end_block_index":1,"source":"fixture"}]}`,
		`{"type":"text","text":"claim","citations":[{"type":"web_search_result_location","url":"https://example.invalid","title":"source","encrypted_index":"opaque"}]}`,
		`{"type":"text","text":""}`,
		`{"type":"text","citations":null}`,
		`{"type":"text","citations":[]}`,
		`{"type":"document","citations":null}`,
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			var block MessageContentBlock
			if err := json.Unmarshal([]byte(fixture), &block); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(block)
			if err != nil {
				t.Fatal(err)
			}
			var expected, actual any
			if err := json.Unmarshal([]byte(fixture), &expected); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &actual); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("round trip: %s", encoded)
			}
		})
	}
}

func TestCitationUnionRejectsWrongShapes(t *testing.T) {
	for _, fixture := range []string{`{"type":"document","citations":[]}`, `{"type":"text","citations":{"enabled":true}}`, `{"type":"text","citations":false}`, `{"type":"document","citations":42}`} {
		var block MessageContentBlock
		if err := json.Unmarshal([]byte(fixture), &block); err == nil {
			t.Fatalf("accepted invalid block: %s", fixture)
		}
	}
}

func TestAssistantHistoryCitations(t *testing.T) {
	var request MessageRequest
	err := json.Unmarshal([]byte(`{"model":"fixture","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"text","text":"claim","citations":[{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":6}]}]}]}`), &request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded MessageRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages[0].Content.MultipleContent[0].Citations) != 1 {
		t.Fatalf("lost history citations: %s", encoded)
	}
}

func FuzzCitationUnion(f *testing.F) {
	f.Add([]byte(`{"type":"text","citations":[{"type":"char_location","document_index":0}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var block MessageContentBlock
		if json.Unmarshal(data, &block) != nil {
			return
		}
		encoded, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		var decoded MessageContentBlock
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
	})
}
