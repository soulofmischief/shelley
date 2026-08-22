package modelsources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models"
)

func TestBuildChatGPTUsesAuthenticatedDynamicCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.URL.Query().Get("client_version") != "1.2.3" {
			t.Fatalf("request URL = %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("ChatGPT-Account-ID") != "account" {
			t.Fatalf("auth headers = %v", r.Header)
		}
		json.NewEncoder(w).Encode(ChatGPTCatalog{Models: []ChatGPTModel{
			{
				Slug:                  "gpt-5.6-sol",
				DisplayName:           "GPT-5.6 Sol",
				DefaultReasoningLevel: "high",
				SupportedReasoningLevels: []ChatGPTReasoningLevel{
					{Effort: "none"}, {Effort: "low"}, {Effort: "high"}, {Effort: "max"},
				},
				Visibility:         "list",
				SupportedInAPI:     true,
				ServiceTiers:       []ChatGPTModelServiceTier{{ID: "fast"}},
				InputModalities:    []string{"text", "image"},
				ContextWindow:      300000,
				ApplyPatchToolType: ptr("freeform"),
			},
			{Slug: "hidden", Visibility: "hide", SupportedInAPI: true},
			{Slug: "unsupported", Visibility: "list", SupportedInAPI: false},
		}})
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = authHeaderTransport{base: client.Transport}
	built, err := BuildChatGPT(context.Background(), models.All(), server.URL, "v1.2.3", client, map[string]bool{"gpt-5.6-sol": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 {
		t.Fatalf("models = %+v", built)
	}
	if built[0].ID != "gpt-5.6-sol@chatgpt" || built[0].DisplayName != "GPT-5.6 Sol (ChatGPT)" {
		t.Fatalf("built model = %+v", built[0])
	}
	service, ok := built[0].Service.(*oai.ResponsesService)
	if !ok {
		t.Fatalf("service = %T", built[0].Service)
	}
	if service.ReasoningEffort != "high" || service.TokenContextWindow() != 300000 || !service.SupportsServiceTier(llm.ServiceTierFast) {
		t.Fatalf("service capabilities = %+v", service)
	}
	wantLevels := []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelLow, llm.ThinkingLevelHigh, llm.ThinkingLevelMax}
	gotLevels := service.SupportedReasoningLevels()
	if len(gotLevels) != len(wantLevels) {
		t.Fatalf("reasoning levels = %v", gotLevels)
	}
	for i := range wantLevels {
		if gotLevels[i] != wantLevels[i] {
			t.Fatalf("reasoning levels = %v", gotLevels)
		}
	}
	if !service.SupportsImages() || service.PatchProfile() != "codex_apply_patch" || !service.OmitMaxOutputTokens {
		t.Fatalf("service profile = %+v", service)
	}
}

func TestBuildChatGPTPreservesUnknownModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ChatGPTCatalog{Models: []ChatGPTModel{{
			Slug: "future-codex", DisplayName: "Future Codex", Visibility: "list", SupportedInAPI: true,
			SupportedReasoningLevels: []ChatGPTReasoningLevel{{Effort: "medium"}},
			InputModalities:          []string{"text"},
		}}})
	}))
	defer server.Close()

	built, err := BuildChatGPT(context.Background(), models.All(), server.URL, "dev", server.Client(), map[string]bool{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != 1 || built[0].ID != "future-codex" {
		t.Fatalf("models = %+v", built)
	}
	service := built[0].Service.(*oai.ResponsesService)
	if service.SupportsImages() || service.Model.ModelName != "future-codex" {
		t.Fatalf("unknown model service = %+v", service)
	}
}

type authHeaderTransport struct{ base http.RoundTripper }

func (t authHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer access")
	req.Header.Set("ChatGPT-Account-ID", "account")
	return t.base.RoundTrip(req)
}

func ptr[T any](value T) *T { return &value }
