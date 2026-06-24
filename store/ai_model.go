package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"nofx/crypto"
	"nofx/logger"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AIModelStore AI model storage
type AIModelStore struct {
	db *gorm.DB
}

// FallbackEndpoint represents a backup API endpoint configuration
type FallbackEndpoint struct {
	Name     string `json:"name"`      // Display name for this endpoint
	BaseURL  string `json:"base_url"`  // API base URL
	APIKey   string `json:"api_key"`   // API key for this endpoint
	Model    string `json:"model"`     // Model name to use with this endpoint
	Priority int    `json:"priority"`  // Priority (lower = higher priority, 0 = highest)
}

// AIModel AI model configuration
type AIModel struct {
	ID                 string          `gorm:"primaryKey" json:"id"`
	UserID             string          `gorm:"column:user_id;not null;default:default;index" json:"user_id"`
	Name               string          `gorm:"not null" json:"name"`
	Provider           string          `gorm:"not null" json:"provider"`
	Enabled            bool            `gorm:"default:false" json:"enabled"`
	APIKey             crypto.EncryptedString `gorm:"column:api_key;default:''" json:"apiKey"`
	CustomAPIURL       string          `gorm:"column:custom_api_url;default:''" json:"customApiUrl"`
	CustomModelName    string          `gorm:"column:custom_model_name;default:''" json:"customModelName"`
	FallbackEndpoints  string          `gorm:"column:fallback_endpoints;type:text;default:''" json:"fallbackEndpoints"` // JSON array of FallbackEndpoint
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

func (AIModel) TableName() string { return "ai_models" }

// NewAIModelStore creates a new AIModelStore
func NewAIModelStore(db *gorm.DB) *AIModelStore {
	return &AIModelStore{db: db}
}

func (s *AIModelStore) initTables() error {
	// For PostgreSQL with existing table, skip AutoMigrate
	if s.db.Dialector.Name() == "postgres" {
		var tableExists int64
		s.db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'ai_models'`).Scan(&tableExists)
		if tableExists > 0 {
			return nil
		}
	}
	return s.db.AutoMigrate(&AIModel{})
}

func (s *AIModelStore) initDefaultData() error {
	// No longer pre-populate AI models - create on demand when user configures
	return nil
}

// List retrieves user's AI model list
func (s *AIModelStore) List(userID string) ([]*AIModel, error) {
	var models []*AIModel
	err := s.db.Where("user_id = ?", userID).Order("id").Find(&models).Error
	if err != nil {
		return nil, err
	}
	return models, nil
}

// Get retrieves a single AI model
func (s *AIModelStore) Get(userID, modelID string) (*AIModel, error) {
	if modelID == "" {
		return nil, fmt.Errorf("model ID cannot be empty")
	}

	candidates := []string{}
	if userID != "" {
		candidates = append(candidates, userID)
	}
	if userID != "default" {
		candidates = append(candidates, "default")
	}
	if len(candidates) == 0 {
		candidates = append(candidates, "default")
	}

	for _, uid := range candidates {
		var model AIModel
		err := s.db.Where("user_id = ? AND id = ?", uid, modelID).First(&model).Error
		if err == nil {
			return &model, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, gorm.ErrRecordNotFound
}

// GetByID retrieves an AI model by ID only
func (s *AIModelStore) GetByID(modelID string) (*AIModel, error) {
	if modelID == "" {
		return nil, fmt.Errorf("model ID cannot be empty")
	}

	var model AIModel
	err := s.db.Where("id = ?", modelID).First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// GetDefault retrieves the default enabled AI model
func (s *AIModelStore) GetDefault(userID string) (*AIModel, error) {
	if userID == "" {
		userID = "default"
	}
	model, err := s.firstEnabled(userID)
	if err == nil {
		return model, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if userID != "default" {
		return s.firstEnabled("default")
	}
	return nil, fmt.Errorf("please configure an available AI model in the system first")
}

func (s *AIModelStore) firstEnabled(userID string) (*AIModel, error) {
	var model AIModel
	err := s.db.Where("user_id = ? AND enabled = ?", userID, true).
		Order("updated_at DESC, id ASC").
		First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// GetAnyEnabled returns the first enabled AI model across all users.
// Used by single-user features (e.g. Telegram bot) that need any working LLM client.
func (s *AIModelStore) GetAnyEnabled() (*AIModel, error) {
	var model AIModel
	err := s.db.Where("enabled = ? AND api_key != ''", true).
		Order("updated_at DESC, id ASC").
		First(&model).Error
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// GetFallbackEndpoints parses and returns the fallback endpoints for a model
func (m *AIModel) GetFallbackEndpoints() ([]FallbackEndpoint, error) {
	if m.FallbackEndpoints == "" {
		return nil, nil
	}
	var endpoints []FallbackEndpoint
	if err := json.Unmarshal([]byte(m.FallbackEndpoints), &endpoints); err != nil {
		return nil, fmt.Errorf("failed to parse fallback endpoints: %w", err)
	}
	return endpoints, nil
}

// Update updates AI model, creates if not exists
// IMPORTANT: If apiKey is empty string, the existing API key will be preserved (not overwritten)
func (s *AIModelStore) Update(userID, id string, enabled bool, apiKey, customAPIURL, customModelName string) error {
	// Try exact ID match first
	var existingModel AIModel
	err := s.db.Where("user_id = ? AND id = ?", userID, id).First(&existingModel).Error
	if err == nil {
		// Update existing model
		updates := map[string]interface{}{
			"enabled":           enabled,
			"custom_api_url":    customAPIURL,
			"custom_model_name": customModelName,
			"updated_at":        time.Now().UTC(),
		}
		// If apiKey is not empty, update it (encryption handled by crypto.EncryptedString)
		if apiKey != "" {
			updates["api_key"] = crypto.EncryptedString(apiKey)
		}
		return s.db.Model(&existingModel).Updates(updates).Error
	}

	// Try legacy logic compatibility: use id as provider to search
	provider := id
	err = s.db.Where("user_id = ? AND provider = ?", userID, provider).First(&existingModel).Error
	if err == nil {
		logger.Warnf("⚠️ Using legacy provider matching to update model: %s -> %s", provider, existingModel.ID)
		updates := map[string]interface{}{
			"enabled":           enabled,
			"custom_api_url":    customAPIURL,
			"custom_model_name": customModelName,
			"updated_at":        time.Now().UTC(),
		}
		if apiKey != "" {
			updates["api_key"] = crypto.EncryptedString(apiKey)
		}
		return s.db.Model(&existingModel).Updates(updates).Error
	}

	// Create new record
	if provider == id && (provider == "deepseek" || provider == "qwen") {
		provider = id
	} else {
		parts := strings.Split(id, "_")
		if len(parts) >= 2 {
			provider = parts[len(parts)-1]
		} else {
			provider = id
		}
	}

	// Try to get name from existing model with same provider
	var refModel AIModel
	var name string
	if err := s.db.Where("provider = ?", provider).First(&refModel).Error; err == nil {
		name = refModel.Name
	} else {
		if provider == "deepseek" {
			name = "DeepSeek AI"
		} else if provider == "qwen" {
			name = "Qwen AI"
		} else {
			name = provider + " AI"
		}
	}

	newModelID := id
	if id == provider {
		newModelID = fmt.Sprintf("%s_%s", userID, provider)
	}

	logger.Infof("✓ Creating new AI model configuration: ID=%s, Provider=%s, Name=%s", newModelID, provider, name)
	newModel := &AIModel{
		ID:              newModelID,
		UserID:          userID,
		Name:            name,
		Provider:        provider,
		Enabled:         enabled,
		APIKey:          crypto.EncryptedString(apiKey),
		CustomAPIURL:    customAPIURL,
		CustomModelName: customModelName,
	}
	return s.db.Create(newModel).Error
}

// UpdateWithFallbacks updates AI model with fallback endpoints
// mergeFallbackAPIKeys fills in blank API keys on incoming fallback endpoints
// using the previously stored keys, matched by name + base_url. This keeps the
// edit-save round-trip from wiping keys that the GET endpoint intentionally masks.
func mergeFallbackAPIKeys(incoming []FallbackEndpoint, existingJSON string) []FallbackEndpoint {
	if existingJSON == "" {
		return incoming
	}
	var existing []FallbackEndpoint
	if err := json.Unmarshal([]byte(existingJSON), &existing); err != nil {
		return incoming
	}
	keyFor := func(fb FallbackEndpoint) string { return fb.Name + "\x00" + fb.BaseURL }
	prev := make(map[string]string, len(existing))
	for _, fb := range existing {
		prev[keyFor(fb)] = fb.APIKey
	}
	for i := range incoming {
		if incoming[i].APIKey == "" {
			if k, ok := prev[keyFor(incoming[i])]; ok {
				incoming[i].APIKey = k
			}
		}
	}
	return incoming
}

func (s *AIModelStore) UpdateWithFallbacks(userID, id string, enabled bool, apiKey, customAPIURL, customModelName string, fallbackEndpoints []FallbackEndpoint) error {
	// Try exact ID match first
	var existingModel AIModel
	err := s.db.Where("user_id = ? AND id = ?", userID, id).First(&existingModel).Error

	// Preserve existing fallback API keys when the incoming key is blank.
	// The GET endpoint masks keys (returns has_api_key only), so an edit-save
	// round-trip would otherwise wipe stored fallback keys.
	if err == nil {
		fallbackEndpoints = mergeFallbackAPIKeys(fallbackEndpoints, existingModel.FallbackEndpoints)
	}

	// Serialize fallback endpoints
	var fallbackJSON string
	if len(fallbackEndpoints) > 0 {
		data, err := json.Marshal(fallbackEndpoints)
		if err != nil {
			return fmt.Errorf("failed to serialize fallback endpoints: %w", err)
		}
		fallbackJSON = string(data)
	}

	if err == nil {
		// Update existing model
		updates := map[string]interface{}{
			"enabled":            enabled,
			"custom_api_url":     customAPIURL,
			"custom_model_name":  customModelName,
			"fallback_endpoints": fallbackJSON,
			"updated_at":         time.Now().UTC(),
		}
		// If apiKey is not empty, update it (encryption handled by crypto.EncryptedString)
		if apiKey != "" {
			updates["api_key"] = crypto.EncryptedString(apiKey)
		}
		return s.db.Model(&existingModel).Updates(updates).Error
	}

	// Try legacy logic compatibility: use id as provider to search
	provider := id
	err = s.db.Where("user_id = ? AND provider = ?", userID, provider).First(&existingModel).Error
	if err == nil {
		logger.Warnf("⚠️ Using legacy provider matching to update model: %s -> %s", provider, existingModel.ID)
		updates := map[string]interface{}{
			"enabled":            enabled,
			"custom_api_url":     customAPIURL,
			"custom_model_name":  customModelName,
			"fallback_endpoints": fallbackJSON,
			"updated_at":         time.Now().UTC(),
		}
		if apiKey != "" {
			updates["api_key"] = crypto.EncryptedString(apiKey)
		}
		return s.db.Model(&existingModel).Updates(updates).Error
	}

	// Create new record
	if provider == id && (provider == "deepseek" || provider == "qwen") {
		provider = id
	} else {
		parts := strings.Split(id, "_")
		if len(parts) >= 2 {
			provider = parts[len(parts)-1]
		} else {
			provider = id
		}
	}

	// Try to get name from existing model with same provider
	var refModel AIModel
	var name string
	if err := s.db.Where("provider = ?", provider).First(&refModel).Error; err == nil {
		name = refModel.Name
	} else {
		if provider == "deepseek" {
			name = "DeepSeek AI"
		} else if provider == "qwen" {
			name = "Qwen AI"
		} else {
			name = provider + " AI"
		}
	}

	newModelID := id
	if id == provider {
		newModelID = fmt.Sprintf("%s_%s", userID, provider)
	}

	logger.Infof("✓ Creating new AI model configuration: ID=%s, Provider=%s, Name=%s", newModelID, provider, name)
	newModel := &AIModel{
		ID:                newModelID,
		UserID:            userID,
		Name:              name,
		Provider:          provider,
		Enabled:           enabled,
		APIKey:            crypto.EncryptedString(apiKey),
		CustomAPIURL:      customAPIURL,
		CustomModelName:   customModelName,
		FallbackEndpoints: fallbackJSON,
	}
	return s.db.Create(newModel).Error
}

// Create creates an AI model
func (s *AIModelStore) Create(userID, id, name, provider string, enabled bool, apiKey, customAPIURL string) error {
	model := &AIModel{
		ID:           id,
		UserID:       userID,
		Name:         name,
		Provider:     provider,
		Enabled:      enabled,
		APIKey:       crypto.EncryptedString(apiKey),
		CustomAPIURL: customAPIURL,
	}
	// Use FirstOrCreate to ignore if already exists
	return s.db.Where("id = ?", id).FirstOrCreate(model).Error
}
