package beeapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/BeeAPI-AI/beeapi/internal/reasoning"
)

func TestModelOptionsRetainsProtocolScopedReasoning(t *testing.T) {
	client := New("https://beeapi.test")
	client.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/client/model-options" || request.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatal("changed model-options authentication or route")
		}
		body := `{"code":0,"data":{"models":[{"id":"coding-alias","protocols":["openai/responses","openai/chat_completions"],"capabilities":["reasoning"],"priority":91,"reasoning":{"openai/responses":{"mode":"effort","supported_efforts":["low","high","max"],"default_effort":"high"},"openai/chat_completions":{"mode":"effort","supported_efforts":["high"],"default_effort":"high"}}},{"id":"legacy-model","protocols":["openai/responses"]}]}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	options, err := client.ModelOptions(context.Background(), "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[0].Priority != 91 || options[1].Reasoning != nil {
		t.Fatalf("lost compatibility/order: %#v", options)
	}
	if !reflect.DeepEqual(options[0].Reasoning[reasoning.Responses].Efforts, []string{"low", "high", "max"}) || !reflect.DeepEqual(options[0].Reasoning[reasoning.Chat].Efforts, []string{"high"}) {
		t.Fatalf("lost per-protocol restrictions: %+v", options[0].Reasoning)
	}
}

func TestOptionalReasoningDistinguishesLegacyFromDisabled(t *testing.T) {
	for _, tt := range []struct {
		raw    string
		legacy bool
	}{
		{`{"id":"x"}`, true}, {`{"id":"x","reasoning":null}`, true},
		{`{"id":"x","reasoning":{}}`, false},
		{`{"id":"x","reasoning":{"openai/responses":{"mode":"none"}}}`, false},
	} {
		var model ModelOption
		if err := json.Unmarshal([]byte(tt.raw), &model); err != nil {
			t.Fatal(err)
		}
		if (model.Reasoning == nil) != tt.legacy {
			t.Fatalf("unexpected optional-field semantics: %s", tt.raw)
		}
	}
}
