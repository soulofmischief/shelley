package modelsources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"shelley.exe.dev/chatgptauth"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/oai"
	"shelley.exe.dev/models"
)

type ChatGPTCatalog struct {
	Models []ChatGPTModel `json:"models"`
}

type ChatGPTModel struct {
	Slug                     string                    `json:"slug"`
	DisplayName              string                    `json:"display_name"`
	Description              string                    `json:"description"`
	DefaultReasoningLevel    string                    `json:"default_reasoning_level"`
	SupportedReasoningLevels []ChatGPTReasoningLevel   `json:"supported_reasoning_levels"`
	Visibility               string                    `json:"visibility"`
	SupportedInAPI           bool                      `json:"supported_in_api"`
	Priority                 int                       `json:"priority"`
	AdditionalSpeedTiers     []string                  `json:"additional_speed_tiers"`
	ServiceTiers             []ChatGPTModelServiceTier `json:"service_tiers"`
	InputModalities          []string                  `json:"input_modalities"`
	ContextWindow            int                       `json:"context_window"`
	ApplyPatchToolType       *string                   `json:"apply_patch_tool_type"`
}

type ChatGPTReasoningLevel struct {
	Effort string `json:"effort"`
}

type ChatGPTModelServiceTier struct {
	ID string `json:"id"`
}

// BuildChatGPT fetches the authenticated Codex catalog and materializes the
// models the account is currently allowed to use. reservedIDs contains models
// already supplied by API keys or another source; colliding subscription models
// receive an @chatgpt suffix so both credentials remain selectable.
func BuildChatGPT(ctx context.Context, catalog []models.Model, baseURL, clientVersion string, httpc *http.Client, reservedIDs map[string]bool, logger *slog.Logger) ([]models.Built, error) {
	if httpc == nil {
		return nil, fmt.Errorf("ChatGPT HTTP client is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	modelsURL, err := url.Parse(baseURL + "/models")
	if err != nil {
		return nil, fmt.Errorf("build ChatGPT models URL: %w", err)
	}
	query := modelsURL.Query()
	query.Set("client_version", normalizedClientVersion(clientVersion))
	modelsURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create ChatGPT models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch ChatGPT models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read ChatGPT models: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, chatgptauth.ErrNotAuthenticated
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ChatGPT models endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var response ChatGPTCatalog
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode ChatGPT models: %w", err)
	}

	known := make(map[string]models.Model)
	for _, model := range catalog {
		if model.Provider == models.ProviderOpenAI {
			known[model.APIModelName] = model
		}
	}
	built := make([]models.Built, 0, len(response.Models))
	for _, advertised := range response.Models {
		if advertised.Slug == "" || !advertised.SupportedInAPI {
			continue
		}
		if advertised.Visibility != "" && advertised.Visibility != "list" {
			continue
		}
		model := chatGPTWireModel(advertised, known[advertised.Slug])
		reasoningLevels := chatGPTReasoningLevels(advertised.SupportedReasoningLevels)
		serviceTiers := chatGPTServiceTiers(advertised)
		service := &oai.ResponsesService{
			HTTPC:               httpc,
			Model:               model,
			ModelURL:            baseURL,
			ThinkingLevel:       llm.ThinkingLevelDefault,
			ReasoningEffort:     advertised.DefaultReasoningLevel,
			ProviderName:        "openai",
			OmitMaxOutputTokens: true,
			ReasoningLevels:     reasoningLevels,
			ServiceTiers:        serviceTiers,
			ContextWindow:       advertised.ContextWindow,
		}
		id := advertised.Slug
		displayName := firstNonEmpty(advertised.DisplayName, advertised.Slug)
		if reservedIDs[id] {
			id += "@chatgpt"
			displayName += " (ChatGPT)"
		}
		built = append(built, models.Built{
			ID:          id,
			DisplayName: displayName,
			Provider:    models.ProviderOpenAI,
			Source:      "ChatGPT subscription",
			Tags:        known[advertised.Slug].Tags,
			ReleaseDate: modelReleaseDate(baseURL, advertised.Slug),
			Service:     service,
			APIType:     models.APITypeOpenAIResponses,
			BaseURL:     baseURL,
		})
		reservedIDs[id] = true
		logger.Debug("Materialized ChatGPT model", "id", id, "model", advertised.Slug)
	}
	return built, nil
}

func chatGPTWireModel(advertised ChatGPTModel, known models.Model) oai.Model {
	if known.Build != nil {
		if service, ok := known.Build("", "", nil).(*oai.ResponsesService); ok {
			model := service.Model
			model.ModelName = advertised.Slug
			model.SupportsImages = slices.Contains(advertised.InputModalities, "image") || len(advertised.InputModalities) == 0
			if advertised.ApplyPatchToolType != nil {
				model.SupportsApplyPatch = *advertised.ApplyPatchToolType == "freeform"
			}
			return model
		}
	}
	return oai.Model{
		UserName:           advertised.Slug,
		ModelName:          advertised.Slug,
		TextVerbosity:      "low",
		IsReasoningModel:   len(advertised.SupportedReasoningLevels) > 0,
		SupportsApplyPatch: advertised.ApplyPatchToolType != nil && *advertised.ApplyPatchToolType == "freeform",
		SupportsImages:     slices.Contains(advertised.InputModalities, "image") || len(advertised.InputModalities) == 0,
	}
}

func chatGPTReasoningLevels(advertised []ChatGPTReasoningLevel) []llm.ThinkingLevel {
	levels := make([]llm.ThinkingLevel, 0, len(advertised))
	for _, option := range advertised {
		effort := option.Effort
		if effort == "none" {
			effort = "off"
		}
		level := llm.ParseThinkingLevel(effort)
		if level != llm.ThinkingLevelDefault && !slices.Contains(levels, level) {
			levels = append(levels, level)
		}
	}
	return levels
}

func chatGPTServiceTiers(model ChatGPTModel) []string {
	tiers := make([]string, 0, len(model.ServiceTiers)+len(model.AdditionalSpeedTiers))
	for _, tier := range model.ServiceTiers {
		if tier.ID != "" && !slices.Contains(tiers, tier.ID) {
			tiers = append(tiers, tier.ID)
		}
	}
	for _, tier := range model.AdditionalSpeedTiers {
		if tier != "" && !slices.Contains(tiers, tier) {
			tiers = append(tiers, tier)
		}
	}
	return tiers
}

func normalizedClientVersion(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" || version == "dev" {
		return "0.0.0"
	}
	return version
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
