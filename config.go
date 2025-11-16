package main

import (
	"encoding/json"
	"log"
	"os"
)

// Config holds all configuration values
type Config struct {
	OpenAI struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	} `json:"openai"`
	Featherless struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	} `json:"featherless"`
	N8N struct {
		WebhookURL string `json:"webhook_url"`
	} `json:"n8n"`
	Server struct {
		Port string `json:"port"`
	} `json:"server"`
}

var appConfig *Config

// LoadConfig loads configuration from config.json, with environment variable overrides
func LoadConfig() *Config {
	config := &Config{}
	
	// Set defaults
	config.OpenAI.BaseURL = "https://api.openai.com/v1/chat/completions"
	config.Featherless.BaseURL = "https://api.featherless.ai/v1/chat/completions"
	config.Server.Port = "8755"
	
	// Try to load from config.json
	if data, err := os.ReadFile("config.json"); err == nil {
		if err := json.Unmarshal(data, config); err != nil {
			log.Printf("Warning: Could not parse config.json: %v", err)
		} else {
			log.Println("Loaded configuration from config.json")
		}
	} else {
		log.Println("config.json not found, using defaults and environment variables")
	}
	
	// Environment variables override config.json
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		config.OpenAI.APIKey = apiKey
	}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		config.OpenAI.BaseURL = baseURL
	}
	if featherlessKey := os.Getenv("FEATHERLESS_API_KEY"); featherlessKey != "" {
		config.Featherless.APIKey = featherlessKey
	}
	if featherlessURL := os.Getenv("FEATHERLESS_BASE_URL"); featherlessURL != "" {
		config.Featherless.BaseURL = featherlessURL
	}
	if n8nWebhookURL := os.Getenv("N8N_WEBHOOK_URL"); n8nWebhookURL != "" {
		config.N8N.WebhookURL = n8nWebhookURL
	}
	if port := os.Getenv("PORT"); port != "" {
		config.Server.Port = port
	}
	
	appConfig = config
	return config
}

// GetConfig returns the loaded configuration
func GetConfig() *Config {
	if appConfig == nil {
		return LoadConfig()
	}
	return appConfig
}

