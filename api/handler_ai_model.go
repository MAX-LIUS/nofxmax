package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nofx/config"
	"nofx/crypto"
	"nofx/logger"
	"nofx/security"
	"nofx/store"

	"github.com/gin-gonic/gin"
)

type ModelConfig struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	Enabled      bool   `json:"enabled"`
	APIKey       string `json:"apiKey,omitempty"`
	CustomAPIURL string `json:"customApiUrl,omitempty"`
}

// SafeModelConfig Safe model configuration structure (does not contain sensitive information)
type SafeModelConfig struct {
	ID                string                     `json:"id"`
	Name              string                     `json:"name"`
	Provider          string                     `json:"provider"`
	Enabled           bool                       `json:"enabled"`
	CustomAPIURL      string                     `json:"customApiUrl"`    // Custom API URL (usually not sensitive)
	CustomModelName   string                     `json:"customModelName"` // Custom model name (not sensitive)
	FallbackEndpoints []SafeFallbackEndpoint     `json:"fallbackEndpoints,omitempty"` // Fallback endpoints (without API keys)
}

// SafeFallbackEndpoint represents a fallback endpoint without sensitive information
type SafeFallbackEndpoint struct {
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	Priority int    `json:"priority"`
	HasAPIKey bool  `json:"has_api_key"` // Whether API key is configured (don't expose the key itself)
}

type UpdateModelConfigRequest struct {
	Models map[string]struct {
		Enabled           bool                       `json:"enabled"`
		APIKey            string                     `json:"api_key"`
		CustomAPIURL      string                     `json:"custom_api_url"`
		CustomModelName   string                     `json:"custom_model_name"`
		FallbackEndpoints []store.FallbackEndpoint   `json:"fallback_endpoints"`
	} `json:"models"`
}

// handleGetModelConfigs Get AI model configurations
func (s *Server) handleGetModelConfigs(c *gin.Context) {
	userID := c.GetString("user_id")
	logger.Infof("🔍 Querying AI model configs for user %s", userID)
	models, err := s.store.AIModel().List(userID)
	if err != nil {
		logger.Infof("❌ Failed to get AI model configs: %v", err)
		SafeInternalError(c, "Failed to get AI model configs", err)
		return
	}

	// If no models in database, return default models
	if len(models) == 0 {
		logger.Infof("⚠️ No AI models in database, returning defaults")
		defaultModels := []SafeModelConfig{
			{ID: "deepseek", Name: "DeepSeek AI", Provider: "deepseek", Enabled: false},
			{ID: "qwen", Name: "Qwen AI", Provider: "qwen", Enabled: false},
			{ID: "openai", Name: "OpenAI", Provider: "openai", Enabled: false},
			{ID: "claude", Name: "Claude AI", Provider: "claude", Enabled: false},
			{ID: "gemini", Name: "Gemini AI", Provider: "gemini", Enabled: false},
			{ID: "grok", Name: "Grok AI", Provider: "grok", Enabled: false},
			{ID: "kimi", Name: "Kimi AI", Provider: "kimi", Enabled: false},
			{ID: "minimax", Name: "MiniMax AI", Provider: "minimax", Enabled: false},
		}
		c.JSON(http.StatusOK, defaultModels)
		return
	}

	logger.Infof("✅ Found %d AI model configs", len(models))

	// Convert to safe response structure, remove sensitive information
	safeModels := make([]SafeModelConfig, len(models))
	for i, model := range models {
		// Parse fallback endpoints
		fallbacks, _ := model.GetFallbackEndpoints()
		safeFallbacks := make([]SafeFallbackEndpoint, len(fallbacks))
		for j, fb := range fallbacks {
			safeFallbacks[j] = SafeFallbackEndpoint{
				Name:      fb.Name,
				BaseURL:   fb.BaseURL,
				Model:     fb.Model,
				Priority:  fb.Priority,
				HasAPIKey: fb.APIKey != "",
			}
		}

		safeModels[i] = SafeModelConfig{
			ID:                model.ID,
			Name:              model.Name,
			Provider:          model.Provider,
			Enabled:           model.Enabled,
			CustomAPIURL:      model.CustomAPIURL,
			CustomModelName:   model.CustomModelName,
			FallbackEndpoints: safeFallbacks,
		}
	}

	c.JSON(http.StatusOK, safeModels)
}

// handleUpdateModelConfigs Update AI model configurations (supports both encrypted and plain text based on config)
func (s *Server) handleUpdateModelConfigs(c *gin.Context) {
	userID := c.GetString("user_id")
	cfg := config.Get()

	// Read raw request body
	bodyBytes, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return
	}

	var req UpdateModelConfigRequest

	// Check if transport encryption is enabled
	if !cfg.TransportEncryption {
		// Transport encryption disabled, accept plain JSON
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			logger.Infof("❌ Failed to parse plain JSON request: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
			return
		}
		logger.Infof("📝 Received plain text model config (UserID: %s)", userID)
	} else {
		// Transport encryption enabled — try encrypted first, fall back to plaintext
		var encryptedPayload crypto.EncryptedPayload
		if err := json.Unmarshal(bodyBytes, &encryptedPayload); err == nil && encryptedPayload.WrappedKey != "" {
			// Decrypt data
			decrypted, err := s.cryptoHandler.cryptoService.DecryptSensitiveData(&encryptedPayload)
			if err != nil {
				logger.Infof("❌ Failed to decrypt model config (UserID: %s): %v", userID, err)
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to decrypt data"})
				return
			}
			if err := json.Unmarshal([]byte(decrypted), &req); err != nil {
				logger.Infof("❌ Failed to parse decrypted data: %v", err)
				c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse decrypted data"})
				return
			}
			logger.Infof("🔓 Decrypted model config data (UserID: %s)", userID)
		} else {
			// Fallback: accept plaintext (client may not support crypto.subtle)
			if err := json.Unmarshal(bodyBytes, &req); err != nil {
				logger.Infof("❌ Failed to parse model config request: %v", err)
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
				return
			}
			logger.Infof("📝 Received plain text model config (crypto.subtle unavailable on client) (UserID: %s)", userID)
		}
	}

	// Update each model's configuration and track traders that need reload
	tradersToReload := make(map[string]bool)
	for modelID, modelData := range req.Models {
		// SSRF protection: validate custom_api_url before storing
		if modelData.CustomAPIURL != "" {
			cleanURL := strings.TrimSuffix(modelData.CustomAPIURL, "#")
			if err := security.ValidateURL(cleanURL); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid custom_api_url for model %s: %s", modelID, err.Error())})
				return
			}
		}

		// Find traders using this AI model BEFORE updating
		traders, _ := s.store.Trader().ListByAIModelID(userID, modelID)
		for _, t := range traders {
			tradersToReload[t.ID] = true
		}

		err := s.store.AIModel().UpdateWithFallbacks(userID, modelID, modelData.Enabled, modelData.APIKey, modelData.CustomAPIURL, modelData.CustomModelName, modelData.FallbackEndpoints)
		if err != nil {
			SafeInternalError(c, fmt.Sprintf("Update model %s", modelID), err)
			return
		}
	}

	// Remove affected traders from memory BEFORE reloading to pick up new config
	for traderID := range tradersToReload {
		logger.Infof("🔄 Removing trader %s from memory to reload with new AI model config", traderID)
		s.traderManager.RemoveTrader(traderID)
	}

	// Reload all traders for this user to make new config take effect immediately
	err = s.traderManager.LoadUserTradersFromStore(s.store, userID)
	if err != nil {
		logger.Infof("⚠️ Failed to reload user traders into memory: %v", err)
		// Don't return error here since model config was successfully updated to database
	}

	// NEVER log req.Models with %+v: it carries APIKey plus every
	// FallbackEndpoints[].APIKey in cleartext (fallback keys are stored
	// unencrypted, so the log would be the plaintext copy), and the log files are
	// 0644 root-readable and also shipped to /tmp/nofx.log. Log only which models
	// changed, whether a key was supplied, and the endpoint count.
	var summary strings.Builder
	for modelID, m := range req.Models {
		if summary.Len() > 0 {
			summary.WriteString(", ")
		}
		fallbackKeys := 0
		for _, fe := range m.FallbackEndpoints {
			if fe.APIKey != "" {
				fallbackKeys++
			}
		}
		summary.WriteString(fmt.Sprintf("%s{enabled=%v api_key=%v custom_url=%v fallbacks=%d(with_key=%d)}",
			modelID, m.Enabled, m.APIKey != "", m.CustomAPIURL != "",
			len(m.FallbackEndpoints), fallbackKeys))
	}
	logger.Infof("✓ AI model config updated: %s", summary.String())
	c.JSON(http.StatusOK, gin.H{"message": "Model configuration updated"})
}

// handleGetSupportedModels Get list of AI models supported by the system
func (s *Server) handleGetSupportedModels(c *gin.Context) {
	// Return static list of supported AI models with default versions
	supportedModels := []map[string]interface{}{
		{"id": "deepseek", "name": "DeepSeek", "provider": "deepseek", "defaultModel": "deepseek-chat"},
		{"id": "qwen", "name": "Qwen", "provider": "qwen", "defaultModel": "qwen3-max"},
		{"id": "openai", "name": "OpenAI", "provider": "openai", "defaultModel": "gpt-5.1"},
		{"id": "claude", "name": "Claude", "provider": "claude", "defaultModel": "claude-opus-4-6"},
		{"id": "gemini", "name": "Google Gemini", "provider": "gemini", "defaultModel": "gemini-3-pro-preview"},
		{"id": "grok", "name": "Grok (xAI)", "provider": "grok", "defaultModel": "grok-3-latest"},
		{"id": "kimi", "name": "Kimi (Moonshot)", "provider": "kimi", "defaultModel": "moonshot-v1-auto"},
		{"id": "minimax", "name": "MiniMax", "provider": "minimax", "defaultModel": "MiniMax-M2.5"},
		{"id": "blockrun-base", "name": "BlockRun (Base Wallet)", "provider": "blockrun-base", "defaultModel": "auto"},
		{"id": "blockrun-sol", "name": "BlockRun (Solana Wallet)", "provider": "blockrun-sol", "defaultModel": "auto"},
		{"id": "claw402", "name": "Claw402 (Base USDC)", "provider": "claw402", "defaultModel": "deepseek"},
	}

	c.JSON(http.StatusOK, supportedModels)
}
