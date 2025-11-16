package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	AgentTrickery = "trickery"
	AgentOffers   = "offers"
	AgentGuide    = "guide"
)

// AIClient handles OpenAI API calls with Featherless fallback
type AIClient struct {
	APIKey          string
	BaseURL         string
	FeatherlessKey  string
	FeatherlessURL  string
	logFile         *os.File
	logMu           sync.Mutex
}

// NewAIClient creates a new AI client
func NewAIClient() *AIClient {
	config := GetConfig()
	
	// Open log file for appending
	logFile, err := os.OpenFile("chatgpt_logs.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: Could not open log file: %v", err)
		logFile = nil
	}
	
	return &AIClient{
		APIKey:         config.OpenAI.APIKey,
		BaseURL:        config.OpenAI.BaseURL,
		FeatherlessKey: config.Featherless.APIKey,
		FeatherlessURL: config.Featherless.BaseURL,
		logFile:        logFile,
	}
}

// logRequestResponse logs the request and response to file
func (c *AIClient) logRequestResponse(agentType string, request []Message, response string, err error) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	
	if c.logFile == nil {
		return
	}
	
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	
	// Log request
	requestJSON, _ := json.MarshalIndent(request, "", "  ")
	c.logFile.WriteString(fmt.Sprintf("\n=== %s - %s ===\n", timestamp, agentType))
	c.logFile.WriteString("REQUEST:\n")
	c.logFile.WriteString(string(requestJSON))
	c.logFile.WriteString("\n\n")
	
	// Log response or error
	if err != nil {
		c.logFile.WriteString(fmt.Sprintf("ERROR: %v\n", err))
	} else {
		c.logFile.WriteString("RESPONSE:\n")
		c.logFile.WriteString(response)
		c.logFile.WriteString("\n")
	}
	
	c.logFile.WriteString("=== END ===\n\n")
	c.logFile.Sync()
}

// OpenAIRequest represents the request to OpenAI API
type OpenAIRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	MaxTokens int      `json:"max_tokens,omitempty"`
}

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIResponse represents the response from OpenAI API
type OpenAIResponse struct {
	Choices []Choice `json:"choices"`
}

// Choice represents a choice in the response
type Choice struct {
	Message Message `json:"message"`
}

// CallOpenAI makes a request to OpenAI API
func (c *AIClient) CallOpenAI(messages []Message) (string, error) {
	return c.CallOpenAIWithAgent("unknown", messages)
}

// CallOpenAIWithAgent makes a request to OpenAI API and logs it, with Featherless fallback
func (c *AIClient) CallOpenAIWithAgent(agentType string, messages []Message) (string, error) {
	// Try OpenAI first
	response, err := c.callAPI(c.BaseURL, c.APIKey, "gpt-3.5-turbo", agentType, messages)
	if err != nil {
		// Check if it's an insufficient_quota error
		if c.isInsufficientQuotaError(err) {
			log.Printf("OpenAI quota exceeded, trying Featherless fallback...")
			// Try Featherless as fallback
			featherlessResponse, featherlessErr := c.callAPI(c.FeatherlessURL, c.FeatherlessKey, "meta-llama/Meta-Llama-3.1-8B-Instruct", agentType+"_featherless", messages)
			if featherlessErr == nil {
				log.Printf("Successfully used Featherless fallback")
				return featherlessResponse, nil
			}
			log.Printf("Featherless fallback also failed: %v", featherlessErr)
		}
		return "", err
	}
	return response, nil
}

// callAPI makes a generic API call to any OpenAI-compatible endpoint
func (c *AIClient) callAPI(baseURL, apiKey, model, agentType string, messages []Message) (string, error) {
	reqBody := OpenAIRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: 800, // Increased for more detailed responses
	}
	
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	
	req, err := http.NewRequest("POST", baseURL, bytes.NewBuffer(jsonData))
	if err != nil {
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("API error (%s): %s", baseURL, string(body))
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	
	var openAIResp OpenAIResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	
	if len(openAIResp.Choices) == 0 {
		err := fmt.Errorf("no response from API")
		c.logRequestResponse(agentType, messages, "", err)
		return "", err
	}
	
	response := openAIResp.Choices[0].Message.Content
	c.logRequestResponse(agentType, messages, response, nil)
	return response, nil
}

// isInsufficientQuotaError checks if the error is due to insufficient quota
func (c *AIClient) isInsufficientQuotaError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "insufficient_quota") || strings.Contains(errStr, "quota")
}

// GenerateTrickeryOffer generates a tricky offer using AI
func (c *AIClient) GenerateTrickeryOffer(gameState *GameState) (*Offer, error) {
	prompt := fmt.Sprintf(`You are a financial trickery agent. Create a deceptive offer that seems like a good deal but is actually a scam or bad investment.

Current game state:
- Player money: $%.2f
- Day: %d
- Current investments: %d stocks, %d crypto

Create a tricky offer that:
1. Seems attractive with a discount or special deal
2. Has hidden costs, risks, or is actually overpriced
3. Uses common financial trickery tactics (fake urgency, hidden fees, too-good-to-be-true returns, etc.)

Respond in JSON format:
{
  "title": "Offer title",
  "description": "Detailed description",
  "price": 1234.56,
  "original_price": 2000.00,
  "discount": 30,
  "reason": "Why this is a trickery (explain the scam)"
}`, gameState.Money, gameState.CurrentDate.Format("2006-01-02"), len(gameState.Stocks), len(gameState.Crypto))
	
	messages := []Message{
		{Role: "system", Content: "You are a financial trickery expert. Generate deceptive offers that test financial literacy."},
		{Role: "user", Content: prompt},
	}
	
	response, err := c.CallOpenAIWithAgent("trickery_offer", messages)
	if err != nil {
		// Fallback to simple offer if API fails
		return c.generateFallbackTrickeryOffer(gameState), nil
	}
	
	// Parse JSON response
	var offerData map[string]interface{}
	if err := json.Unmarshal([]byte(response), &offerData); err != nil {
		return c.generateFallbackTrickeryOffer(gameState), nil
	}
	
	offer := &Offer{
		ID:            generateID(),
		Type:          AgentTrickery,
		Title:         getString(offerData, "title", "Special Investment Opportunity"),
		Description:   getString(offerData, "description", "Limited time offer!"),
		Price:         getFloat(offerData, "price", 1000),
		OriginalPrice: getFloat(offerData, "original_price", 1500),
		Discount:      getFloat(offerData, "discount", 30),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		IsTrickery:    true,
		Reason:        getString(offerData, "reason", "Hidden fees and risks"),
	}
	
	return offer, nil
}

// GenerateGoodOffer generates a legitimate good offer using AI
func (c *AIClient) GenerateGoodOffer(gameState *GameState) (*Offer, error) {
	prompt := fmt.Sprintf(`You are a financial advisor agent. Create a legitimate, good-value offer that helps the player.

Current game state:
- Player money: $%.2f
- Day: %d
- Current investments: %d stocks, %d crypto

Create a good offer that:
1. Provides real value
2. Has a genuine discount
3. Helps the player's financial situation
4. Is transparent about terms

Respond in JSON format:
{
  "title": "Offer title",
  "description": "Detailed description",
  "price": 1234.56,
  "original_price": 2000.00,
  "discount": 30,
  "reason": "Why this is a good deal"
}`, gameState.Money, gameState.CurrentDate.Format("2006-01-02"), len(gameState.Stocks), len(gameState.Crypto))
	
	messages := []Message{
		{Role: "system", Content: "You are a helpful financial advisor. Generate legitimate, valuable offers."},
		{Role: "user", Content: prompt},
	}
	
	response, err := c.CallOpenAIWithAgent("good_offer", messages)
	if err != nil {
		return c.generateFallbackGoodOffer(gameState), nil
	}
	
	var offerData map[string]interface{}
	if err := json.Unmarshal([]byte(response), &offerData); err != nil {
		return c.generateFallbackGoodOffer(gameState), nil
	}
	
	offer := &Offer{
		ID:            generateID(),
		Type:          AgentOffers,
		Title:         getString(offerData, "title", "Great Investment Deal"),
		Description:   getString(offerData, "description", "A solid investment opportunity"),
		Price:         getFloat(offerData, "price", 800),
		OriginalPrice: getFloat(offerData, "original_price", 1200),
		Discount:      getFloat(offerData, "discount", 25),
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		IsTrickery:    false,
		Reason:        getString(offerData, "reason", "Genuine value and discount"),
	}
	
	return offer, nil
}

// GenerateStockOffer generates a stock offer using AI (safe or unsafe)
func (c *AIClient) GenerateStockOffer(gameState *GameState) (*StockOffer, error) {
	// Randomly decide if it's safe or unsafe (50/50)
	isSafe := rand.Float64() < 0.5
	
	prompt := fmt.Sprintf(`You are a %s stock analyst. Create a stock investment opportunity that %s.

Current game state:
- Player money: €%.2f
- Current date: %s
- Current investments: %d stocks, %d crypto

Create a stock offer that:
1. Has a company name and stock symbol (3-5 letter ticker)
2. Has a description of the company and why it's an investment opportunity
3. Current stock price (€10-€500 per share)
4. Is either SAFE (reliable, established company) or UNSAFE (risky, speculative, potential scam)
5. Failure chance (0-100%): For safe stocks, 5-20% failure chance. For unsafe stocks, 30-80% failure chance.
6. Reliability rating: "high" (safe), "medium" (moderate risk), or "low" (high risk/unsafe)
7. Reason: Explain why this stock is safe/unsafe, what makes it reliable or risky

IMPORTANT:
- Safe stocks: Established companies, good fundamentals, low volatility, reliable dividends
- Unsafe stocks: New companies, high volatility, speculative, potential pump-and-dump, questionable financials, overhyped

Respond in JSON format:
{
  "symbol": "TECH",
  "company_name": "TechCorp Inc.",
  "description": "Description of the company and investment opportunity",
  "current_price": 150.00,
  "is_safe": true,
  "failure_chance": 15.0,
  "reliability": "high",
  "reason": "Why this stock is safe/unsafe"
}`, 
		map[bool]string{true: "conservative", false: "aggressive"}[isSafe],
		map[bool]string{true: "is safe and reliable", false: "is risky or potentially unsafe"}[isSafe],
		gameState.Money,
		gameState.CurrentDate.Format("2006-01-02"),
		len(gameState.Stocks),
		len(gameState.Crypto))
	
	systemMsg := map[bool]string{
		true:  "You are a conservative stock analyst. Generate safe, reliable stock investment opportunities.",
		false: "You are a stock analyst. Generate risky or potentially unsafe stock opportunities (could be scams, overhyped, speculative).",
	}[isSafe]
	
	messages := []Message{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: prompt},
	}
	
	response, err := c.CallOpenAIWithAgent("stock_offer", messages)
	if err != nil {
		return c.generateFallbackStockOffer(gameState, isSafe), nil
	}
	
	var offerData map[string]interface{}
	if err := json.Unmarshal([]byte(response), &offerData); err != nil {
		return c.generateFallbackStockOffer(gameState, isSafe), nil
	}
	
	price := getFloat(offerData, "current_price", 100)
	if price < 10 {
		price = 10
	}
	if price > 500 {
		price = 500
	}
	
	failureChance := getFloat(offerData, "failure_chance", 20)
	if failureChance < 0 {
		failureChance = 0
	}
	if failureChance > 100 {
		failureChance = 100
	}
	
	// Adjust failure chance based on is_safe
	if isSafe && failureChance > 20 {
		failureChance = 15 + rand.Float64()*5 // 15-20% for safe stocks
	}
	if !isSafe && failureChance < 30 {
		failureChance = 30 + rand.Float64()*50 // 30-80% for unsafe stocks
	}
	
	reliability := getString(offerData, "reliability", "medium")
	if isSafe && reliability != "high" {
		reliability = "high"
	}
	if !isSafe && reliability == "high" {
		reliability = "low"
	}
	
	offer := &StockOffer{
		ID:            generateID(),
		Symbol:        getString(offerData, "symbol", "STOCK"),
		CompanyName:   getString(offerData, "company_name", "Company Inc."),
		Description:   getString(offerData, "description", "A stock investment opportunity"),
		CurrentPrice:  price,
		IsSafe:        isSafe,
		FailureChance: failureChance,
		Reliability:   reliability,
		Reason:        getString(offerData, "reason", ""),
		ExpiresAt:     gameState.CurrentDate.Add(7 * 24 * time.Hour), // Expires in 7 days
	}
	
	return offer, nil
}

func (c *AIClient) generateFallbackStockOffer(gameState *GameState, isSafe bool) *StockOffer {
	if isSafe {
		return &StockOffer{
			ID:            generateID(),
			Symbol:        "SAFE",
			CompanyName:   "SafeCorp Industries",
			Description:   "Established company with strong fundamentals and reliable dividends.",
			CurrentPrice:  150.0,
			IsSafe:        true,
			FailureChance: 15.0,
			Reliability:   "high",
			Reason:        "Established company with good financials and stable growth",
			ExpiresAt:     gameState.CurrentDate.Add(7 * 24 * time.Hour),
		}
	}
	return &StockOffer{
		ID:            generateID(),
		Symbol:        "RISK",
		CompanyName:   "Risky Ventures Inc.",
		Description:   "New startup with high growth potential but significant risk.",
		CurrentPrice:  50.0,
		IsSafe:        false,
		FailureChance: 60.0,
		Reliability:   "low",
		Reason:        "New company, high volatility, speculative investment",
		ExpiresAt:     gameState.CurrentDate.Add(7 * 24 * time.Hour),
	}
}

// GenerateOtherOffer generates a random "other" offer (scams, charity, etc.) using AI
func (c *AIClient) GenerateOtherOffer(gameState *GameState) (*Offer, error) {
	// Provide examples to the AI, but let it be creative
	examples := []string{
		"African Prince / Nigerian prince scam - promises large money for small processing fee",
		"Charity donation with official partners - legitimate but costs money, improves reputation",
		"Buy a ring from a gypsy/traveler - could be magical/lucky or a scam, may affect health/energy",
		"Sell passport - illegal, gives money but loses reputation",
		"Buy cheap car from friend - could be good deal or have hidden problems",
	}
	
	// Randomly select an example category to guide the AI
	exampleCategory := examples[rand.Intn(len(examples))]
	
	// Determine if it should be trickery (70% chance for scams, 30% for legitimate)
	isTrickery := rand.Float64() < 0.5
	
	systemMsg := "You are a creative offer generator. Create interesting, realistic offers that test financial literacy and decision-making."
	
	prompt := fmt.Sprintf(`Create a random "other" type offer for an economic game. This offer should be creative and test the player's financial awareness.

INSPIRATION / EXAMPLE CATEGORY: %s

The offer should:
1. Be realistic and believable
2. Test financial literacy (scams, legitimate opportunities, ethical dilemmas)
3. Have consequences that affect player stats (health, energy, reputation, money)
4. Be either a scam/trickery OR legitimate opportunity
5. Have a price (can be €0 if it's about selling something)
6. Be either a one-time item purchase OR a recurring agreement/subscription:
   - One-time items: is_recurring=false (e.g., buying a ring, selling passport, one-time charity donation)
   - Recurring agreements: is_recurring=true (e.g., gym membership, subscription service, monthly charity, insurance)
   - If recurring: set recurrence_type ("daily", "weekly", or "monthly")
7. Have stat effects when accepted:
   - health_change: -100 to 100 (health gained/lost)
   - energy_change: -100 to 100 (energy gained/lost)
   - reputation_change: reputation gained/lost (can be negative)
   - money_change: additional money change beyond the price (can be positive or negative)
   - For recurring agreements, these effects apply periodically (daily/weekly/monthly)

Current game state:
- Player money: €%.2f
- Health: %d/100
- Energy: %d/100
- Reputation: %d
- Current date: %s

Be creative! Don't just copy the example - create variations or entirely new ideas inspired by it. Think of:
- Scams (advance fee fraud, fake investments, identity theft, etc.)
- Legitimate opportunities (charity, investments, services, etc.)
- Ethical dilemmas (selling personal items, illegal activities, etc.)
- Unusual offers (magical items, suspicious deals, etc.)

Respond in JSON format:
{
  "title": "Creative offer title",
  "description": "Detailed, convincing description",
  "price": 100.00,
  "money_change": -100.00,
  "health_change": 0,
  "energy_change": 0,
  "reputation_change": 0,
  "is_recurring": false,
  "recurrence_type": "monthly",
  "reason": "Why this is %s (explain the consequences)",
  "is_trickery": true
}`, 
		exampleCategory,
		gameState.Money,
		gameState.Health,
		gameState.Energy,
		gameState.Reputation,
		gameState.CurrentDate.Format("2006-01-02"),
		map[bool]string{true: "a scam/trickery", false: "legitimate"}[isTrickery])
	
	messages := []Message{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: prompt},
	}
	
	response, err := c.CallOpenAIWithAgent("other_offer", messages)
	if err != nil {
		// Use fallback with the example category
		return c.generateFallbackOtherOffer(gameState, exampleCategory, isTrickery), nil
	}
	
	var offerData map[string]interface{}
	if err := json.Unmarshal([]byte(response), &offerData); err != nil {
		return c.generateFallbackOtherOffer(gameState, exampleCategory, isTrickery), nil
	}
	
	// Get is_trickery from AI response, or use default
	aiIsTrickery := isTrickery
	if trickeryVal, ok := offerData["is_trickery"].(bool); ok {
		aiIsTrickery = trickeryVal
	}
	
	price := getFloat(offerData, "price", 100)
	if price < 0 {
		price = 0
	}
	if price > 10000 {
		price = 10000
	}
	
	healthChange := int(getFloat(offerData, "health_change", 0))
	if healthChange < -100 {
		healthChange = -100
	}
	if healthChange > 100 {
		healthChange = 100
	}
	
	energyChange := int(getFloat(offerData, "energy_change", 0))
	if energyChange < -100 {
		energyChange = -100
	}
	if energyChange > 100 {
		energyChange = 100
	}
	
	reputationChange := int(getFloat(offerData, "reputation_change", 0))
	
	moneyChange := getFloat(offerData, "money_change", 0)
	
	// Determine if this is recurring
	isRecurring := false
	if val, ok := offerData["is_recurring"].(bool); ok {
		isRecurring = val
	}
	
	recurrenceType := getString(offerData, "recurrence_type", "monthly")
	if !isRecurring {
		recurrenceType = ""
	}
	
	offer := &Offer{
		ID:               generateID(),
		Type:             "other",
		Title:            getString(offerData, "title", "Special Offer"),
		Description:      getString(offerData, "description", "An interesting offer"),
		Price:            price,
		ExpiresAt:        gameState.CurrentDate.Add(3 * 24 * time.Hour), // Expires in 3 days
		IsTrickery:       aiIsTrickery,
		Reason:           getString(offerData, "reason", ""),
		HealthChange:     healthChange,
		EnergyChange:     energyChange,
		ReputationChange: reputationChange,
		MoneyChange:      moneyChange,
		IsRecurring:      isRecurring,
		RecurrenceType:   recurrenceType,
	}
	
	return offer, nil
}

func (c *AIClient) generateFallbackOtherOffer(gameState *GameState, exampleCategory string, isTrickery bool) *Offer {
	// Parse example category to determine type
	var title, description, reason string
	var price float64
	var healthChange, energyChange, reputationChange int
	var moneyChange float64
	
	// Use examples as fallbacks, but create variations
	if strings.Contains(strings.ToLower(exampleCategory), "african") || strings.Contains(strings.ToLower(exampleCategory), "nigerian") || strings.Contains(strings.ToLower(exampleCategory), "prince") {
		title = "Urgent: Help Transfer Funds from African Prince"
		description = "I am Prince Abubakar from Nigeria. I need your help to transfer €500,000. Send €2,000 processing fee to receive your share!"
		price = 2000
		reason = "Classic advance fee fraud scam - you'll never see the money"
		moneyChange = -2000
	} else if strings.Contains(strings.ToLower(exampleCategory), "charity") || strings.Contains(strings.ToLower(exampleCategory), "donation") {
		title = "Charity Donation - Official Partners"
		description = "Donate to help children in need. Official partners: UNICEF, Red Cross. Your donation makes a difference!"
		price = 100
		reason = "Legitimate charity donation that helps others"
		moneyChange = -100
		reputationChange = 5
	} else if strings.Contains(strings.ToLower(exampleCategory), "ring") || strings.Contains(strings.ToLower(exampleCategory), "gypsy") || strings.Contains(strings.ToLower(exampleCategory), "traveler") {
		title = "Buy Magical Ring from Traveler"
		description = "A mysterious traveler offers you a 'lucky' ring. They say it brings good fortune. Only €50!"
		price = 50
		if isTrickery {
			reason = "Scam - the ring is worthless"
			moneyChange = -50
		} else {
			reason = "Legitimate - could bring luck"
			moneyChange = -50
			healthChange = 5
		}
	} else if strings.Contains(strings.ToLower(exampleCategory), "passport") || strings.Contains(strings.ToLower(exampleCategory), "sell") {
		title = "Sell Your Passport"
		description = "A shady character offers €3,000 for your passport. Quick cash, but is it worth it?"
		price = 0
		reason = "Illegal and dangerous - selling identity documents"
		moneyChange = 3000
		reputationChange = -10
	} else if strings.Contains(strings.ToLower(exampleCategory), "car") || strings.Contains(strings.ToLower(exampleCategory), "friend") {
		title = "Buy Car from Friend"
		description = "Your friend offers to sell you their old car for €1,500. It's a good deal, but the car might have issues."
		price = 1500
		if isTrickery {
			reason = "Car breaks down immediately - hidden problems"
			moneyChange = -1500
			healthChange = -5
		} else {
			reason = "Good deal - reliable car from friend"
			moneyChange = -1500
		}
	} else {
		// Generic fallback
		title = "Special Offer"
		description = "An interesting opportunity has come your way."
		price = 100
		reason = "Consider carefully before accepting"
		moneyChange = -100
	}
	
	return &Offer{
		ID:               generateID(),
		Type:             "other",
		Title:            title,
		Description:      description,
		Price:            price,
		ExpiresAt:        gameState.CurrentDate.Add(3 * 24 * time.Hour),
		IsTrickery:       isTrickery,
		Reason:           reason,
		HealthChange:     healthChange,
		EnergyChange:     energyChange,
		ReputationChange: reputationChange,
		MoneyChange:      moneyChange,
		IsRecurring:      false, // Fallback offers are one-time by default
		RecurrenceType:   "",
	}
}

// ChatWithGuide asks the guide agent for advice
func (c *AIClient) ChatWithGuide(gameState *GameState, userMessage string, context string) (*ChatResponse, error) {
	// Build comprehensive work context
	workContext := c.buildWorkContext(gameState)
	
	// Build apartment and health context
	apartmentContext := c.buildApartmentContext(gameState)
	
	// Get recent work-related events
	recentEvents := c.getRecentWorkEvents(gameState)
	
	prompt := fmt.Sprintf(`The player is asking about their work choices. Use the detailed context below to provide personalized guidance.

CURRENT GAME STATE:
- Player money: €%.2f
- Current date: %s
- Health: %d/100
- Energy: %d/100
- Reputation: %d
- Current job: %s
- Current apartment: %s
- Investments: %d stocks, %d crypto, %d items

DETAILED WORK CONTEXT:
%s

APARTMENT & HEALTH STATUS:
%s

RECENT WORK-RELATED EVENTS (in chronological order, most recent first):
%s

ADDITIONAL CONTEXT FROM PLAYER:
%s

PLAYER'S QUESTION:
"%s"

YOUR TASK:
1. Analyze their current work situation using the WORK CONTEXT above
2. Consider their APARTMENT & HEALTH STATUS - if they have no apartment, URGENTLY inform them they will lose 2 health each night!
3. Consider their recent work-related events and decisions
4. Provide personalized guidance that references specific details (job title, salary, work type, schedule, apartment, health, energy, etc.)
5. Ask 2-3 thoughtful guiding questions that help them think critically about their work choice
6. If they're asking "was it a good work choice?", evaluate based on:
   - Their current job details (salary, hours, work type)
   - Their apartment situation (critical for health!)
   - Their health and energy levels
   - Their recent decisions (job changes, work patterns)
   - Their financial situation (money, reputation)
   - Available alternatives (job offers, apartment offers)

CRITICAL: Do NOT give generic responses. Always reference specific details from the context above. For example:
- "Looking at your current job as [Job Title] with €[Salary]/month..."
- "I see you recently [recent event]..."
- "Given that you have a [work type] job with [schedule]..."

Respond in JSON format:
{
  "message": "Your personalized guidance message that references specific context details",
  "questions": ["Question 1 that relates to their specific situation", "Question 2", "Question 3"]
}`, 
		gameState.Money, 
		gameState.CurrentDate.Format("2006-01-02"),
		gameState.Health,
		gameState.Energy,
		gameState.Reputation,
		getJobTitle(gameState),
		func() string {
			if gameState.Apartment != nil {
				return gameState.Apartment.Title
			}
			return "None"
		}(),
		len(gameState.Stocks),
		len(gameState.Crypto),
		len(gameState.Inventory),
		workContext,
		apartmentContext,
		recentEvents,
		context,
		userMessage)
	
	systemPrompt := `You are a patient financial educator and career advisor. Your role is to guide players through their work and financial decisions by asking thoughtful, guiding questions rather than giving direct answers. 

CRITICAL RULES - YOU MUST FOLLOW THESE:
1. ALWAYS reference specific details from the WORK CONTEXT provided (job title, salary, work type, schedule, etc.)
2. ALWAYS mention recent work-related events when relevant
3. NEVER give generic responses like "What are the risks?" without context
4. ALWAYS personalize your response - use their actual job title, salary amounts, work type
5. Your "message" should be a brief personalized guidance (2-3 sentences) that references their specific situation
6. Your "questions" should be 2-3 specific questions that relate to THEIR situation, not generic ones

EXAMPLE OF GOOD RESPONSE:
If player has job "Software Developer" with €5000/month, fixed schedule 09:00-17:00:
{
  "message": "Looking at your Software Developer position with €5000/month and a fixed 09:00-17:00 schedule, this seems like a stable opportunity. The €31.25/hour rate is competitive. However, the fixed schedule means less flexibility.",
  "questions": [
    "Does the fixed 09:00-17:00 schedule work well with your lifestyle and other commitments?",
    "Have you compared this €5000/month salary with other available job offers?",
    "How important is schedule flexibility versus the stability of a fixed-time job?"
  ]
}

EXAMPLE OF BAD RESPONSE (DO NOT DO THIS):
{
  "message": "I'm here to help! Think about: What are the risks? What are the alternatives?",
  "questions": ["What are the potential risks?", "Have you considered alternatives?", "How does this fit your plan?"]
}

You MUST respond in valid JSON format only, no markdown, no code blocks, just pure JSON.`

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}
	
	response, err := c.CallOpenAIWithAgent("guide_chat", messages)
	if err != nil {
		// Log the error but provide a context-aware fallback
		log.Printf("Error calling OpenAI for guide chat: %v", err)
		return c.generateContextAwareFallback(gameState, userMessage), nil
	}
	
	// Try to extract JSON from response (might have markdown code blocks)
	responseText := response
	if len(responseText) > 0 {
		// Remove markdown code blocks if present
		if idx := findJSONInResponse(responseText); idx >= 0 {
			responseText = responseText[idx:]
			if endIdx := findJSONEnd(responseText); endIdx > 0 {
				responseText = responseText[:endIdx+1]
			}
		}
	}
	
	var chatData map[string]interface{}
	if err := json.Unmarshal([]byte(responseText), &chatData); err != nil {
		// If JSON parsing fails, try to extract message and questions from plain text
		log.Printf("Error parsing JSON response: %v. Response was: %s", err, response)
		return c.parseTextResponse(response, gameState, userMessage), nil
	}
	
	questions := []string{}
	if qs, ok := chatData["questions"].([]interface{}); ok {
		for _, q := range qs {
			if qStr, ok := q.(string); ok {
				questions = append(questions, qStr)
			}
		}
	}
	
	// If no questions extracted, generate some based on context
	if len(questions) == 0 {
		questions = c.generateContextQuestions(gameState, userMessage)
	}
	
	message := getString(chatData, "message", "")
	if message == "" {
		message = c.generateContextAwareMessage(gameState, userMessage)
	}
	
	return &ChatResponse{
		Agent:     AgentGuide,
		Message:   message,
		Questions: questions,
	}, nil
}

// ParseChatForOfferCreation parses a chat message to detect if player wants to create an offer/agreement/sell item
func (c *AIClient) ParseChatForOfferCreation(gameState *GameState, userMessage string) (*ChatResponse, error) {
	prompt := fmt.Sprintf(`Analyze the following player message to determine if they want to create an offer, agreement, or sell an item to other players.

CURRENT GAME STATE:
- Player money: €%.2f
- Current date: %s
- Health: %d/100
- Energy: %d/100
- Reputation: %d
- Inventory items: %d
- Current job: %s

PLAYER'S MESSAGE:
"%s"

YOUR TASK:
1. Determine if the player wants to:
   - Create an OFFER (one-time sale of something)
   - Create an AGREEMENT (recurring subscription/service)
   - SELL an ITEM (sell an item from their inventory)
   - Just asking a question (no creation intent)

2. If they want to create something, extract:
   - Type: "offer", "agreement", or "item"
   - Title: What they're offering
   - Description: Details about the offer
   - Price: How much they want (€)
   - For agreements: recurrence_type ("daily", "weekly", "monthly")
   - For items: which item from inventory (if mentioned)
   - Stat effects: health_change, energy_change, reputation_change, money_change (if mentioned)
   - Is it a scam/trickery? (if they mention it's deceptive)

3. If they're just asking a question, return null for all creation fields.

Respond in JSON format:
{
  "intent": "offer" | "agreement" | "item" | "question",
  "title": "Title of offer/agreement/item",
  "description": "Description",
  "price": 100.00,
  "recurrence_type": "daily" | "weekly" | "monthly" | null,
  "health_change": 0,
  "energy_change": 0,
  "reputation_change": 0,
  "money_change": 0.00,
  "is_trickery": false,
  "item_id": "item_id_from_inventory" | null,
  "message": "Confirmation message or clarification question"
}`, 
		gameState.Money,
		gameState.CurrentDate.Format("2006-01-02"),
		gameState.Health,
		gameState.Energy,
		gameState.Reputation,
		len(gameState.Inventory),
		getJobTitle(gameState),
		userMessage)

	systemMsg := `You are an assistant that helps players create offers, agreements, or sell items in an economic game. 
Parse their natural language and extract structured information. If the message is unclear, ask clarifying questions.
If they mention selling an item, try to match it to their inventory.`

	messages := []Message{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: prompt},
	}

	response, err := c.CallOpenAIWithAgent("chat_offer_parser", messages)
	if err != nil {
		log.Printf("[PARSE_OFFER] ERROR calling AI for player %s: %v", gameState.PlayerID, err)
		return &ChatResponse{
			Agent:   AgentGuide,
			Message: "I couldn't understand your request. Could you clarify what you'd like to create?",
		}, nil
	}

	log.Printf("[PARSE_OFFER] Raw AI response for player %s: %s", gameState.PlayerID, response)

	// Try to extract JSON
	responseText := response
	if idx := findJSONInResponse(responseText); idx >= 0 {
		responseText = responseText[idx:]
		if endIdx := findJSONEnd(responseText); endIdx > 0 {
			responseText = responseText[:endIdx+1]
		}
	}

	log.Printf("[PARSE_OFFER] Extracted JSON for player %s: %s", gameState.PlayerID, responseText)

	var parsedData map[string]interface{}
	if err := json.Unmarshal([]byte(responseText), &parsedData); err != nil {
		log.Printf("[PARSE_OFFER] ERROR parsing JSON for player %s: %v. Response text: %s", gameState.PlayerID, err, responseText)
		return &ChatResponse{
			Agent:   AgentGuide,
			Message: "I couldn't parse your request. Please try again with more details.",
		}, nil
	}

	intent := getString(parsedData, "intent", "question")
	log.Printf("[PARSE_OFFER] Parsed intent for player %s: %s", gameState.PlayerID, intent)
	
	if intent == "question" {
		log.Printf("[PARSE_OFFER] No creation intent for player %s, returning nil", gameState.PlayerID)
		return nil, nil // No creation intent, return nil to indicate normal chat flow
	}

	// Create the appropriate structure
	title := getString(parsedData, "title", "")
	description := getString(parsedData, "description", "")
	price := getFloat(parsedData, "price", 0)
	
	if title == "" || description == "" {
		return &ChatResponse{
			Agent:   AgentGuide,
			Message: getString(parsedData, "message", "I need more details. What exactly are you offering? Please include a title, description, and price."),
		}, nil
	}

	chatResponse := &ChatResponse{
		Agent:   AgentGuide,
		Message: getString(parsedData, "message", fmt.Sprintf("I'll create your %s: %s", intent, title)),
		Created: true,
	}
	
	log.Printf("[PARSE_OFFER] Created ChatResponse for player %s: intent=%s, title=%s, Created=true", 
		gameState.PlayerID, intent, title)

	switch intent {
	case "offer":
		log.Printf("[PARSE_OFFER] Processing offer creation for player %s", gameState.PlayerID)
		healthChange := 0
		if val, ok := parsedData["health_change"]; ok {
			if f, ok := val.(float64); ok {
				healthChange = int(f)
			}
		}
		energyChange := 0
		if val, ok := parsedData["energy_change"]; ok {
			if f, ok := val.(float64); ok {
				energyChange = int(f)
			}
		}
		reputationChange := 0
		if val, ok := parsedData["reputation_change"]; ok {
			if f, ok := val.(float64); ok {
				reputationChange = int(f)
			}
		}
		
		offer := &Offer{
			ID:              generateID(),
			Type:            "other",
			Title:           title,
			Description:     description,
			Price:           price,
			ExpiresAt:       gameState.CurrentDate.Add(7 * 24 * time.Hour), // Expires in 7 days
			IsTrickery:      getBool(parsedData, "is_trickery", false),
			HealthChange:    healthChange,
			EnergyChange:    energyChange,
			ReputationChange: reputationChange,
			MoneyChange:     getFloat(parsedData, "money_change", 0),
			IsRecurring:     false,
			CreatedBy:       gameState.PlayerID,
		}
		chatResponse.Offer = offer
		log.Printf("[PARSE_OFFER] Created offer for player %s: ID=%s, Title=%s, Price=%.2f", 
			gameState.PlayerID, offer.ID, offer.Title, offer.Price)

	case "agreement":
		log.Printf("[PARSE_OFFER] Processing agreement creation for player %s", gameState.PlayerID)
		recurrenceType := getString(parsedData, "recurrence_type", "monthly")
		if recurrenceType != "daily" && recurrenceType != "weekly" && recurrenceType != "monthly" {
			recurrenceType = "monthly"
		}
		
		healthChange := 0
		if val, ok := parsedData["health_change"]; ok {
			if f, ok := val.(float64); ok {
				healthChange = int(f)
			}
		}
		energyChange := 0
		if val, ok := parsedData["energy_change"]; ok {
			if f, ok := val.(float64); ok {
				energyChange = int(f)
			}
		}
		reputationChange := 0
		if val, ok := parsedData["reputation_change"]; ok {
			if f, ok := val.(float64); ok {
				reputationChange = int(f)
			}
		}
		
		// Ensure price is positive and MoneyChange is negative
		agreementPrice := price
		if agreementPrice <= 0 {
			agreementPrice = 50.0 // Default price
		}
		moneyChange := getFloat(parsedData, "money_change", -agreementPrice)
		if moneyChange >= 0 {
			moneyChange = -agreementPrice // Ensure negative for subscriptions
		}
		
		agreement := &Agreement{
			ID:              generateID(),
			Title:           title,
			Description:     description,
			RecurrenceType:  recurrenceType,
			StartedAt:       gameState.CurrentDate,
			LastProcessedAt: gameState.CurrentDate,
			HealthChange:    healthChange,
			EnergyChange:    energyChange,
			ReputationChange: reputationChange,
			MoneyChange:     moneyChange,
			IsTrickery:      getBool(parsedData, "is_trickery", false),
			Price:           agreementPrice, // Store the price for offer creation
		}
		chatResponse.Agreement = agreement
		log.Printf("[PARSE_OFFER] Created agreement for player %s: ID=%s, Title=%s, RecurrenceType=%s", 
			gameState.PlayerID, agreement.ID, agreement.Title, agreement.RecurrenceType)

	case "item":
		log.Printf("[PARSE_OFFER] Processing item sale for player %s", gameState.PlayerID)
		itemID := getString(parsedData, "item_id", "")
		// Find item in inventory
		var item *Item
		if itemID != "" {
			for i := range gameState.Inventory {
				if gameState.Inventory[i].ID == itemID {
					item = &gameState.Inventory[i]
					break
				}
			}
		}
		
		if item == nil && len(gameState.Inventory) > 0 {
			// Use first item if no ID specified
			item = &gameState.Inventory[0]
		}
		
		if item == nil {
			return &ChatResponse{
				Agent:   AgentGuide,
				Message: "You don't have any items in your inventory to sell.",
			}, nil
		}
		
		// Create an offer for the item
		offer := &Offer{
			ID:          generateID(),
			Type:        "other",
			Title:       fmt.Sprintf("Buy %s from %s", item.Name, gameState.PlayerID),
			Description: fmt.Sprintf("%s. %s", description, item.Name),
			Price:       price,
			ExpiresAt:   gameState.CurrentDate.Add(7 * 24 * time.Hour),
			IsTrickery:  getBool(parsedData, "is_trickery", false),
			CreatedBy:   gameState.PlayerID,
		}
		chatResponse.Offer = offer
		chatResponse.Item = item
	}

	return chatResponse, nil
}

// Helper function to get bool from map
func getBool(data map[string]interface{}, key string, defaultValue bool) bool {
	if val, ok := data[key]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return defaultValue
}


// generateContextAwareFallback generates a fallback response based on context
func (c *AIClient) generateContextAwareFallback(gameState *GameState, userMessage string) *ChatResponse {
	job := gameState.Job
	message := "I'm here to help you think through your work decisions! "
	
	if job != nil {
		salaryPerHour := 0.0
		if job.HoursPerDay > 0 && job.Salary > 0 {
			salaryPerHour = job.Salary / float64(job.HoursPerDay*20)
		}
		
		message += fmt.Sprintf("Looking at your current job as %s with €%.2f/month (€%.2f/hour) and %d hours per day, ", 
			job.Title, job.Salary, salaryPerHour, job.HoursPerDay)
		if job.WorkType == "fixed_time" {
			message += fmt.Sprintf("with a fixed schedule from %s to %s, ", job.WorkStart, job.WorkEnd)
		} else {
			message += "with flexible hours, "
		}
		message += "let's think about whether this aligns with your goals. "
	} else {
		message += "You don't currently have a job. "
		if len(gameState.JobOffers) > 0 {
			message += fmt.Sprintf("You have %d job offer(s) available to consider. ", len(gameState.JobOffers))
		}
	}
	
	message += "What factors are most important to you in a job?"
	
	questions := c.generateContextQuestions(gameState, userMessage)
	
	return &ChatResponse{
		Agent:     AgentGuide,
		Message:   message,
		Questions: questions,
	}
}

// generateContextQuestions generates context-aware questions
func (c *AIClient) generateContextQuestions(gameState *GameState, userMessage string) []string {
	questions := []string{}
	job := gameState.Job
	
	if job != nil {
		salaryPerHour := 0.0
		if job.HoursPerDay > 0 && job.Salary > 0 {
			salaryPerHour = job.Salary / float64(job.HoursPerDay*20)
		}
		
		questions = append(questions, fmt.Sprintf("Is your current salary of €%.2f/month (€%.2f/hour) meeting your financial needs?", 
			job.Salary, salaryPerHour))
		
		if job.WorkType == "fixed_time" {
			questions = append(questions, fmt.Sprintf("How does the fixed schedule (%s-%s) fit with your lifestyle?", 
				job.WorkStart, job.WorkEnd))
		} else {
			questions = append(questions, "Are you able to work the expected hours consistently with the flexible schedule?")
		}
		
		if len(gameState.JobOffers) > 0 {
			questions = append(questions, fmt.Sprintf("Have you compared this job with the %d other offer(s) available?", 
				len(gameState.JobOffers)))
		} else {
			questions = append(questions, "How does this job fit into your long-term career goals?")
		}
	} else {
		questions = append(questions, "What type of work schedule would work best for you?")
		questions = append(questions, "What salary range are you looking for?")
		if len(gameState.JobOffers) > 0 {
			questions = append(questions, fmt.Sprintf("Have you reviewed the %d job offer(s) available?", len(gameState.JobOffers)))
		}
	}
	
	// Ensure we have at least 2-3 questions
	if len(questions) < 2 {
		questions = append(questions, "What are your long-term career goals?")
		questions = append(questions, "How does this decision impact your financial stability?")
	}
	
	// Return max 3 questions
	maxQ := 3
	if len(questions) > maxQ {
		return questions[:maxQ]
	}
	return questions
}

// generateContextAwareMessage generates a context-aware message
func (c *AIClient) generateContextAwareMessage(gameState *GameState, userMessage string) string {
	job := gameState.Job
	if job == nil {
		return "You're currently unemployed. Let's think about what kind of job would be best for your situation."
	}
	
	salaryPerHour := 0.0
	if job.HoursPerDay > 0 && job.Salary > 0 {
		salaryPerHour = job.Salary / float64(job.HoursPerDay*20)
	}
	
	return fmt.Sprintf("Looking at your job as %s with €%.2f/month (€%.2f/hour), let's evaluate if this is the right choice for you.", 
		job.Title, job.Salary, salaryPerHour)
}

// parseTextResponse tries to extract message and questions from plain text response
func (c *AIClient) parseTextResponse(response string, gameState *GameState, userMessage string) *ChatResponse {
	// Try to find JSON-like structure in the text
	// Or extract meaningful content
	message := response
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	
	questions := c.generateContextQuestions(gameState, userMessage)
	
	return &ChatResponse{
		Agent:     AgentGuide,
		Message:   message,
		Questions: questions,
	}
}

// Helper functions for JSON extraction
func findJSONInResponse(text string) int {
	// Look for { that starts JSON
	for i, char := range text {
		if char == '{' {
			return i
		}
	}
	return -1
}

func findJSONEnd(text string) int {
	depth := 0
	for i, char := range text {
		if char == '{' {
			depth++
		} else if char == '}' {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// buildWorkContext builds a comprehensive work context string
func (c *AIClient) buildWorkContext(gameState *GameState) string {
	if gameState.Job == nil {
		context := "CURRENT JOB STATUS: Unemployed (no job)\n"
		if len(gameState.JobOffers) > 0 {
			context += fmt.Sprintf("- Available Job Offers: %d\n", len(gameState.JobOffers))
			context += "- Player should consider accepting a job offer\n"
		}
		return context
	}
	
	job := gameState.Job
	context := fmt.Sprintf("CURRENT JOB DETAILS:\n")
	context += fmt.Sprintf("- Job Title: %s\n", job.Title)
	context += fmt.Sprintf("- Description: %s\n", job.Description)
	context += fmt.Sprintf("- Monthly Salary: €%.2f\n", job.Salary)
	context += fmt.Sprintf("- Hours per Day: %d hours\n", job.HoursPerDay)
	
	if job.WorkType == "fixed_time" {
		context += fmt.Sprintf("- Work Type: FIXED SCHEDULE (automatic)\n")
		context += fmt.Sprintf("- Work Hours: %s to %s (work starts/ends automatically)\n", job.WorkStart, job.WorkEnd)
		context += "- Player cannot manually start/stop work\n"
	} else {
		context += fmt.Sprintf("- Work Type: FLEXIBLE HOURS (hourly)\n")
		context += "- Player can manually start and stop work anytime\n"
		context += fmt.Sprintf("- Expected hours per day: %d hours\n", job.HoursPerDay)
	}
	
	// Calculate salary per hour
	if job.HoursPerDay > 0 && job.Salary > 0 {
		salaryPerHour := job.Salary / float64(job.HoursPerDay*20) // Assuming 20 working days per month
		context += fmt.Sprintf("- Effective Hourly Rate: €%.2f/hour\n", salaryPerHour)
	}
	
	context += fmt.Sprintf("\nCURRENT WORK STATUS:\n")
	context += fmt.Sprintf("- Currently Working: %v\n", gameState.IsWorking)
	
	if gameState.IsWorking {
		if !gameState.WorkStartTime.IsZero() {
			context += fmt.Sprintf("- Work Session Started: %s\n", gameState.WorkStartTime.Format("2006-01-02 15:04"))
		}
		if !gameState.WorkEndTime.IsZero() {
			context += fmt.Sprintf("- Work Session Ends: %s\n", gameState.WorkEndTime.Format("2006-01-02 15:04"))
		}
	}
	
	if !gameState.LastSalaryDate.IsZero() {
		context += fmt.Sprintf("- Last Salary Payment Received: %s\n", gameState.LastSalaryDate.Format("2006-01-02"))
	}
	
	// Job offers available
	if len(gameState.JobOffers) > 0 {
		context += fmt.Sprintf("\nAVAILABLE ALTERNATIVES:\n")
		context += fmt.Sprintf("- Number of Job Offers Available: %d\n", len(gameState.JobOffers))
		context += "- Player could consider switching jobs\n"
	}
	
	return context
}

// buildApartmentContext builds apartment and health context
func (c *AIClient) buildApartmentContext(gameState *GameState) string {
	context := ""
	
	if gameState.Apartment == nil {
		context += "⚠️ CRITICAL: Player has NO APARTMENT!\n"
		context += "- Player will lose 2 health each night (00:00-07:00) without an apartment\n"
		context += "- This is a serious issue that needs to be addressed immediately\n"
		context += fmt.Sprintf("- Available apartment offers: %d\n", len(gameState.ApartmentOffers))
		context += "- Player should rent an apartment as soon as possible to avoid health loss\n"
	} else {
		apt := gameState.Apartment
		context += fmt.Sprintf("CURRENT APARTMENT:\n")
		context += fmt.Sprintf("- Title: %s\n", apt.Title)
		context += fmt.Sprintf("- Monthly Rent: €%.2f\n", apt.Rent)
		context += fmt.Sprintf("- Health Gain: +%d/hour (when not working)\n", apt.HealthGain)
		context += fmt.Sprintf("- Energy Gain: +%d/hour (when not working)\n", apt.EnergyGain)
	}
	
	context += fmt.Sprintf("\nCURRENT HEALTH & ENERGY:\n")
	context += fmt.Sprintf("- Health: %d/100", gameState.Health)
	if gameState.Health < 30 {
		context += " ⚠️ LOW HEALTH - Player needs to rest or get an apartment!"
	}
	context += "\n"
	context += fmt.Sprintf("- Energy: %d/100", gameState.Energy)
	if gameState.Energy < 30 {
		context += " ⚠️ LOW ENERGY - Player needs to rest!"
	}
	context += "\n"
	
	return context
}

// getRecentWorkEvents gets recent work-related events from history
func (c *AIClient) getRecentWorkEvents(gameState *GameState) string {
	if len(gameState.History) == 0 {
		return "No recent events."
	}
	
	workEventTypes := map[string]bool{
		"job_accepted":  true,
		"job_quit":      true,
		"work_start":    true,
		"work_end":      true,
		"work_stop":     true,
		"salary":        true,
		"hint_purchased": true,
		"apartment_rented": true,
		"apartment_quit": true,
		"rent_paid": true,
		"health_lost_no_apartment": true,
	}
	
	recentEvents := []string{}
	// Get last 10 events, filter for work-related ones
	startIdx := len(gameState.History) - 10
	if startIdx < 0 {
		startIdx = 0
	}
	
	for i := len(gameState.History) - 1; i >= startIdx && len(recentEvents) < 5; i-- {
		event := gameState.History[i]
		if workEventTypes[event.Type] {
			recentEvents = append(recentEvents, fmt.Sprintf("- %s: %s (Date: %s)", 
				event.Type, event.Message, event.Timestamp.Format("2006-01-02 15:04")))
		}
	}
	
	if len(recentEvents) == 0 {
		return "No recent work-related events."
	}
	
	result := ""
	for _, event := range recentEvents {
		result += event + "\n"
	}
	return result
}

// Helper functions
func (c *AIClient) generateFallbackTrickeryOffer(gameState *GameState) *Offer {
	return &Offer{
		ID:            generateID(),
		Type:          AgentTrickery,
		Title:         "Limited Time: Get Rich Quick Scheme",
		Description:   "Invest now and double your money in 7 days! No risk! (Warning: This is a scam)",
		Price:         gameState.Money * 0.3,
		OriginalPrice: gameState.Money * 0.5,
		Discount:      40,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		IsTrickery:    true,
		Reason:        "Get-rich-quick schemes are always scams. Real investments take time and have risks.",
	}
}

func (c *AIClient) generateFallbackGoodOffer(gameState *GameState) *Offer {
	return &Offer{
		ID:            generateID(),
		Type:          AgentOffers,
		Title:         "Diversified Investment Package",
		Description:   "A balanced mix of stocks and bonds with proven track record. 15% annual return expected.",
		Price:         gameState.Money * 0.2,
		OriginalPrice: gameState.Money * 0.3,
		Discount:      25,
		ExpiresAt:     time.Now().Add(24 * time.Hour),
		IsTrickery:    false,
		Reason:        "Diversified portfolio reduces risk while maintaining growth potential.",
	}
}

func getString(data map[string]interface{}, key, defaultValue string) string {
	if val, ok := data[key].(string); ok {
		return val
	}
	return defaultValue
}

func getFloat(data map[string]interface{}, key string, defaultValue float64) float64 {
	if val, ok := data[key]; ok {
		switch v := val.(type) {
		case float64:
			return v
		case float32:
			return float64(v)
		case int:
			return float64(v)
		case int64:
			return float64(v)
		case int32:
			return float64(v)
		case string:
			// Try to parse string as float
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return f
			}
		}
	}
	return defaultValue
}

func getJobTitle(gs *GameState) string {
	if gs.Job != nil {
		return gs.Job.Title
	}
	return "None"
}

// GenerateJobOffer generates a job offer using AI (good or trickery)
func (c *AIClient) GenerateJobOffer(gameState *GameState, offerType string) (*JobOffer, error) {
	isTrickery := offerType == "trickery"
	
	// Randomly choose work type (50/50 chance)
	var workType string
	var workStart, workEnd string
	if rand.Float64() < 0.5 {
		workType = "fixed_time"
		// Generate random work hours (e.g., 09:00-17:00, 08:00-16:00, etc.)
		startHour := 8 + rand.Intn(3) // 8, 9, or 10
		endHour := startHour + 8 // 8 hours later
		workStart = fmt.Sprintf("%02d:00", startHour)
		workEnd = fmt.Sprintf("%02d:00", endHour)
	} else {
		workType = "hourly"
	}
	
	prompt := fmt.Sprintf(`You are a %s job offer agent. Create a job offer that %s.

Current game state:
- Player money: €%.2f
- Current date: %s
- Current job: %s

Create a job offer that:
1. Has a title and description
2. Monthly salary (reasonable range: €2000-€8000)
3. Hours per day (4-10 hours)
4. Work type: %s%s
5. Health loss per hour (0.5-3.0) - how much health is lost per hour of work. Physical jobs lose more, desk jobs lose less.
6. Energy loss per hour (1.0-5.0) - how much energy is lost per hour of work. Demanding jobs lose more energy.
7. Upfront cost (0-€2000) - for %s jobs, this should be 0. For trickery/scam jobs, this can be €100-€2000 (training fees, materials, "registration fees", etc.). This is a red flag!
8. %s

IMPORTANT: Health and energy loss should reflect the job's physical/mental demands:
- Physical jobs (construction, delivery): higher health loss (2.0-3.0), high energy loss (3.0-5.0)
- Desk jobs (office work, programming): low health loss (0.5-1.5), moderate energy loss (1.5-3.0)
- Service jobs (retail, customer service): moderate health loss (1.0-2.0), moderate-high energy loss (2.0-4.0)
- Trickery jobs might have unrealistic promises but high hidden costs (high health/energy loss, upfront fees)

IMPORTANT: Upfront costs are a major red flag for scams:
- Legitimate jobs: upfront_cost should be 0
- Scam jobs: upfront_cost can be €100-€2000 (training materials, registration fees, "starter kits", etc.)

Respond in JSON format:
{
  "title": "Job title",
  "description": "Job description",
  "salary": 5000.00,
  "hours_per_day": 8,
  "health_loss_per_hour": 1.5,
  "energy_loss_per_hour": 3.0,
  "upfront_cost": 0.00,
  "reason": "Why this is %s"
}`, 
		map[bool]string{true: "trickery", false: "good"}[isTrickery],
		map[bool]string{true: "seems attractive but has hidden issues (low pay per hour, unrealistic promises, etc.)", false: "is legitimate and fair"}[isTrickery],
		gameState.Money,
		gameState.CurrentDate.Format("2006-01-02"),
		getJobTitle(gameState),
		workType,
		map[bool]string{true: fmt.Sprintf(" (Fixed schedule: %s-%s)", workStart, workEnd), false: ""}[workType == "fixed_time"],
		map[bool]string{true: "Uses common job scam tactics (pyramid scheme, unpaid training, commission-only, etc.)", false: "Is transparent and fair"}[isTrickery],
		map[bool]string{true: "a trickery", false: "a good offer"}[isTrickery])
	
	systemMsg := map[bool]string{
		true:  "You are a job scam expert. Generate deceptive job offers that test financial literacy.",
		false: "You are a helpful job recruiter. Generate legitimate, fair job offers.",
	}[isTrickery]
	
	messages := []Message{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: prompt},
	}
	
	agentType := "job_offer_good"
	if isTrickery {
		agentType = "job_offer_trickery"
	}
	response, err := c.CallOpenAIWithAgent(agentType, messages)
	if err != nil {
		return c.generateFallbackJobOffer(gameState, isTrickery), nil
	}
	
	var offerData map[string]interface{}
	if err := json.Unmarshal([]byte(response), &offerData); err != nil {
		return c.generateFallbackJobOffer(gameState, isTrickery), nil
	}
	
	salary := getFloat(offerData, "salary", 3000)
	if salary < 1000 {
		salary = 1000
	}
	if salary > 10000 {
		salary = 10000
	}
	
	hours := int(getFloat(offerData, "hours_per_day", 8))
	if hours < 4 {
		hours = 4
	}
	if hours > 10 {
		hours = 10
	}
	
	// Get health and energy loss per hour (AI-determined)
	healthLossPerHour := getFloat(offerData, "health_loss_per_hour", 1.5)
	if healthLossPerHour < 0.1 {
		healthLossPerHour = 0.1
	}
	if healthLossPerHour > 5.0 {
		healthLossPerHour = 5.0
	}
	
	energyLossPerHour := getFloat(offerData, "energy_loss_per_hour", 3.0)
	if energyLossPerHour < 0.5 {
		energyLossPerHour = 0.5
	}
	if energyLossPerHour > 8.0 {
		energyLossPerHour = 8.0
	}
	
	// Get upfront cost (for scam jobs)
	upfrontCost := getFloat(offerData, "upfront_cost", 0)
	if upfrontCost < 0 {
		upfrontCost = 0
	}
	if upfrontCost > 2000 {
		upfrontCost = 2000
	}
	// Legitimate jobs should not have upfront costs
	if !isTrickery && upfrontCost > 0 {
		upfrontCost = 0
	}
	
	// Use the work type already determined at the start of the function
	offer := &JobOffer{
		ID:                generateID(),
		Type:              offerType,
		Title:             getString(offerData, "title", "Job Opportunity"),
		Description:       getString(offerData, "description", "A job opportunity"),
		Salary:            salary,
		HoursPerDay:       hours,
		WorkType:          workType,
		WorkStart:         workStart,
		WorkEnd:           workEnd,
		HealthLossPerHour: healthLossPerHour,
		EnergyLossPerHour: energyLossPerHour,
		UpfrontCost:       upfrontCost,
		ExpiresAt:         gameState.CurrentDate.Add(7 * 24 * time.Hour), // Expires in 7 days
		IsTrickery:        isTrickery,
		Reason:            getString(offerData, "reason", ""),
	}
	
	return offer, nil
}

// GenerateApartmentOffer generates an apartment offer using AI (good or trickery)
func (c *AIClient) GenerateApartmentOffer(gameState *GameState, offerType string) (*ApartmentOffer, error) {
	isTrickery := offerType == "trickery"
	
	prompt := fmt.Sprintf(`You are a %s apartment rental agent. Create an apartment rental offer that %s.

Current game state:
- Player money: €%.2f
- Current date: %s
- Current apartment: %s

Create an apartment offer that:
1. Has a title and description
2. Monthly rent (reasonable range: €300-€2000)
3. Health gain per hour (1-5 for good, 0-2 for trickery)
4. Energy gain per hour (2-8 for good, 0-3 for trickery)
5. %s

Respond in JSON format:
{
  "title": "Apartment title",
  "description": "Apartment description",
  "rent": 800.00,
  "health_gain": 3,
  "energy_gain": 5,
  "reason": "Why this is %s"
}`, 
		map[bool]string{true: "trickery", false: "good"}[isTrickery],
		map[bool]string{true: "seems attractive but has hidden issues (overpriced, poor condition, hidden fees, etc.)", false: "is legitimate and good value"}[isTrickery],
		gameState.Money,
		gameState.CurrentDate.Format("2006-01-02"),
		func() string {
			if gameState.Apartment != nil {
				return gameState.Apartment.Title
			}
			return "None"
		}(),
		map[bool]string{true: "Uses common rental scam tactics (fake photos, hidden fees, deposit scams, etc.)", false: "Is transparent and fair"}[isTrickery],
		map[bool]string{true: "a trickery", false: "a good offer"}[isTrickery])
	
	systemMsg := map[bool]string{
		true:  "You are a rental scam expert. Generate deceptive apartment offers that test financial literacy.",
		false: "You are a helpful real estate agent. Generate legitimate, fair apartment rental offers.",
	}[isTrickery]
	
	messages := []Message{
		{Role: "system", Content: systemMsg},
		{Role: "user", Content: prompt},
	}
	
	agentType := "apartment_offer_good"
	if isTrickery {
		agentType = "apartment_offer_trickery"
	}
	response, err := c.CallOpenAIWithAgent(agentType, messages)
	if err != nil {
		return c.generateFallbackApartmentOffer(gameState, isTrickery), nil
	}
	
	var offerData map[string]interface{}
	if err := json.Unmarshal([]byte(response), &offerData); err != nil {
		return c.generateFallbackApartmentOffer(gameState, isTrickery), nil
	}
	
	rent := getFloat(offerData, "rent", 800)
	if rent < 200 {
		rent = 200
	}
	if rent > 3000 {
		rent = 3000
	}
	
	healthGain := int(getFloat(offerData, "health_gain", 3))
	if healthGain < 0 {
		healthGain = 0
	}
	if healthGain > 10 {
		healthGain = 10
	}
	
	energyGain := int(getFloat(offerData, "energy_gain", 5))
	if energyGain < 0 {
		energyGain = 0
	}
	if energyGain > 15 {
		energyGain = 15
	}
	
	offer := &ApartmentOffer{
		ID:          generateID(),
		Type:        offerType,
		Title:       getString(offerData, "title", "Apartment for Rent"),
		Description: getString(offerData, "description", "A nice apartment"),
		Rent:        rent,
		HealthGain:  healthGain,
		EnergyGain:  energyGain,
		ExpiresAt:   gameState.CurrentDate.Add(7 * 24 * time.Hour), // Expires in 7 days
		IsTrickery:  isTrickery,
		Reason:      getString(offerData, "reason", ""),
	}
	
	return offer, nil
}

func (c *AIClient) generateFallbackApartmentOffer(gameState *GameState, isTrickery bool) *ApartmentOffer {
	rent := 800.0
	healthGain := 3
	energyGain := 5
	title := "Cozy Studio Apartment"
	description := "Nice studio apartment in good location"
	reason := "Fair rent, good health and energy restoration"
	
	if isTrickery {
		rent = 1500.0
		healthGain = 1
		energyGain = 2
		title = "Luxury Apartment - Great Deal!"
		description = "Amazing apartment at unbeatable price!"
		reason = "Overpriced rent with poor health/energy restoration"
	}
	
	return &ApartmentOffer{
		ID:          generateID(),
		Type:        map[bool]string{true: "trickery", false: "good"}[isTrickery],
		Title:       title,
		Description: description,
		Rent:        rent,
		HealthGain:  healthGain,
		EnergyGain:  energyGain,
		ExpiresAt:   gameState.CurrentDate.Add(7 * 24 * time.Hour),
		IsTrickery:  isTrickery,
		Reason:      reason,
	}
}

func (c *AIClient) generateFallbackJobOffer(gameState *GameState, isTrickery bool) *JobOffer {
	// Randomly choose work type
	workType := "hourly"
	workStart := ""
	workEnd := ""
	if rand.Float64() < 0.5 {
		workType = "fixed_time"
		startHour := 8 + rand.Intn(3)
		endHour := startHour + 8
		workStart = fmt.Sprintf("%02d:00", startHour)
		workEnd = fmt.Sprintf("%02d:00", endHour)
	}
	
	if isTrickery {
		return &JobOffer{
			ID:                generateID(),
			Type:              "trickery",
			Title:             "Work from Home - Make €10,000/month!",
			Description:       "No experience needed! Just pay €500 for training materials and start earning immediately! Commission-based only.",
			Salary:            0, // Commission only
			HoursPerDay:       10,
			WorkType:          workType,
			WorkStart:         workStart,
			WorkEnd:           workEnd,
			HealthLossPerHour: 2.5, // High hidden cost
			EnergyLossPerHour: 4.5, // Very draining
			UpfrontCost:       500, // Training materials fee
			ExpiresAt:         gameState.CurrentDate.Add(7 * 24 * time.Hour),
			IsTrickery:        true,
			Reason:            "This is a scam - requires upfront payment, commission-only (no guaranteed salary), unrealistic promises",
		}
	}
	return &JobOffer{
		ID:                generateID(),
		Type:              "good",
		Title:             "Software Developer",
		Description:       "Full-time position with benefits. Competitive salary and growth opportunities.",
		Salary:            5000,
		HoursPerDay:       8,
		WorkType:          workType,
		WorkStart:         workStart,
		WorkEnd:           workEnd,
		HealthLossPerHour: 1.0, // Low for desk job
		EnergyLossPerHour: 2.0, // Moderate for office work
		UpfrontCost:       0,   // No upfront cost for legitimate jobs
		ExpiresAt:         gameState.CurrentDate.Add(7 * 24 * time.Hour),
		IsTrickery:        false,
		Reason:            "Fair salary, reasonable hours, legitimate opportunity",
	}
}

