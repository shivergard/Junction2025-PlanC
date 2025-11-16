package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Logging helpers for resource tracking
func logLockAcquire(lockType, playerID string) {
	_, file, line, _ := runtime.Caller(1)
	log.Printf("[LOCK_ACQUIRE] %s for player %s at %s:%d", lockType, playerID, file, line)
}

func logLockRelease(lockType, playerID string) {
	_, file, line, _ := runtime.Caller(1)
	log.Printf("[LOCK_RELEASE] %s for player %s at %s:%d", lockType, playerID, file, line)
}

func logGoroutineStart(name, playerID string) {
	log.Printf("[GOROUTINE_START] %s for player %s (goroutines: %d)", name, playerID, runtime.NumGoroutine())
}

func logGoroutineEnd(name, playerID string) {
	log.Printf("[GOROUTINE_END] %s for player %s (goroutines: %d)", name, playerID, runtime.NumGoroutine())
}

func logChannelOp(op, playerID string, channelLen, channelCap int) {
	log.Printf("[CHAN_%s] Player %s (len=%d, cap=%d)", op, playerID, channelLen, channelCap)
}

func logHTTPRequest(method, path, playerID string) {
	log.Printf("[HTTP_REQUEST] %s %s for player %s", method, path, playerID)
}

func logHTTPResponse(method, path, playerID string, statusCode int) {
	log.Printf("[HTTP_RESPONSE] %s %s for player %s - Status: %d", method, path, playerID, statusCode)
}

// GameManager manages game sessions
type GameManager struct {
	games                    map[string]*GameState
	ai                       *AIClient
	mu                       sync.RWMutex
	lastJobOfferGen          map[string]time.Time
	jobOfferGenMu            sync.Mutex
	lastApartmentOfferGen    map[string]time.Time
	apartmentOfferGenMu      sync.Mutex
	lastOtherOfferGen        map[string]time.Time
	otherOfferGenMu          sync.Mutex
	lastStockOfferGen        map[string]time.Time
	stockOfferGenMu          sync.Mutex
	// Invite code tracking: invite code -> player ID
	inviteCodes              map[string]string
	inviteCodesMu            sync.RWMutex
	firstPlayerID            string // Track the first player
	firstPlayerMu            sync.Mutex
	// Shared job offers: job offer ID -> network root player ID
	sharedJobOffers          map[string]string // Maps job offer ID to the network root player ID
	sharedJobOffersMu        sync.RWMutex
	// Caching
	stateCache               map[string]*cachedState // playerID -> cached state
	stateCacheMu             sync.RWMutex
	// JSON encoder pool for better performance
	jsonEncoderPool          sync.Pool
	// Encryption key cache
	encryptionKey            []byte
	encryptionKeyOnce        sync.Once
	// WebSocket connections
	wsConnections            map[string]*wsConnection // playerID -> connection
	wsConnectionsMu          sync.RWMutex
}

// wsConnection represents a WebSocket connection for a player
type wsConnection struct {
	conn     *websocket.Conn
	playerID string
	send     chan []byte
	manager  *GameManager
	mu       sync.Mutex
}

// WebSocket upgrader
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins in development
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// cachedState stores cached game state with ETag
type cachedState struct {
	state     *GameState
	etag      string
	timestamp time.Time
	jsonData  []byte
}

// NewGameManager creates a new game manager
func NewGameManager() *GameManager {
	gm := &GameManager{
		games:                 make(map[string]*GameState),
		ai:                    NewAIClient(),
		lastJobOfferGen:       make(map[string]time.Time),
		lastApartmentOfferGen: make(map[string]time.Time),
		lastOtherOfferGen:     make(map[string]time.Time),
		lastStockOfferGen:     make(map[string]time.Time),
		inviteCodes:           make(map[string]string),
		firstPlayerID:         "",
		sharedJobOffers:       make(map[string]string),
		stateCache:            make(map[string]*cachedState),
		wsConnections:         make(map[string]*wsConnection),
		jsonEncoderPool: sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		},
	}
	
	// Start background job offer generator
	go gm.autoGenerateJobOffers()
	
	// Start background apartment offer generator
	go gm.autoGenerateApartmentOffers()
	
	// Start background other offers generator
	go gm.autoGenerateOtherOffers()
	
	// Start background stock offers generator
	go gm.autoGenerateStockOffers()
	
	return gm
}

// autoGenerateJobOffers periodically generates job offers
func (gm *GameManager) autoGenerateJobOffers() {
	// Generate initial offers immediately
	gm.generateJobOffersForAllGames()
	
	// Generate more frequently: every 30-90 seconds (randomized for variability)
	for {
		// Random interval between 30-90 seconds
		interval := 30 + rand.Intn(60) // 30-90 seconds
		time.Sleep(time.Duration(interval) * time.Second)
		gm.generateJobOffersForAllGames()
	}
}

// generateJobOffersForAllGames generates job offers for all active games
func (gm *GameManager) generateJobOffersForAllGames() {
	gm.mu.RLock()
	playerIDs := make([]string, 0, len(gm.games))
	for playerID := range gm.games {
		playerIDs = append(playerIDs, playerID)
	}
	gm.mu.RUnlock()
	
	// Track which networks we've already generated offers for
	networksProcessed := make(map[string]bool)
	
	for _, playerID := range playerIDs {
		// Get network root to avoid generating duplicate offers for the same network
		networkRoot := gm.getNetworkRoot(playerID)
		if networksProcessed[networkRoot] {
			continue // Skip if we already processed this network
		}
		networksProcessed[networkRoot] = true
		
		game, err := gm.GetGame(playerID)
		if err != nil {
			continue
		}
		
		// Check if we should generate offers for this network (use network root's timing)
		gm.jobOfferGenMu.Lock()
		lastGen, exists := gm.lastJobOfferGen[networkRoot]
		shouldGen := !exists || time.Since(lastGen) >= 2*time.Minute
		gm.jobOfferGenMu.Unlock()
		
		if shouldGen {
			// Check total offers in network (use network root's count)
			gm.mu.RLock()
			networkGame, networkExists := gm.games[networkRoot]
			currentOffers := 0
			if networkExists {
				currentOffers = len(networkGame.JobOffers)
			}
			gm.mu.RUnlock()
			
			if currentOffers < 7 {
				// Randomly decide if it's a good or trickery offer
				offerType := "good"
				if rand.Float64() < 0.3 { // 30% chance of trickery
					offerType = "trickery"
				}
				
				jobOffer, err := gm.ai.GenerateJobOffer(game, offerType)
				if err == nil && jobOffer != nil {
					// Share the job offer with all players in the network
					gm.shareJobOfferWithNetwork(playerID, *jobOffer)
					
					// Notify all network players via WebSocket
					networkPlayers := gm.getNetworkPlayers(playerID)
					gm.wsConnectionsMu.RLock()
					for _, pid := range networkPlayers {
						if wsConn, exists := gm.wsConnections[pid]; exists {
							gm.mu.RLock()
							if g, exists := gm.games[pid]; exists {
								wsConn.sendGameState(g)
							}
							gm.mu.RUnlock()
						}
					}
					gm.wsConnectionsMu.RUnlock()
					
					gm.jobOfferGenMu.Lock()
					gm.lastJobOfferGen[networkRoot] = time.Now()
					gm.jobOfferGenMu.Unlock()
				}
			}
		}
	}
}

// autoGenerateApartmentOffers periodically generates apartment offers (3-6 per week)
func (gm *GameManager) autoGenerateApartmentOffers() {
	// Generate initial offers after a short delay
	time.Sleep(20 * time.Second)
	gm.generateApartmentOffersForAllGames()
	
	// Generate more frequently: every 45-120 seconds (randomized for variability)
	for {
		// Random interval between 45-120 seconds
		interval := 45 + rand.Intn(75) // 45-120 seconds
		time.Sleep(time.Duration(interval) * time.Second)
		gm.generateApartmentOffersForAllGames()
	}
}

// generateApartmentOffersForAllGames generates apartment offers for all active games
func (gm *GameManager) generateApartmentOffersForAllGames() {
	gm.mu.RLock()
	gameList := make(map[string]*GameState)
	for k, v := range gm.games {
		gameList[k] = v
	}
	gm.mu.RUnlock()
	
	gm.apartmentOfferGenMu.Lock()
	defer gm.apartmentOfferGenMu.Unlock()
	
	for playerID, game := range gameList {
		// Check if enough time has passed (30 seconds real time minimum)
		lastGen, exists := gm.lastApartmentOfferGen[playerID]
		if !exists || time.Since(lastGen) >= 30*time.Second {
			// Limit to max 7 apartment offers (increased from 6)
			gm.mu.RLock()
			currentOffers := 0
			if g, exists := gm.games[playerID]; exists {
				currentOffers = len(g.ApartmentOffers)
			}
			gm.mu.RUnlock()
			
			if currentOffers < 7 {
				// Generate a random apartment offer (70% good, 30% trickery)
				offerType := "good"
				if rand.Float64() < 0.3 {
					offerType = "trickery"
				}
				
				apartmentOffer, err := gm.ai.GenerateApartmentOffer(game, offerType)
				if err == nil && apartmentOffer != nil {
					gm.mu.Lock()
					if g, exists := gm.games[playerID]; exists {
						g.ApartmentOffers = append(g.ApartmentOffers, *apartmentOffer)
					}
					gm.mu.Unlock()
					
					// Notify player via WebSocket if connected
					gm.wsConnectionsMu.RLock()
					if wsConn, exists := gm.wsConnections[playerID]; exists {
						gm.mu.RLock()
						if g, exists := gm.games[playerID]; exists {
							wsConn.sendGameState(g)
						}
						gm.mu.RUnlock()
					}
					gm.wsConnectionsMu.RUnlock()
					
					gm.lastApartmentOfferGen[playerID] = time.Now()
				}
			}
		}
	}
}

// autoGenerateOtherOffers periodically generates random "other" offers
func (gm *GameManager) autoGenerateOtherOffers() {
	// Generate initial offers after a short delay
	time.Sleep(25 * time.Second)
	gm.generateOtherOffersForAllGames()
	
	// Generate more frequently: every 60-150 seconds (randomized for variability)
	for {
		// Random interval between 60-150 seconds
		interval := 60 + rand.Intn(90) // 60-150 seconds
		time.Sleep(time.Duration(interval) * time.Second)
		gm.generateOtherOffersForAllGames()
	}
}

// generateOtherOffersForAllGames generates other offers for all active games
func (gm *GameManager) generateOtherOffersForAllGames() {
	gm.mu.RLock()
	gameList := make(map[string]*GameState)
	for k, v := range gm.games {
		gameList[k] = v
	}
	gm.mu.RUnlock()
	
	gm.otherOfferGenMu.Lock()
	defer gm.otherOfferGenMu.Unlock()
	
	for playerID, game := range gameList {
		// Check if enough time has passed (30 seconds real time minimum)
		lastGen, exists := gm.lastOtherOfferGen[playerID]
		if !exists || time.Since(lastGen) >= 30*time.Second {
			// Limit to max 5 other offers (increased from 3)
			gm.mu.RLock()
			currentOffers := 0
			if g, exists := gm.games[playerID]; exists {
				for _, offer := range g.ActiveOffers {
					if offer.Type == "other" {
						currentOffers++
					}
				}
			}
			gm.mu.RUnlock()
			
			if currentOffers < 5 {
				otherOffer, err := gm.ai.GenerateOtherOffer(game)
				if err == nil && otherOffer != nil {
					gm.mu.Lock()
					if g, exists := gm.games[playerID]; exists {
						g.ActiveOffers = append(g.ActiveOffers, *otherOffer)
					}
					gm.mu.Unlock()
					
					// Notify player via WebSocket if connected
					gm.wsConnectionsMu.RLock()
					if wsConn, exists := gm.wsConnections[playerID]; exists {
						gm.mu.RLock()
						if g, exists := gm.games[playerID]; exists {
							wsConn.sendGameState(g)
						}
						gm.mu.RUnlock()
					}
					gm.wsConnectionsMu.RUnlock()
					
					gm.lastOtherOfferGen[playerID] = time.Now()
				}
			}
		}
	}
}

// generateInviteCode generates a unique invite code
func (gm *GameManager) generateInviteCode() string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // Excluding confusing chars
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

// GetOrCreateGame gets or creates a game for a player
func (gm *GameManager) GetOrCreateGame(playerID string) *GameState {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	
	if game, exists := gm.games[playerID]; exists {
		return game
	}
	
	game := NewGame(playerID)
	
	// Check if this is the first player
	gm.firstPlayerMu.Lock()
	if gm.firstPlayerID == "" {
		gm.firstPlayerID = playerID
		game.IsFirstPlayer = true
	}
	gm.firstPlayerMu.Unlock()
	
	// Generate invite code for this player (always uppercase)
	gm.inviteCodesMu.Lock()
	inviteCode := strings.ToUpper(gm.generateInviteCode())
	// Ensure uniqueness
	for {
		if _, exists := gm.inviteCodes[inviteCode]; !exists {
			break
		}
		inviteCode = strings.ToUpper(gm.generateInviteCode())
	}
	gm.inviteCodes[inviteCode] = playerID
	game.InviteCode = inviteCode
	gm.inviteCodesMu.Unlock()
	
	gm.games[playerID] = game
	
	// Sync time with network if this player was invited
	if game.InvitedBy != "" {
		if inviter, exists := gm.games[game.InvitedBy]; exists {
			game.CurrentDate = inviter.CurrentDate
		}
	}
	
	// Trigger job offer, apartment offer, stock offer, and other offer generation for new game
	go func() {
		time.Sleep(3 * time.Second) // Wait 3 seconds before generating first offers (faster)
		gm.generateJobOffersForAllGames()
		gm.generateApartmentOffersForAllGames()
		gm.generateStockOffersForAllGames()
		// Other offers will be generated after 25 seconds (handled by autoGenerateOtherOffers)
	}()
	
	return game
}

// CreateGameWithInvite creates a new game with an invite code
func (gm *GameManager) CreateGameWithInvite(playerID string, inviteCode string) (*GameState, error) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	
	// Check if player already exists
	if _, exists := gm.games[playerID]; exists {
		return nil, errors.New("player already exists")
	}
	
	// Validate invite code (case-insensitive lookup - convert to uppercase)
	inviteCodeUpper := strings.ToUpper(inviteCode)
	gm.inviteCodesMu.RLock()
	inviterID, exists := gm.inviteCodes[inviteCodeUpper]
	gm.inviteCodesMu.RUnlock()
	
	if !exists {
		return nil, errors.New("invalid invite code")
	}
	
	// Check if inviter exists
	inviter, inviterExists := gm.games[inviterID]
	if !inviterExists {
		return nil, errors.New("inviter not found")
	}
	
	// Create new game
	game := NewGame(playerID)
	game.InvitedBy = inviterID
	
	// Deduct 500 from new player's initial money (they start with less)
	inviteFee := 500.0
	if game.Money >= inviteFee {
		game.Money -= inviteFee
		game.InitialMoney = game.Money
		game.addEvent("invite_fee", fmt.Sprintf("Paid €%.2f invite fee to %s", inviteFee, inviterID), -inviteFee)
	} else {
		// If they don't have enough, set money to 0
		game.Money = 0
		game.InitialMoney = 0
		game.addEvent("invite_fee", fmt.Sprintf("Paid all money (€%.2f) as invite fee to %s", game.Money+inviteFee, inviterID), -(game.Money + inviteFee))
	}
	
	// Give 500 to inviter
	inviter.Money += inviteFee
	inviter.addEvent("invite_reward", fmt.Sprintf("Received €%.2f from %s (invite reward)", inviteFee, playerID), inviteFee)
	
	// Generate invite code for new player (always uppercase)
	gm.inviteCodesMu.Lock()
	newInviteCode := strings.ToUpper(gm.generateInviteCode())
	for {
		if _, exists := gm.inviteCodes[newInviteCode]; !exists {
			break
		}
		newInviteCode = strings.ToUpper(gm.generateInviteCode())
	}
	gm.inviteCodes[newInviteCode] = playerID
	game.InviteCode = newInviteCode
	gm.inviteCodesMu.Unlock()
	
	gm.games[playerID] = game
	
	// Sync time with inviter's network
	if inviter, exists := gm.games[inviterID]; exists {
		game.CurrentDate = inviter.CurrentDate
	}
	
	// Share existing job offers from the network with the new player
	// We already hold the lock, so use the unlocked version
	networkRoot := gm.getNetworkRootUnlocked(playerID)
	networkPlayers := []string{networkRoot}
	
	// Find all players who were invited by anyone in the network
	visited := make(map[string]bool)
	visited[networkRoot] = true
	
	// Recursively find all players in the network
	var findNetwork func(rootID string)
	findNetwork = func(rootID string) {
		for pid, g := range gm.games {
			if !visited[pid] && g.InvitedBy == rootID {
				networkPlayers = append(networkPlayers, pid)
				visited[pid] = true
				findNetwork(pid) // Recursively find players invited by this one
			}
		}
	}
	
	findNetwork(networkRoot)
	
	// Copy job offers from network (we already hold the lock)
	for _, pid := range networkPlayers {
		if pid != playerID {
			if networkGame, exists := gm.games[pid]; exists {
				// Copy job offers from network
				for _, offer := range networkGame.JobOffers {
					// Check if offer already exists
					exists := false
					for _, existingOffer := range game.JobOffers {
						if existingOffer.ID == offer.ID {
							exists = true
							break
						}
					}
					if !exists {
						game.JobOffers = append(game.JobOffers, offer)
					}
				}
				break // Only need to copy from one player in network
			}
		}
	}
	
	// Trigger offer generation
	go func() {
		time.Sleep(3 * time.Second)
		gm.generateJobOffersForAllGames()
		gm.generateApartmentOffersForAllGames()
		gm.generateStockOffersForAllGames()
	}()
	
	return game, nil
}

// getNetworkRoot finds the root player (first player) in the network
// Note: This function assumes the caller already holds the appropriate lock
func (gm *GameManager) getNetworkRootUnlocked(playerID string) string {
	visited := make(map[string]bool)
	currentID := playerID
	
	// Traverse up the invite chain to find the root
	for {
		if visited[currentID] {
			break // Prevent infinite loops
		}
		visited[currentID] = true
		
		game, exists := gm.games[currentID]
		if !exists {
			break
		}
		
		if game.IsFirstPlayer || game.InvitedBy == "" {
			return currentID
		}
		
		currentID = game.InvitedBy
	}
	
	return playerID // Fallback to current player if no root found
}

// getNetworkRoot finds the root player (first player) in the network
func (gm *GameManager) getNetworkRoot(playerID string) string {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return gm.getNetworkRootUnlocked(playerID)
}

// getNetworkPlayers returns all player IDs in the same network (connected via invites)
func (gm *GameManager) getNetworkPlayers(playerID string) []string {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	
	networkRoot := gm.getNetworkRootUnlocked(playerID)
	networkPlayers := []string{networkRoot}
	
	// Find all players who were invited by anyone in the network
	visited := make(map[string]bool)
	visited[networkRoot] = true
	
	// Recursively find all players in the network
	var findNetwork func(rootID string)
	findNetwork = func(rootID string) {
		for pid, game := range gm.games {
			if !visited[pid] && game.InvitedBy == rootID {
				networkPlayers = append(networkPlayers, pid)
				visited[pid] = true
				findNetwork(pid) // Recursively find players invited by this one
			}
		}
	}
	
	findNetwork(networkRoot)
	
	return networkPlayers
}

// syncTimeAcrossNetwork syncs game time across all players in the network and notifies via WebSocket
func (gm *GameManager) syncTimeAcrossNetwork(playerID string, newTime time.Time) {
	networkPlayers := gm.getNetworkPlayers(playerID)
	
	gm.mu.Lock()
	for _, pid := range networkPlayers {
		if game, exists := gm.games[pid]; exists {
			game.CurrentDate = newTime
		}
	}
	gm.mu.Unlock()
	
	// Notify all network players via WebSocket
	gm.wsConnectionsMu.RLock()
	for _, pid := range networkPlayers {
		if wsConn, exists := gm.wsConnections[pid]; exists {
			// Get fresh game state
			gm.mu.RLock()
			if game, exists := gm.games[pid]; exists {
				wsConn.sendGameState(game)
			}
			gm.mu.RUnlock()
		}
	}
	gm.wsConnectionsMu.RUnlock()
}

// removeJobOfferFromNetwork removes a job offer from all players in the network
func (gm *GameManager) removeJobOfferFromNetwork(playerID string, offerID string) {
	networkPlayers := gm.getNetworkPlayers(playerID)
	
	gm.mu.Lock()
	defer gm.mu.Unlock()
	
	for _, pid := range networkPlayers {
		if game, exists := gm.games[pid]; exists {
			// Remove the job offer
			for i, offer := range game.JobOffers {
				if offer.ID == offerID {
					game.JobOffers = append(game.JobOffers[:i], game.JobOffers[i+1:]...)
					break
				}
			}
		}
	}
	
	// Remove from shared job offers tracking
	gm.sharedJobOffersMu.Lock()
	delete(gm.sharedJobOffers, offerID)
	gm.sharedJobOffersMu.Unlock()
}

// removeOfferFromNetwork removes an offer from all players in the network
func (gm *GameManager) removeOfferFromNetwork(playerID string, offerID string) {
	networkPlayers := gm.getNetworkPlayers(playerID)
	
	gm.mu.Lock()
	defer gm.mu.Unlock()
	
	for _, pid := range networkPlayers {
		if game, exists := gm.games[pid]; exists {
			// Remove the offer
			for i, offer := range game.ActiveOffers {
				if offer.ID == offerID {
					game.ActiveOffers = append(game.ActiveOffers[:i], game.ActiveOffers[i+1:]...)
					break
				}
			}
		}
	}
}

// shareJobOfferWithNetwork adds a job offer to all players in the network
func (gm *GameManager) shareJobOfferWithNetwork(playerID string, offer JobOffer) {
	networkPlayers := gm.getNetworkPlayers(playerID)
	networkRoot := gm.getNetworkRoot(playerID)
	
	gm.mu.Lock()
	defer gm.mu.Unlock()
	
	// Track this offer as shared
	gm.sharedJobOffersMu.Lock()
	gm.sharedJobOffers[offer.ID] = networkRoot
	gm.sharedJobOffersMu.Unlock()
	
	// Add offer to all players in network
	for _, pid := range networkPlayers {
		if game, exists := gm.games[pid]; exists {
			// Check if offer already exists
			exists := false
			for _, existingOffer := range game.JobOffers {
				if existingOffer.ID == offer.ID {
					exists = true
					break
				}
			}
			if !exists {
				game.JobOffers = append(game.JobOffers, offer)
			}
		}
	}
}

// autoGenerateStockOffers periodically generates stock offers
func (gm *GameManager) autoGenerateStockOffers() {
	// Generate initial offers after a short delay
	time.Sleep(35 * time.Second)
	gm.generateStockOffersForAllGames()
	
	// Generate more frequently: every 50-130 seconds (randomized for variability)
	for {
		// Random interval between 50-130 seconds
		interval := 50 + rand.Intn(80) // 50-130 seconds
		time.Sleep(time.Duration(interval) * time.Second)
		gm.generateStockOffersForAllGames()
	}
}

// generateStockOffersForAllGames generates stock offers for all active games
func (gm *GameManager) generateStockOffersForAllGames() {
	gm.mu.RLock()
	gameList := make(map[string]*GameState)
	for k, v := range gm.games {
		gameList[k] = v
	}
	gm.mu.RUnlock()
	
	gm.stockOfferGenMu.Lock()
	defer gm.stockOfferGenMu.Unlock()
	
	for playerID, game := range gameList {
		// Check if enough time has passed (30 seconds real time minimum)
		lastGen, exists := gm.lastStockOfferGen[playerID]
		if !exists || time.Since(lastGen) >= 30*time.Second {
			// Limit to max 6 stock offers (increased from 5)
			gm.mu.RLock()
			currentOffers := 0
			if g, exists := gm.games[playerID]; exists {
				currentOffers = len(g.StockOffers)
			}
			gm.mu.RUnlock()
			
			if currentOffers < 6 {
				stockOffer, err := gm.ai.GenerateStockOffer(game)
				if err == nil && stockOffer != nil {
					gm.mu.Lock()
					if g, exists := gm.games[playerID]; exists {
						g.StockOffers = append(g.StockOffers, *stockOffer)
					}
					gm.mu.Unlock()
					
					// Notify player via WebSocket if connected
					gm.wsConnectionsMu.RLock()
					if wsConn, exists := gm.wsConnections[playerID]; exists {
						gm.mu.RLock()
						if g, exists := gm.games[playerID]; exists {
							wsConn.sendGameState(g)
						}
						gm.mu.RUnlock()
					}
					gm.wsConnectionsMu.RUnlock()
					
					gm.lastStockOfferGen[playerID] = time.Now()
				}
			}
		}
	}
}

// GetGame gets a game for a player
func (gm *GameManager) GetGame(playerID string) (*GameState, error) {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	
	game, exists := gm.games[playerID]
	if !exists {
		return nil, &GameError{Message: "Game not found"}
	}
	return game, nil
}

// getETag generates an ETag for a game state
func (gm *GameManager) getETag(game *GameState) string {
	// Use a hash of key fields that change frequently
	hash := sha256.New()
	hash.Write([]byte(fmt.Sprintf("%s-%.2f-%d-%d-%d-%v-%v",
		game.PlayerID,
		game.Money,
		game.Health,
		game.Energy,
		len(game.History),
		game.CurrentDate.Unix(),
		game.IsWorking,
	)))
	return fmt.Sprintf(`"%x"`, hash.Sum(nil)[:8])
}

// encodeJSON encodes game state to JSON with compression support
func (gm *GameManager) encodeJSON(game *GameState) ([]byte, string, error) {
	// Get buffer from pool
	buf := gm.jsonEncoderPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		gm.jsonEncoderPool.Put(buf)
	}()
	
	// Limit history size for performance (keep last 50 events)
	limitedGame := *game
	if len(limitedGame.History) > 50 {
		limitedGame.History = limitedGame.History[len(limitedGame.History)-50:]
	}
	
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(limitedGame); err != nil {
		return nil, "", err
	}
	
	data := buf.Bytes()
	// Remove trailing newline from json.Encoder
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	
	etag := gm.getETag(game)
	return data, etag, nil
}

// writeJSONResponse writes JSON response with compression and caching
func (gm *GameManager) writeJSONResponse(w http.ResponseWriter, r *http.Request, data []byte, etag string) {
	// Check if client has cached version
	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	
	// Set caching headers
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=5") // Cache for 5 seconds
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	
	// Check if client accepts gzip
	acceptsGzip := strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
	
	if acceptsGzip && len(data) > 1024 { // Only compress if > 1KB
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gz.Write(data)
	} else {
		w.Write(data)
	}
}

// HandleGetState returns the current game state with caching and compression
func (gm *GameManager) HandleGetState(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		playerID = "default"
	}
	
	game := gm.GetOrCreateGame(playerID)
	
	// Check cache first
	gm.stateCacheMu.RLock()
	cached, exists := gm.stateCache[playerID]
	gm.stateCacheMu.RUnlock()
	
	var data []byte
	var etag string
	var err error
	
	if exists && time.Since(cached.timestamp) < 2*time.Second {
		// Use cached data if fresh (< 2 seconds old)
		data = cached.jsonData
		etag = cached.etag
	} else {
		// Encode fresh
		data, etag, err = gm.encodeJSON(game)
		if err != nil {
			http.Error(w, "Failed to encode game state", http.StatusInternalServerError)
			return
		}
		
		// Update cache
		gm.stateCacheMu.Lock()
		gm.stateCache[playerID] = &cachedState{
			state:     game,
			etag:      etag,
			timestamp: time.Now(),
			jsonData:  data,
		}
		gm.stateCacheMu.Unlock()
	}
	
	gm.writeJSONResponse(w, r, data, etag)
}

// HandleAction handles player actions
func (gm *GameManager) HandleAction(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		playerID = "default"
	}
	
	game, err := gm.GetGame(playerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	
	var actionReq ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&actionReq); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	var result map[string]interface{}
	
	switch actionReq.Action {
	case "start_work":
		err = game.StartWork()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "accept_job_offer":
		offerID := getString(actionReq.Data, "offer_id", "")
		err = game.AcceptJobOffer(offerID)
		if err == nil {
			// Remove job offer from all players in the network
			gm.removeJobOfferFromNetwork(playerID, offerID)
		}
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "quit_job":
		err = game.QuitJob()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "stop_work":
		err = game.StopWork()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "show_hint":
		offerID := getString(actionReq.Data, "offer_id", "")
		err = game.ShowHint(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "accept_apartment_offer":
		offerID := getString(actionReq.Data, "offer_id", "")
		err = game.AcceptApartmentOffer(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "show_apartment_hint":
		offerID := getString(actionReq.Data, "offer_id", "")
		err = game.ShowApartmentHint(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "quit_apartment":
		err = game.QuitApartment()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "quit_agreement":
		agreementID := getString(actionReq.Data, "agreement_id", "")
		// Find the agreement before quitting to check if it's reciprocal
		// Copy all necessary data before QuitAgreement modifies/removes it
		var agreementCopy Agreement
		var agreementFound bool
		for _, a := range game.Agreements {
			if a.ID == agreementID {
				agreementCopy = a // Copy the agreement data
				agreementFound = true
				break
			}
		}
		
		if !agreementFound {
			result = map[string]interface{}{"success": false, "message": "Agreement not found"}
			break
		}
		
		// Calculate penalty BEFORE calling QuitAgreement (which might modify CurrentDate)
		penalty := 0.0
		daysActive := game.CurrentDate.Sub(agreementCopy.StartedAt).Hours() / 24
		switch agreementCopy.RecurrenceType {
		case "daily":
			if daysActive < 1 {
				penalty = 50.0
			} else if daysActive < 7 {
				penalty = 25.0
			}
		case "weekly":
			if daysActive < 7 {
				penalty = 100.0
			} else if daysActive < 30 {
				penalty = 50.0
			}
		case "monthly":
			if daysActive < 30 {
				penalty = 200.0
			} else if daysActive < 90 {
				penalty = 100.0
			}
		}
		
		err = game.QuitAgreement(agreementID)
		
		if err == nil && agreementCopy.OtherPartyID != "" && !agreementCopy.IsReciprocal && penalty > 0 {
			// This is a buyer canceling - creator should get penalty payment
			// The penalty was already deducted from the buyer in QuitAgreement
			// Now we need to give it to the creator
			log.Printf("[QUIT_AGREEMENT] Buyer %s canceled agreement %s, penalty €%.2f should go to creator %s", 
				playerID, agreementCopy.ID, penalty, agreementCopy.OtherPartyID)
			
			gm.mu.Lock()
			if creator, exists := gm.games[agreementCopy.OtherPartyID]; exists {
				// Find and remove the reciprocal agreement from creator
				reciprocalFound := false
				for idx, creatorAgreement := range creator.Agreements {
					if creatorAgreement.IsReciprocal && creatorAgreement.OtherPartyID == playerID {
						creator.Agreements = append(creator.Agreements[:idx], creator.Agreements[idx+1:]...)
						reciprocalFound = true
						log.Printf("[QUIT_AGREEMENT] Found and removed reciprocal agreement %s from creator %s", 
							creatorAgreement.ID, agreementCopy.OtherPartyID)
						break
					}
				}
				
				if reciprocalFound {
					creator.Money += penalty
					creator.addEvent("agreement_cancelled_penalty", 
						fmt.Sprintf("Received €%.2f early termination penalty from %s canceling %s", 
							penalty, playerID, agreementCopy.Title), penalty)
					log.Printf("[QUIT_AGREEMENT] Creator %s received penalty €%.2f from buyer %s", 
						agreementCopy.OtherPartyID, penalty, playerID)
					
					// Get fresh creator state after modifications
					creatorStateForSend := creator
					
					gm.mu.Unlock() // Release lock before WebSocket operations
					
					// Notify creator via WebSocket
					gm.wsConnectionsMu.RLock()
					if creatorWs, exists := gm.wsConnections[agreementCopy.OtherPartyID]; exists {
						// Re-acquire read lock to get fresh state
						gm.mu.RLock()
						if freshCreator, exists := gm.games[agreementCopy.OtherPartyID]; exists {
							creatorStateForSend = freshCreator
						}
						gm.mu.RUnlock()
						
						creatorWs.sendGameState(creatorStateForSend)
						log.Printf("[QUIT_AGREEMENT] Notified creator %s via WebSocket with %d agreements", 
							agreementCopy.OtherPartyID, len(creatorStateForSend.Agreements))
					} else {
						log.Printf("[QUIT_AGREEMENT] Creator %s not connected via WebSocket", agreementCopy.OtherPartyID)
					}
					gm.wsConnectionsMu.RUnlock()
					
					// Re-acquire lock for result
					gm.mu.Lock()
				} else {
					log.Printf("[QUIT_AGREEMENT] WARNING: Reciprocal agreement not found for creator %s, buyer %s", 
						agreementCopy.OtherPartyID, playerID)
					gm.mu.Unlock()
				}
			} else {
				log.Printf("[QUIT_AGREEMENT] WARNING: Creator %s not found", agreementCopy.OtherPartyID)
				gm.mu.Unlock()
			}
		}
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "advance_time":
		hours := getFloat(actionReq.Data, "hours", 0.0)
		if hours > 0 {
			oldTime := game.CurrentDate
			game.AdvanceTime(time.Duration(hours * float64(time.Hour)))
			newTime := game.CurrentDate
			
			// Sync time across all players in the network
			if !oldTime.Equal(newTime) {
				gm.syncTimeAcrossNetwork(playerID, newTime)
			}
			
			result = map[string]interface{}{"success": true, "message": "Time advanced"}
		} else {
			result = map[string]interface{}{"success": false, "message": "Invalid time duration"}
		}
		
	case "buy_stock":
		offerID := getString(actionReq.Data, "offer_id", "")
		shares := getInt(actionReq.Data, "shares")
		err = game.BuyStock(offerID, shares)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "sell_stock":
		symbol := getString(actionReq.Data, "symbol", "")
		shares := getInt(actionReq.Data, "shares")
		err = game.SellStock(symbol, shares)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "buy_crypto":
		symbol := getString(actionReq.Data, "symbol", "")
		amount := getFloat(actionReq.Data, "amount", 0.0)
		err = game.BuyCrypto(symbol, amount)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "sell_crypto":
		symbol := getString(actionReq.Data, "symbol", "")
		amount := getFloat(actionReq.Data, "amount", 0.0)
		err = game.SellCrypto(symbol, amount)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "buy_item":
		itemID := getString(actionReq.Data, "item_id", "")
		price := getFloat(actionReq.Data, "price", 0.0)
		err = game.BuyItem(itemID, price)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "sell_item":
		itemID := getString(actionReq.Data, "item_id", "")
		err = game.SellItem(itemID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "accept_offer":
		offerID := getString(actionReq.Data, "offer_id", "")
		
		// Find the offer before accepting to check if it's player-created
		var offer *Offer
		for i, o := range game.ActiveOffers {
			if o.ID == offerID {
				offer = &game.ActiveOffers[i]
				break
			}
		}
		
		err = game.AcceptOffer(offerID)
		if err == nil && offer != nil && offer.CreatedBy != "" && offer.CreatedBy != playerID {
			// Transfer money to the creator
			gm.mu.Lock()
			if creator, exists := gm.games[offer.CreatedBy]; exists {
				// Transfer money to creator
				creator.Money += offer.Price
				creator.addEvent("offer_sold", fmt.Sprintf("Sold %s to %s for €%.2f", offer.Title, playerID, offer.Price), offer.Price)
				
				// If it's a recurring offer, creator should also get an agreement
				if offer.IsRecurring {
					recurrenceType := offer.RecurrenceType
					if recurrenceType == "" {
						recurrenceType = "monthly"
					}
					
					// Create reciprocal agreement for creator (they provide the service)
					// The creator gets money periodically (positive MoneyChange)
					creatorAgreement := Agreement{
						ID:              generateID(),
						Title:           fmt.Sprintf("Providing %s to %s", offer.Title, playerID),
						Description:     fmt.Sprintf("You are providing %s to %s. You receive €%.2f per %s.", offer.Description, playerID, offer.Price, recurrenceType),
						RecurrenceType:  recurrenceType,
						StartedAt:       creator.CurrentDate,
						LastProcessedAt: creator.CurrentDate,
						HealthChange:    0, // Creator doesn't lose health/energy from providing service
						EnergyChange:    0,
						ReputationChange: 0,
						MoneyChange:     offer.Price, // Creator receives money periodically
						IsTrickery:      offer.IsTrickery,
						Reason:          fmt.Sprintf("Reciprocal agreement from selling %s", offer.Title),
						IsReciprocal:    true,  // Mark as reciprocal
						OtherPartyID:    playerID, // Track who the buyer is
						OriginalPrice:   offer.Price, // Store original price for penalty calculation
					}
					creator.Agreements = append(creator.Agreements, creatorAgreement)
					creator.addEvent("agreement_started", fmt.Sprintf("Started providing %s to %s (€%.2f per %s)", offer.Title, playerID, offer.Price, recurrenceType), 0)
					log.Printf("[ACCEPT_OFFER] Created reciprocal agreement for creator %s: ID=%s, Title=%s, OtherParty=%s", 
						offer.CreatedBy, creatorAgreement.ID, creatorAgreement.Title, playerID)
				} else {
					// For one-time offers, creator just gets the money (already done above)
					log.Printf("[ACCEPT_OFFER] One-time offer accepted, creator %s received €%.2f", offer.CreatedBy, offer.Price)
				}
				
				// Notify creator via WebSocket if connected
				gm.wsConnectionsMu.RLock()
				if creatorWs, exists := gm.wsConnections[offer.CreatedBy]; exists {
					creatorWs.sendGameState(creator)
					log.Printf("[ACCEPT_OFFER] Notified creator %s via WebSocket", offer.CreatedBy)
				}
				gm.wsConnectionsMu.RUnlock()
			}
			gm.mu.Unlock()
			
			// Remove offer from all players in the network
			gm.removeOfferFromNetwork(playerID, offerID)
		} else if err == nil && offer != nil {
			// Remove offer from all players in the network even if not player-created
			gm.removeOfferFromNetwork(playerID, offerID)
		}
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}
		
	case "next_day":
		game.NextDay()
		result = map[string]interface{}{"success": true, "message": "Day advanced"}
		
	default:
		result = map[string]interface{}{"success": false, "message": "Unknown action"}
	}
	
	// Invalidate cache for this player
	gm.stateCacheMu.Lock()
	delete(gm.stateCache, playerID)
	gm.stateCacheMu.Unlock()
	
	// Encode result with optimized JSON
	buf := gm.jsonEncoderPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		gm.jsonEncoderPool.Put(buf)
	}()
	
	// Limit history size
	limitedGame := *game
	if len(limitedGame.History) > 50 {
		limitedGame.History = limitedGame.History[len(limitedGame.History)-50:]
	}
	result["game_state"] = &limitedGame
	
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
	
	data := buf.Bytes()
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	
	// Write with compression
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && len(data) > 1024 {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gz.Write(data)
	} else {
		w.Write(data)
	}
}

// HandleGenerateOffer generates an AI offer
func (gm *GameManager) HandleGenerateOffer(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		playerID = "default"
	}
	
	offerType := r.URL.Query().Get("type") // "trickery" or "good"
	
	game, err := gm.GetGame(playerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	
	var offer *Offer
	if offerType == "trickery" {
		offer, err = gm.ai.GenerateTrickeryOffer(game)
	} else {
		offer, err = gm.ai.GenerateGoodOffer(game)
	}
	
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	// Add offer to game
	gm.mu.Lock()
	game.ActiveOffers = append(game.ActiveOffers, *offer)
	gm.mu.Unlock()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"offer":   offer,
		"game_state": game,
	})
}

// HandleGenerateJobOffer generates a job offer using AI
func (gm *GameManager) HandleGenerateJobOffer(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		playerID = "default"
	}
	
	offerType := r.URL.Query().Get("type") // "trickery" or "good"
	if offerType == "" {
		offerType = "good"
	}
	
	game, err := gm.GetGame(playerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	
	jobOffer, err := gm.ai.GenerateJobOffer(game, offerType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	// Add job offer to game
	gm.mu.Lock()
	game.JobOffers = append(game.JobOffers, *jobOffer)
	gm.mu.Unlock()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"job_offer": jobOffer,
		"game_state": game,
	})
}

// N8NWebhookResponse represents the response from n8n webhook
type N8NWebhookResponse struct {
	Message      *string                `json:"message,omitempty"`
	Offer        *Offer                 `json:"offer,omitempty"`
	JobOffer     *JobOffer              `json:"job_offer,omitempty"`
	ApartmentOffer *ApartmentOffer      `json:"apartment_offer,omitempty"`
	StockOffer   *StockOffer            `json:"stock_offer,omitempty"`
}

// callN8NWebhook calls the n8n webhook with offer details and message
// It accepts any offer type and sends it to the webhook
func (gm *GameManager) callN8NWebhook(offerType string, offerID string, offerData interface{}, message string, playerID string) (*N8NWebhookResponse, error) {
	config := GetConfig()
	if config.N8N.WebhookURL == "" {
		return nil, fmt.Errorf("n8n webhook URL not configured")
	}
	
	// Prepare request payload with offer data
	payload := map[string]interface{}{
		"offer_id":   offerID,
		"offer_type": offerType, // "job", "apartment", "stock", "other"
		"message":    message,
		"player_id":  playerID,
	}
	
	// Add offer-specific fields based on type
	switch offerType {
	case "job":
		if jobOffer, ok := offerData.(*JobOffer); ok {
			payload["offer_title"] = jobOffer.Title
			payload["offer_salary"] = jobOffer.Salary
			payload["job_offer"] = jobOffer
		}
	case "apartment":
		if aptOffer, ok := offerData.(*ApartmentOffer); ok {
			payload["offer_title"] = aptOffer.Title
			payload["offer_rent"] = aptOffer.Rent
			payload["apartment_offer"] = aptOffer
		}
	case "stock":
		if stockOffer, ok := offerData.(*StockOffer); ok {
			payload["offer_title"] = stockOffer.CompanyName
			payload["offer_price"] = stockOffer.CurrentPrice
			payload["stock_offer"] = stockOffer
		}
	case "other":
		if offer, ok := offerData.(*Offer); ok {
			payload["offer_title"] = offer.Title
			payload["offer_price"] = offer.Price
			payload["offer"] = offer
		}
	}
	
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %v", err)
	}
	
	// Create HTTP request
	req, err := http.NewRequest("POST", config.N8N.WebhookURL, bytes.NewBuffer(payloadJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	
	// Make request with timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call webhook: %v", err)
	}
	defer resp.Body.Close()
	
	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}
	
	// If no content or empty response, return nil (no update)
	if len(body) == 0 || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	
	// Try to parse JSON response
	var webhookResp N8NWebhookResponse
	if err := json.Unmarshal(body, &webhookResp); err != nil {
		// If not valid JSON, treat as message
		messageStr := string(body)
		webhookResp.Message = &messageStr
	}
	
	return &webhookResp, nil
}

// HandleOfferMessage handles sending a message to an offer via n8n webhook
// Supports all offer types: job, apartment, stock, and other offers
func (gm *GameManager) HandleOfferMessage(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		http.Error(w, "player_id is required", http.StatusBadRequest)
		return
	}
	
	// Parse request body
	var requestData struct {
		OfferID string `json:"offer_id"`
		Message string `json:"message"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	
	if requestData.OfferID == "" {
		http.Error(w, "offer_id is required", http.StatusBadRequest)
		return
	}
	
	if requestData.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}
	
	// Get game
	game, err := gm.GetGame(playerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	
	// Search for offer in all offer types
	var foundOfferType string
	var foundOfferIndex int = -1
	var foundOfferData interface{}
	
	gm.mu.Lock()
	// Search in ActiveOffers (other offers)
	for i := range game.ActiveOffers {
		if game.ActiveOffers[i].ID == requestData.OfferID {
			foundOfferType = "other"
			foundOfferIndex = i
			foundOfferData = &game.ActiveOffers[i]
			break
		}
	}
	
	// Search in JobOffers
	if foundOfferData == nil {
		for i := range game.JobOffers {
			if game.JobOffers[i].ID == requestData.OfferID {
				foundOfferType = "job"
				foundOfferIndex = i
				foundOfferData = &game.JobOffers[i]
				break
			}
		}
	}
	
	// Search in ApartmentOffers
	if foundOfferData == nil {
		for i := range game.ApartmentOffers {
			if game.ApartmentOffers[i].ID == requestData.OfferID {
				foundOfferType = "apartment"
				foundOfferIndex = i
				foundOfferData = &game.ApartmentOffers[i]
				break
			}
		}
	}
	
	// Search in StockOffers
	if foundOfferData == nil {
		for i := range game.StockOffers {
			if game.StockOffers[i].ID == requestData.OfferID {
				foundOfferType = "stock"
				foundOfferIndex = i
				foundOfferData = &game.StockOffers[i]
				break
			}
		}
	}
	gm.mu.Unlock()
	
	if foundOfferData == nil {
		http.Error(w, "Offer not found", http.StatusNotFound)
		return
	}
	
	// Call n8n webhook
	webhookResp, err := gm.callN8NWebhook(foundOfferType, requestData.OfferID, foundOfferData, requestData.Message, playerID)
	if err != nil {
		log.Printf("Error calling n8n webhook: %v", err)
		http.Error(w, fmt.Sprintf("Failed to send message: %v", err), http.StatusInternalServerError)
		return
	}
	
	// Update offer based on response
	gm.mu.Lock()
	offerUpdated := false
	
	// Add message to offer's message history and update if needed
	switch foundOfferType {
	case "other":
		if foundOfferIndex >= 0 && foundOfferIndex < len(game.ActiveOffers) {
			if game.ActiveOffers[foundOfferIndex].Messages == nil {
				game.ActiveOffers[foundOfferIndex].Messages = []string{}
			}
			game.ActiveOffers[foundOfferIndex].Messages = append(game.ActiveOffers[foundOfferIndex].Messages, requestData.Message)
			
			if webhookResp != nil && webhookResp.Message != nil {
				game.ActiveOffers[foundOfferIndex].Messages = append(game.ActiveOffers[foundOfferIndex].Messages, *webhookResp.Message)
			}
			
			if webhookResp != nil && webhookResp.Offer != nil {
				updatedOffer := webhookResp.Offer
				updatedOffer.ID = game.ActiveOffers[foundOfferIndex].ID
				updatedOffer.Messages = game.ActiveOffers[foundOfferIndex].Messages
				game.ActiveOffers[foundOfferIndex] = *updatedOffer
				offerUpdated = true
			}
		}
	case "job":
		if foundOfferIndex >= 0 && foundOfferIndex < len(game.JobOffers) {
			if game.JobOffers[foundOfferIndex].Messages == nil {
				game.JobOffers[foundOfferIndex].Messages = []string{}
			}
			game.JobOffers[foundOfferIndex].Messages = append(game.JobOffers[foundOfferIndex].Messages, requestData.Message)
			
			if webhookResp != nil && webhookResp.Message != nil {
				game.JobOffers[foundOfferIndex].Messages = append(game.JobOffers[foundOfferIndex].Messages, *webhookResp.Message)
			}
			
			if webhookResp != nil && webhookResp.JobOffer != nil {
				updatedOffer := webhookResp.JobOffer
				updatedOffer.ID = game.JobOffers[foundOfferIndex].ID
				updatedOffer.Messages = game.JobOffers[foundOfferIndex].Messages
				game.JobOffers[foundOfferIndex] = *updatedOffer
				offerUpdated = true
			}
		}
	case "apartment":
		if foundOfferIndex >= 0 && foundOfferIndex < len(game.ApartmentOffers) {
			if game.ApartmentOffers[foundOfferIndex].Messages == nil {
				game.ApartmentOffers[foundOfferIndex].Messages = []string{}
			}
			game.ApartmentOffers[foundOfferIndex].Messages = append(game.ApartmentOffers[foundOfferIndex].Messages, requestData.Message)
			
			if webhookResp != nil && webhookResp.Message != nil {
				game.ApartmentOffers[foundOfferIndex].Messages = append(game.ApartmentOffers[foundOfferIndex].Messages, *webhookResp.Message)
			}
			
			if webhookResp != nil && webhookResp.ApartmentOffer != nil {
				updatedOffer := webhookResp.ApartmentOffer
				updatedOffer.ID = game.ApartmentOffers[foundOfferIndex].ID
				updatedOffer.Messages = game.ApartmentOffers[foundOfferIndex].Messages
				game.ApartmentOffers[foundOfferIndex] = *updatedOffer
				offerUpdated = true
			}
		}
	case "stock":
		if foundOfferIndex >= 0 && foundOfferIndex < len(game.StockOffers) {
			if game.StockOffers[foundOfferIndex].Messages == nil {
				game.StockOffers[foundOfferIndex].Messages = []string{}
			}
			game.StockOffers[foundOfferIndex].Messages = append(game.StockOffers[foundOfferIndex].Messages, requestData.Message)
			
			if webhookResp != nil && webhookResp.Message != nil {
				game.StockOffers[foundOfferIndex].Messages = append(game.StockOffers[foundOfferIndex].Messages, *webhookResp.Message)
			}
			
			if webhookResp != nil && webhookResp.StockOffer != nil {
				updatedOffer := webhookResp.StockOffer
				updatedOffer.ID = game.StockOffers[foundOfferIndex].ID
				updatedOffer.Messages = game.StockOffers[foundOfferIndex].Messages
				game.StockOffers[foundOfferIndex] = *updatedOffer
				offerUpdated = true
			}
		}
	}
	gm.mu.Unlock()
	
	// Invalidate cache
	gm.stateCacheMu.Lock()
	delete(gm.stateCache, playerID)
	gm.stateCacheMu.Unlock()
	
	// Return response
	response := map[string]interface{}{
		"success": true,
		"message": "Message sent successfully",
		"offer_type": foundOfferType,
	}
	
	if webhookResp != nil {
		if webhookResp.Message != nil {
			response["response_message"] = *webhookResp.Message
		}
		if offerUpdated {
			response["offer_updated"] = true
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleChat handles chat with guide agent
func (gm *GameManager) HandleChat(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		playerID = "default"
	}
	
	game, err := gm.GetGame(playerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	
	var chatReq ChatMessage
	if err := json.NewDecoder(r.Body).Decode(&chatReq); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	// First, check if the message is trying to create an offer/agreement/item
	creationResponse, err := gm.ai.ParseChatForOfferCreation(game, chatReq.Message)
	if err == nil && creationResponse != nil && creationResponse.Created {
		// Player wants to create something
		gm.mu.Lock()
		
		if creationResponse.Offer != nil {
			// Add offer to game and share with network
			game.ActiveOffers = append(game.ActiveOffers, *creationResponse.Offer)
			// Share with network
			networkPlayers := gm.getNetworkPlayers(playerID)
			for _, pid := range networkPlayers {
				if pid != playerID {
					if networkGame, exists := gm.games[pid]; exists {
						networkGame.ActiveOffers = append(networkGame.ActiveOffers, *creationResponse.Offer)
					}
				}
			}
			// Notify all network players via WebSocket
			gm.wsConnectionsMu.RLock()
			for _, pid := range networkPlayers {
				if wsConn, exists := gm.wsConnections[pid]; exists {
					if g, exists := gm.games[pid]; exists {
						wsConn.sendGameState(g)
					}
				}
			}
			gm.wsConnectionsMu.RUnlock()
			creationResponse.Message = fmt.Sprintf("✅ Created offer: %s (€%.2f). It's now available to other players in your network!", creationResponse.Offer.Title, creationResponse.Offer.Price)
		} else if creationResponse.Agreement != nil {
			// For agreements, we create an offer that becomes an agreement when accepted
			// Use the price stored in the Agreement struct, or calculate from MoneyChange
			agreementPrice := creationResponse.Agreement.Price
			if agreementPrice <= 0 {
				// If price wasn't set, use absolute value of MoneyChange
				agreementPrice = -creationResponse.Agreement.MoneyChange
				if agreementPrice <= 0 {
					// If still invalid, default to a reasonable price
					agreementPrice = 50.0
				}
			}
			
			offer := &Offer{
				ID:              generateID(),
				Type:            "other",
				Title:           creationResponse.Agreement.Title,
				Description:     creationResponse.Agreement.Description,
				Price:           agreementPrice, // Use the calculated price
				ExpiresAt:       game.CurrentDate.Add(7 * 24 * time.Hour),
				IsTrickery:      creationResponse.Agreement.IsTrickery,
				HealthChange:    creationResponse.Agreement.HealthChange,
				EnergyChange:    creationResponse.Agreement.EnergyChange,
				ReputationChange: creationResponse.Agreement.ReputationChange,
				MoneyChange:     creationResponse.Agreement.MoneyChange,
				IsRecurring:     true,
				RecurrenceType:  creationResponse.Agreement.RecurrenceType,
				CreatedBy:       playerID,
			}
			game.ActiveOffers = append(game.ActiveOffers, *offer)
			// Share with network
			networkPlayers := gm.getNetworkPlayers(playerID)
			for _, pid := range networkPlayers {
				if pid != playerID {
					if networkGame, exists := gm.games[pid]; exists {
						networkGame.ActiveOffers = append(networkGame.ActiveOffers, *offer)
					}
				}
			}
			// Notify all network players via WebSocket
			gm.wsConnectionsMu.RLock()
			for _, pid := range networkPlayers {
				if wsConn, exists := gm.wsConnections[pid]; exists {
					if g, exists := gm.games[pid]; exists {
						wsConn.sendGameState(g)
					}
				}
			}
			gm.wsConnectionsMu.RUnlock()
			creationResponse.Offer = offer
			creationResponse.Message = fmt.Sprintf("✅ Created agreement offer: %s (€%.2f/%s). It's now available to other players in your network!", offer.Title, offer.Price, creationResponse.Agreement.RecurrenceType)
		}
		
		gm.mu.Unlock()
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(creationResponse)
		return
	}
	
	// Normal chat flow
	response, err := gm.ai.ChatWithGuide(game, chatReq.Message, chatReq.Context)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	// Add hint about creating offers if it's a general question
	if strings.Contains(strings.ToLower(chatReq.Message), "how") || strings.Contains(strings.ToLower(chatReq.Message), "can i") || strings.Contains(strings.ToLower(chatReq.Message), "create") {
		if !strings.Contains(response.Message, "create") && !strings.Contains(response.Message, "offer") {
			response.Message += "\n\n💡 Tip: You can create offers, agreements, or sell items to other players by describing what you want to offer in the chat! For example: 'I want to sell my laptop for €500' or 'I'm offering a monthly subscription service for €50/month'."
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// HandleGetMarketItems returns available market items
func (gm *GameManager) HandleGetMarketItems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(marketItems)
}

// HandleGetStockSymbols returns available stock symbols (deprecated - now using stock offers)
func (gm *GameManager) HandleGetStockSymbols(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stockSymbols)
}

// HandleGetCryptoSymbols returns available crypto symbols
func (gm *GameManager) HandleGetCryptoSymbols(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cryptoSymbols)
}

// HandleCreateWithInvite creates a new game with an invite code
func (gm *GameManager) HandleCreateWithInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PlayerID   string `json:"player_id"`
		InviteCode string `json:"invite_code"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request"})
		return
	}
	
	if req.PlayerID == "" || req.InviteCode == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "player_id and invite_code are required"})
		return
	}
	
	// Convert invite code to uppercase for case-insensitive lookup
	req.InviteCode = strings.ToUpper(req.InviteCode)
	
	game, err := gm.CreateGameWithInvite(req.PlayerID, req.InviteCode)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(game)
}

// HandleEncrypt encrypts game state data (optimized with goroutine for large data)
func (gm *GameManager) HandleEncrypt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Data string `json:"data"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	// For large data, encrypt in goroutine to avoid blocking
	if len(req.Data) > 100000 { // > 100KB
		done := make(chan struct {
			encrypted string
			err       error
		}, 1)
		
		go func() {
			encrypted, err := Encrypt(req.Data)
			done <- struct {
				encrypted string
				err       error
			}{encrypted, err}
		}()
		
		// Wait with timeout
		select {
		case result := <-done:
			if result.err != nil {
				http.Error(w, "Encryption failed: "+result.err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"encrypted": result.encrypted})
		case <-time.After(5 * time.Second):
			http.Error(w, "Encryption timeout", http.StatusRequestTimeout)
			return
		}
	} else {
		// Small data, encrypt synchronously
		encrypted, err := Encrypt(req.Data)
		if err != nil {
			http.Error(w, "Encryption failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"encrypted": encrypted})
	}
}

// HandleDecrypt decrypts game state data
func (gm *GameManager) HandleDecrypt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Encrypted string `json:"encrypted"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	
	decrypted, err := Decrypt(req.Encrypted)
	if err != nil {
		http.Error(w, "Decryption failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"decrypted": decrypted})
}

// HandleWebSocket handles WebSocket connections for real-time game updates
func (gm *GameManager) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		http.Error(w, "player_id required", http.StatusBadRequest)
		return
	}

	log.Printf("[WS_OPEN] Opening WebSocket connection for player %s", playerID)

	// Upgrade connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS_ERROR] WebSocket upgrade error for player %s: %v", playerID, err)
		return
	}

	log.Printf("[WS_OPEN] WebSocket connection upgraded for player %s", playerID)

	// Create connection handler
	sendChan := make(chan []byte, 256)
	log.Printf("[CHAN_OPEN] Created send channel for player %s (capacity: 256)", playerID)
	
	wsConn := &wsConnection{
		conn:     conn,
		playerID: playerID,
		send:     sendChan,
		manager:  gm,
	}

	// Register connection
	log.Printf("[LOCK_ACQUIRE] Acquiring wsConnectionsMu write lock for player %s", playerID)
	gm.wsConnectionsMu.Lock()
	// Close existing connection if any
	if oldConn, exists := gm.wsConnections[playerID]; exists {
		log.Printf("[WS_CLOSE] Closing existing WebSocket connection for player %s", playerID)
		oldConn.close()
	}
	gm.wsConnections[playerID] = wsConn
	log.Printf("[LOCK_RELEASE] Releasing wsConnectionsMu write lock for player %s", playerID)
	gm.wsConnectionsMu.Unlock()

	// Start goroutines
	log.Printf("[GOROUTINE_START] Starting writePump goroutine for player %s", playerID)
	go wsConn.writePump()
	log.Printf("[GOROUTINE_START] Starting readPump goroutine for player %s", playerID)
	go wsConn.readPump()

	// Send initial game state
	game := gm.GetOrCreateGame(playerID)
	wsConn.sendGameState(game)
}

// sendGameState sends game state to the WebSocket connection
func (c *wsConnection) sendGameState(game *GameState) {
	log.Printf("[SEND_STATE_START] Starting sendGameState for player %s", c.playerID)
	// Limit history for performance
	limitedGame := *game
	if len(limitedGame.History) > 50 {
		limitedGame.History = limitedGame.History[len(limitedGame.History)-50:]
	}

	msg := map[string]interface{}{
		"type":       "state",
		"game_state": &limitedGame,
	}

	log.Printf("[SEND_STATE_MARSHAL] Marshaling game state for player %s", c.playerID)
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("[SEND_STATE_ERROR] Error marshaling game state for player %s: %v", c.playerID, err)
		return
	}
	log.Printf("[SEND_STATE_MARSHALED] Marshaled game state for player %s (size: %d bytes)", c.playerID, len(data))

	select {
	case c.send <- data:
		logChannelOp("SEND", c.playerID+"_state", len(c.send), cap(c.send))
		log.Printf("[SEND_STATE_SUCCESS] Successfully queued game state for player %s", c.playerID)
	default:
		log.Printf("[SEND_STATE_FULL] Channel full for player %s, dropping game state", c.playerID)
		// Channel full, connection might be slow
	}
	log.Printf("[SEND_STATE_END] Finished sendGameState for player %s", c.playerID)
}

// readPump reads messages from the WebSocket connection
func (c *wsConnection) readPump() {
	log.Printf("[GOROUTINE_START] readPump started for player %s", c.playerID)
	defer func() {
		log.Printf("[GOROUTINE_END] readPump ending for player %s", c.playerID)
		log.Printf("[LOCK_ACQUIRE] Acquiring wsConnectionsMu write lock to unregister player %s", c.playerID)
		c.manager.wsConnectionsMu.Lock()
		delete(c.manager.wsConnections, c.playerID)
		log.Printf("[WS_UNREGISTER] Unregistered WebSocket connection for player %s", c.playerID)
		log.Printf("[LOCK_RELEASE] Releasing wsConnectionsMu write lock for player %s", c.playerID)
		c.manager.wsConnectionsMu.Unlock()
		log.Printf("[WS_CLOSE] Closing WebSocket connection (readPump) for player %s", c.playerID)
		c.conn.Close()
		log.Printf("[WS_CLOSED] WebSocket connection closed (readPump) for player %s", c.playerID)
	}()

	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		// Handle action message
		var actionMsg map[string]interface{}
		if err := json.Unmarshal(message, &actionMsg); err != nil {
			continue
		}

		action, ok := actionMsg["action"].(string)
		if !ok {
			continue
		}

		// Process action
		c.manager.processWebSocketAction(c.playerID, action, actionMsg["data"], c)
	}
}

// writePump writes messages to the WebSocket connection
func (c *wsConnection) writePump() {
	log.Printf("[GOROUTINE_START] writePump started for player %s", c.playerID)
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		log.Printf("[GOROUTINE_END] writePump ending for player %s", c.playerID)
		ticker.Stop()
		log.Printf("[WS_CLOSE] Closing WebSocket connection (writePump) for player %s", c.playerID)
		c.conn.Close()
		log.Printf("[WS_CLOSED] WebSocket connection closed (writePump) for player %s", c.playerID)
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Send queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// close closes the WebSocket connection
func (c *wsConnection) close() {
	log.Printf("[LOCK_ACQUIRE] Acquiring wsConnection.mu lock to close connection for player %s", c.playerID)
	c.mu.Lock()
	defer func() {
		log.Printf("[LOCK_RELEASE] Releasing wsConnection.mu lock for player %s", c.playerID)
		c.mu.Unlock()
	}()
	log.Printf("[CHAN_CLOSE] Closing send channel for player %s", c.playerID)
	close(c.send)
	log.Printf("[CHAN_CLOSED] Send channel closed for player %s", c.playerID)
	log.Printf("[WS_CLOSE] Closing WebSocket connection (close method) for player %s", c.playerID)
	c.conn.Close()
	log.Printf("[WS_CLOSED] WebSocket connection closed (close method) for player %s", c.playerID)
}

// processWebSocketAction processes an action from WebSocket
func (gm *GameManager) processWebSocketAction(playerID string, action string, data interface{}, wsConn *wsConnection) {
	game, err := gm.GetGame(playerID)
	if err != nil {
		wsConn.sendError("Game not found")
		return
	}

	var result map[string]interface{}

	// Convert data to map
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		dataMap = make(map[string]interface{})
	}

	switch action {
	case "advance_time":
		hours := getFloat(dataMap, "hours", 0.0)
		if hours > 0 {
			oldTime := game.CurrentDate
			game.AdvanceTime(time.Duration(hours * float64(time.Hour)))
			newTime := game.CurrentDate
			
			if !oldTime.Equal(newTime) {
				gm.syncTimeAcrossNetwork(playerID, newTime)
			}
			// For advance_time, we don't need to send state immediately
			// The frontend updates time locally, and state will be synced via periodic updates
			// This reduces WebSocket traffic significantly
			result = map[string]interface{}{"success": true, "message": "Time advanced", "skip_state": true}
		} else {
			result = map[string]interface{}{"success": false, "message": "Invalid time duration"}
		}

	case "start_work":
		err = game.StartWork()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "stop_work":
		err = game.StopWork()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "quit_job":
		err = game.QuitJob()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "accept_job_offer":
		offerID := getString(dataMap, "offer_id", "")
		err = game.AcceptJobOffer(offerID)
		if err == nil {
			gm.removeJobOfferFromNetwork(playerID, offerID)
		}
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "accept_apartment_offer":
		offerID := getString(dataMap, "offer_id", "")
		err = game.AcceptApartmentOffer(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "quit_apartment":
		err = game.QuitApartment()
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "accept_offer":
		offerID := getString(dataMap, "offer_id", "")
		var offer *Offer
		for i, o := range game.ActiveOffers {
			if o.ID == offerID {
				offer = &game.ActiveOffers[i]
				break
			}
		}
		err = game.AcceptOffer(offerID)
		if err == nil && offer != nil && offer.CreatedBy != "" && offer.CreatedBy != playerID {
			gm.mu.Lock()
			if creator, exists := gm.games[offer.CreatedBy]; exists {
				// Transfer money to creator
				creator.Money += offer.Price
				creator.addEvent("offer_sold", fmt.Sprintf("Sold %s to %s for €%.2f", offer.Title, playerID, offer.Price), offer.Price)
				
				// If it's a recurring offer, creator should also get an agreement
				if offer.IsRecurring {
					recurrenceType := offer.RecurrenceType
					if recurrenceType == "" {
						recurrenceType = "monthly"
					}
					
					// Create reciprocal agreement for creator (they provide the service)
					// The creator gets money periodically (positive MoneyChange)
					creatorAgreement := Agreement{
						ID:              generateID(),
						Title:           fmt.Sprintf("Providing %s to %s", offer.Title, playerID),
						Description:     fmt.Sprintf("You are providing %s to %s. You receive €%.2f per %s.", offer.Description, playerID, offer.Price, recurrenceType),
						RecurrenceType:  recurrenceType,
						StartedAt:       creator.CurrentDate,
						LastProcessedAt: creator.CurrentDate,
						HealthChange:    0, // Creator doesn't lose health/energy from providing service (or could be negative if it's work)
						EnergyChange:    0,
						ReputationChange: 0,
						MoneyChange:     offer.Price, // Creator receives money periodically
						IsTrickery:      offer.IsTrickery,
						Reason:          fmt.Sprintf("Reciprocal agreement from selling %s", offer.Title),
						IsReciprocal:    true,  // Mark as reciprocal
						OtherPartyID:    playerID, // Track who the buyer is
						OriginalPrice:   offer.Price, // Store original price for penalty calculation
					}
					creator.Agreements = append(creator.Agreements, creatorAgreement)
					creator.addEvent("agreement_started", fmt.Sprintf("Started providing %s to %s (€%.2f per %s)", offer.Title, playerID, offer.Price, recurrenceType), 0)
					log.Printf("[ACCEPT_OFFER] Created reciprocal agreement for creator %s: ID=%s, Title=%s, OtherParty=%s, IsReciprocal=%v", 
						offer.CreatedBy, creatorAgreement.ID, creatorAgreement.Title, playerID, creatorAgreement.IsReciprocal)
				} else {
					// For one-time offers, creator just gets the money (already done above)
					log.Printf("[ACCEPT_OFFER] One-time offer accepted, creator %s received €%.2f", offer.CreatedBy, offer.Price)
				}
				
				// Notify creator via WebSocket if connected
				creatorWsConn := (*wsConnection)(nil)
				gm.wsConnectionsMu.RLock()
				if ws, exists := gm.wsConnections[offer.CreatedBy]; exists {
					creatorWsConn = ws
				}
				gm.wsConnectionsMu.RUnlock()
				
				if creatorWsConn != nil {
					creatorWsConn.sendGameState(creator)
					log.Printf("[ACCEPT_OFFER] Notified creator %s via WebSocket", offer.CreatedBy)
				}
			}
			gm.mu.Unlock()
			gm.removeOfferFromNetwork(playerID, offerID)
		} else if err == nil && offer != nil {
			gm.removeOfferFromNetwork(playerID, offerID)
		}
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "buy_stock":
		offerID := getString(dataMap, "offer_id", "")
		shares := getInt(dataMap, "shares")
		err = game.BuyStock(offerID, shares)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "sell_stock":
		symbol := getString(dataMap, "symbol", "")
		shares := getInt(dataMap, "shares")
		err = game.SellStock(symbol, shares)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "buy_crypto":
		symbol := getString(dataMap, "symbol", "")
		amount := getFloat(dataMap, "amount", 0.0)
		err = game.BuyCrypto(symbol, amount)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "sell_crypto":
		symbol := getString(dataMap, "symbol", "")
		amount := getFloat(dataMap, "amount", 0.0)
		err = game.SellCrypto(symbol, amount)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "buy_item":
		itemID := getString(dataMap, "item_id", "")
		price := getFloat(dataMap, "price", 0.0)
		err = game.BuyItem(itemID, price)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "sell_item":
		itemID := getString(dataMap, "item_id", "")
		err = game.SellItem(itemID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "show_hint":
		offerID := getString(dataMap, "offer_id", "")
		err = game.ShowHint(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "show_apartment_hint":
		offerID := getString(dataMap, "offer_id", "")
		err = game.ShowApartmentHint(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "show_stock_hint":
		offerID := getString(dataMap, "offer_id", "")
		err = game.ShowStockHint(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "show_other_offer_hint":
		offerID := getString(dataMap, "offer_id", "")
		err = game.ShowOtherOfferHint(offerID)
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "quit_agreement":
		agreementID := getString(dataMap, "agreement_id", "")
		// Find the agreement before quitting to check if it's reciprocal
		// Copy all necessary data before QuitAgreement modifies/removes it
		var agreementCopy Agreement
		var agreementFound bool
		for _, a := range game.Agreements {
			if a.ID == agreementID {
				agreementCopy = a // Copy the agreement data
				agreementFound = true
				break
			}
		}
		
		if !agreementFound {
			result = map[string]interface{}{"success": false, "message": "Agreement not found"}
			break
		}
		
		// Calculate penalty BEFORE calling QuitAgreement (which might modify CurrentDate)
		penalty := 0.0
		daysActive := game.CurrentDate.Sub(agreementCopy.StartedAt).Hours() / 24
		switch agreementCopy.RecurrenceType {
		case "daily":
			if daysActive < 1 {
				penalty = 50.0
			} else if daysActive < 7 {
				penalty = 25.0
			}
		case "weekly":
			if daysActive < 7 {
				penalty = 100.0
			} else if daysActive < 30 {
				penalty = 50.0
			}
		case "monthly":
			if daysActive < 30 {
				penalty = 200.0
			} else if daysActive < 90 {
				penalty = 100.0
			}
		}
		
		err = game.QuitAgreement(agreementID)
		
		// Always send updated state to the buyer (person who canceled)
		// Get fresh game state after QuitAgreement
		var buyerStateForSend *GameState
		gm.mu.RLock()
		if g, exists := gm.games[playerID]; exists {
			buyerStateForSend = g
		}
		gm.mu.RUnlock()
		
		if err == nil && agreementCopy.OtherPartyID != "" && !agreementCopy.IsReciprocal && penalty > 0 {
			// This is a buyer canceling - creator should get penalty payment
			// The penalty was already deducted from the buyer in QuitAgreement
			// Now we need to give it to the creator
			log.Printf("[QUIT_AGREEMENT] Buyer %s canceled agreement %s, penalty €%.2f should go to creator %s", 
				playerID, agreementCopy.ID, penalty, agreementCopy.OtherPartyID)
			
			gm.mu.Lock()
			if creator, exists := gm.games[agreementCopy.OtherPartyID]; exists {
				// Find and remove the reciprocal agreement from creator
				reciprocalFound := false
				for idx, creatorAgreement := range creator.Agreements {
					if creatorAgreement.IsReciprocal && creatorAgreement.OtherPartyID == playerID {
						creator.Agreements = append(creator.Agreements[:idx], creator.Agreements[idx+1:]...)
						reciprocalFound = true
						log.Printf("[QUIT_AGREEMENT] Found and removed reciprocal agreement %s from creator %s", 
							creatorAgreement.ID, agreementCopy.OtherPartyID)
						break
					}
				}
				
				if reciprocalFound {
					creator.Money += penalty
					creator.addEvent("agreement_cancelled_penalty", 
						fmt.Sprintf("Received €%.2f early termination penalty from %s canceling %s", 
							penalty, playerID, agreementCopy.Title), penalty)
					log.Printf("[QUIT_AGREEMENT] Creator %s received penalty €%.2f from buyer %s. Remaining agreements: %d", 
						agreementCopy.OtherPartyID, penalty, playerID, len(creator.Agreements))
					
					// Get fresh creator state after modifications
					creatorStateForSend := creator
					
					gm.mu.Unlock() // Release lock before WebSocket operations
					
					// Notify creator via WebSocket
					gm.wsConnectionsMu.RLock()
					if creatorWs, exists := gm.wsConnections[agreementCopy.OtherPartyID]; exists {
						// Re-acquire read lock to get fresh state
						gm.mu.RLock()
						if freshCreator, exists := gm.games[agreementCopy.OtherPartyID]; exists {
							creatorStateForSend = freshCreator
						}
						gm.mu.RUnlock()
						
						creatorWs.sendGameState(creatorStateForSend)
						log.Printf("[QUIT_AGREEMENT] Notified creator %s via WebSocket with %d agreements", 
							agreementCopy.OtherPartyID, len(creatorStateForSend.Agreements))
					} else {
						log.Printf("[QUIT_AGREEMENT] Creator %s not connected via WebSocket", agreementCopy.OtherPartyID)
					}
					gm.wsConnectionsMu.RUnlock()
					
					// Lock already released above, no need to re-acquire
					// All modifications to creator are complete
				} else {
					log.Printf("[QUIT_AGREEMENT] WARNING: Reciprocal agreement not found for creator %s, buyer %s. Creator has %d agreements", 
						agreementCopy.OtherPartyID, playerID, len(creator.Agreements))
					// Log all creator's agreements for debugging
					for _, ag := range creator.Agreements {
						log.Printf("[QUIT_AGREEMENT] Creator agreement: ID=%s, IsReciprocal=%v, OtherPartyID=%s, Title=%s", 
							ag.ID, ag.IsReciprocal, ag.OtherPartyID, ag.Title)
					}
					gm.mu.Unlock()
				}
			} else {
				log.Printf("[QUIT_AGREEMENT] WARNING: Creator %s not found", agreementCopy.OtherPartyID)
				gm.mu.Unlock()
			}
		}
		
		// Send updated state to buyer (person who canceled)
		if buyerStateForSend != nil {
			wsConn.sendGameState(buyerStateForSend)
			log.Printf("[QUIT_AGREEMENT] Sent updated state to buyer %s with %d agreements", 
				playerID, len(buyerStateForSend.Agreements))
		}
		
		result = map[string]interface{}{"success": err == nil, "message": getMessage(err)}

	case "offer_message":
		// Handle sending message to offer via n8n webhook (supports all offer types)
		offerID := getString(dataMap, "offer_id", "")
		message := getString(dataMap, "message", "")
		
		if offerID == "" {
			result = map[string]interface{}{"success": false, "message": "offer_id is required"}
			break
		}
		
		if message == "" {
			result = map[string]interface{}{"success": false, "message": "message is required"}
			break
		}
		
		// Search for offer in all offer types
		var foundOfferType string
		var foundOfferIndex int = -1
		var foundOfferData interface{}
		
		// Search in ActiveOffers (other offers)
		for i := range game.ActiveOffers {
			if game.ActiveOffers[i].ID == offerID {
				foundOfferType = "other"
				foundOfferIndex = i
				foundOfferData = &game.ActiveOffers[i]
				break
			}
		}
		
		// Search in JobOffers
		if foundOfferData == nil {
			for i := range game.JobOffers {
				if game.JobOffers[i].ID == offerID {
					foundOfferType = "job"
					foundOfferIndex = i
					foundOfferData = &game.JobOffers[i]
					break
				}
			}
		}
		
		// Search in ApartmentOffers
		if foundOfferData == nil {
			for i := range game.ApartmentOffers {
				if game.ApartmentOffers[i].ID == offerID {
					foundOfferType = "apartment"
					foundOfferIndex = i
					foundOfferData = &game.ApartmentOffers[i]
					break
				}
			}
		}
		
		// Search in StockOffers
		if foundOfferData == nil {
			for i := range game.StockOffers {
				if game.StockOffers[i].ID == offerID {
					foundOfferType = "stock"
					foundOfferIndex = i
					foundOfferData = &game.StockOffers[i]
					break
				}
			}
		}
		
		if foundOfferData == nil {
			result = map[string]interface{}{"success": false, "message": "Offer not found"}
			break
		}
		
		// Call n8n webhook
		webhookResp, err := gm.callN8NWebhook(foundOfferType, offerID, foundOfferData, message, playerID)
		if err != nil {
			log.Printf("Error calling n8n webhook: %v", err)
			result = map[string]interface{}{"success": false, "message": fmt.Sprintf("Failed to send message: %v", err)}
			break
		}
		
		// Update offer based on response
		offerUpdated := false
		switch foundOfferType {
		case "other":
			if foundOfferIndex >= 0 && foundOfferIndex < len(game.ActiveOffers) {
				if game.ActiveOffers[foundOfferIndex].Messages == nil {
					game.ActiveOffers[foundOfferIndex].Messages = []string{}
				}
				game.ActiveOffers[foundOfferIndex].Messages = append(game.ActiveOffers[foundOfferIndex].Messages, message)
				
				if webhookResp != nil && webhookResp.Message != nil {
					game.ActiveOffers[foundOfferIndex].Messages = append(game.ActiveOffers[foundOfferIndex].Messages, *webhookResp.Message)
				}
				
				if webhookResp != nil && webhookResp.Offer != nil {
					updatedOffer := webhookResp.Offer
					updatedOffer.ID = game.ActiveOffers[foundOfferIndex].ID
					updatedOffer.Messages = game.ActiveOffers[foundOfferIndex].Messages
					game.ActiveOffers[foundOfferIndex] = *updatedOffer
					offerUpdated = true
				}
			}
		case "job":
			if foundOfferIndex >= 0 && foundOfferIndex < len(game.JobOffers) {
				if game.JobOffers[foundOfferIndex].Messages == nil {
					game.JobOffers[foundOfferIndex].Messages = []string{}
				}
				game.JobOffers[foundOfferIndex].Messages = append(game.JobOffers[foundOfferIndex].Messages, message)
				
				if webhookResp != nil && webhookResp.Message != nil {
					game.JobOffers[foundOfferIndex].Messages = append(game.JobOffers[foundOfferIndex].Messages, *webhookResp.Message)
				}
				
				if webhookResp != nil && webhookResp.JobOffer != nil {
					updatedOffer := webhookResp.JobOffer
					updatedOffer.ID = game.JobOffers[foundOfferIndex].ID
					updatedOffer.Messages = game.JobOffers[foundOfferIndex].Messages
					game.JobOffers[foundOfferIndex] = *updatedOffer
					offerUpdated = true
				}
			}
		case "apartment":
			if foundOfferIndex >= 0 && foundOfferIndex < len(game.ApartmentOffers) {
				if game.ApartmentOffers[foundOfferIndex].Messages == nil {
					game.ApartmentOffers[foundOfferIndex].Messages = []string{}
				}
				game.ApartmentOffers[foundOfferIndex].Messages = append(game.ApartmentOffers[foundOfferIndex].Messages, message)
				
				if webhookResp != nil && webhookResp.Message != nil {
					game.ApartmentOffers[foundOfferIndex].Messages = append(game.ApartmentOffers[foundOfferIndex].Messages, *webhookResp.Message)
				}
				
				if webhookResp != nil && webhookResp.ApartmentOffer != nil {
					updatedOffer := webhookResp.ApartmentOffer
					updatedOffer.ID = game.ApartmentOffers[foundOfferIndex].ID
					updatedOffer.Messages = game.ApartmentOffers[foundOfferIndex].Messages
					game.ApartmentOffers[foundOfferIndex] = *updatedOffer
					offerUpdated = true
				}
			}
		case "stock":
			if foundOfferIndex >= 0 && foundOfferIndex < len(game.StockOffers) {
				if game.StockOffers[foundOfferIndex].Messages == nil {
					game.StockOffers[foundOfferIndex].Messages = []string{}
				}
				game.StockOffers[foundOfferIndex].Messages = append(game.StockOffers[foundOfferIndex].Messages, message)
				
				if webhookResp != nil && webhookResp.Message != nil {
					game.StockOffers[foundOfferIndex].Messages = append(game.StockOffers[foundOfferIndex].Messages, *webhookResp.Message)
				}
				
				if webhookResp != nil && webhookResp.StockOffer != nil {
					updatedOffer := webhookResp.StockOffer
					updatedOffer.ID = game.StockOffers[foundOfferIndex].ID
					updatedOffer.Messages = game.StockOffers[foundOfferIndex].Messages
					game.StockOffers[foundOfferIndex] = *updatedOffer
					offerUpdated = true
				}
			}
		}
		
		responseMsg := "Message sent successfully"
		if webhookResp != nil && webhookResp.Message != nil {
			responseMsg = *webhookResp.Message
		}
		
		result = map[string]interface{}{
			"success": true,
			"message": responseMsg,
			"offer_type": foundOfferType,
			"offer_updated": offerUpdated,
		}

	case "chat":
		// Handle chat asynchronously to avoid blocking
		message := getString(dataMap, "message", "")
		context := getString(dataMap, "context", "")
		
		if message == "" {
			result = map[string]interface{}{"success": false, "message": "Message is required"}
		} else {
			// Process chat in a goroutine to avoid blocking
			// Chat handles its own state updates, so we'll skip the default state send at the end
			log.Printf("[GOROUTINE_START] Starting chat processing goroutine for player %s", playerID)
			go func() {
				defer func() {
					log.Printf("[GOROUTINE_END] Chat processing goroutine ending for player %s", playerID)
					if r := recover(); r != nil {
						log.Printf("[PANIC] PANIC in chat goroutine for player %s: %v", playerID, r)
						errorMsg := map[string]interface{}{
							"type":    "chat_response",
							"success": false,
							"message": "An error occurred processing your chat. Please try again.",
						}
						errorData, _ := json.Marshal(errorMsg)
						select {
						case wsConn.send <- errorData:
						default:
						}
					}
				}()
				
				log.Printf("[CHAT] Player %s sent message: %s", playerID, message)
				
				// Get fresh game state
				log.Printf("[LOCK_ACQUIRE] Acquiring gm.mu read lock for chat (player %s)", playerID)
				gm.mu.RLock()
				currentGame, exists := gm.games[playerID]
				if !exists {
					log.Printf("[LOCK_RELEASE] Releasing gm.mu read lock (game not found, player %s)", playerID)
					gm.mu.RUnlock()
					log.Printf("[CHAT] ERROR: Game not found for player %s", playerID)
					errorMsg := map[string]interface{}{
						"type":    "chat_response",
						"success": false,
						"message": "Game not found",
					}
					errorData, _ := json.Marshal(errorMsg)
					select {
					case wsConn.send <- errorData:
					default:
					}
					return
				}
				logLockRelease("gm.mu.RUnlock", playerID)
				gm.mu.RUnlock()
				
				// First, check if the message is trying to create an offer/agreement/item
				log.Printf("[CHAT] Parsing offer creation for player %s", playerID)
				
				// Call ParseChatForOfferCreation with timeout protection
				parseResponseChan := make(chan *ChatResponse, 1)
				parseErrorChan := make(chan error, 1)
				doneChan := make(chan bool, 1) // Signal when goroutine completes
				logChannelOp("OPEN", playerID+"_parse_resp", 0, 1)
				logChannelOp("OPEN", playerID+"_parse_err", 0, 1)
				logChannelOp("OPEN", playerID+"_parse_done", 0, 1)
				
				logGoroutineStart("ParseChatForOfferCreation", playerID)
				go func() {
					defer func() {
						logGoroutineEnd("ParseChatForOfferCreation", playerID)
						select {
						case doneChan <- true:
							logChannelOp("SEND", playerID+"_parse_done", len(doneChan), cap(doneChan))
						default:
							log.Printf("[CHAN_FULL] doneChan full for player %s", playerID)
						}
						if r := recover(); r != nil {
							log.Printf("[PANIC] PANIC in ParseChatForOfferCreation goroutine for player %s: %v", playerID, r)
							select {
							case parseErrorChan <- fmt.Errorf("panic: %v", r):
								logChannelOp("SEND", playerID+"_parse_err", len(parseErrorChan), cap(parseErrorChan))
							default:
								log.Printf("[CHAN_FULL] parseErrorChan full for player %s", playerID)
							}
						}
					}()
					
					log.Printf("[AI_CALL_START] ParseChatForOfferCreation for player %s", playerID)
					response, parseErr := gm.ai.ParseChatForOfferCreation(currentGame, message)
					log.Printf("[AI_CALL_END] ParseChatForOfferCreation for player %s (error: %v)", playerID, parseErr != nil)
					if parseErr != nil {
						select {
						case parseErrorChan <- parseErr:
							logChannelOp("SEND", playerID+"_parse_err", len(parseErrorChan), cap(parseErrorChan))
						case <-time.After(1 * time.Second):
							log.Printf("[CHAN_TIMEOUT] Could not send parse error for player %s (channel timeout)", playerID)
						}
					} else {
						select {
						case parseResponseChan <- response:
							logChannelOp("SEND", playerID+"_parse_resp", len(parseResponseChan), cap(parseResponseChan))
						case <-time.After(1 * time.Second):
							log.Printf("[CHAN_TIMEOUT] Could not send parse response for player %s (channel timeout)", playerID)
						}
					}
				}()
				
				var creationResponse *ChatResponse
				var parseErr error
				log.Printf("[SELECT_START] Waiting for ParseChatForOfferCreation response for player %s", playerID)
				select {
				case creationResponse = <-parseResponseChan:
					logChannelOp("RECV", playerID+"_parse_resp", len(parseResponseChan), cap(parseResponseChan))
					log.Printf("[CHAT] Received parse response for player %s", playerID)
				case parseErr = <-parseErrorChan:
					logChannelOp("RECV", playerID+"_parse_err", len(parseErrorChan), cap(parseErrorChan))
					log.Printf("[CHAT] Received parse error for player %s: %v", playerID, parseErr)
				case <-time.After(20 * time.Second):
					log.Printf("[TIMEOUT] ParseChatForOfferCreation timeout for player %s", playerID)
					parseErr = fmt.Errorf("Offer parsing timed out after 20 seconds")
					// Wait a bit for goroutine to finish (non-blocking)
					select {
					case <-doneChan:
						logChannelOp("RECV", playerID+"_parse_done", len(doneChan), cap(doneChan))
						log.Printf("[CHAT] ParseChatForOfferCreation goroutine completed after timeout for player %s", playerID)
					case <-time.After(2 * time.Second):
						log.Printf("[WARNING] ParseChatForOfferCreation goroutine still running after timeout for player %s", playerID)
					}
				}
				log.Printf("[SELECT_END] ParseChatForOfferCreation select completed for player %s", playerID)
				
				if parseErr != nil {
					log.Printf("[CHAT] ERROR parsing offer creation for player %s: %v", playerID, parseErr)
					// Continue with normal chat flow if parsing fails
					creationResponse = nil
				}
				
				if creationResponse != nil {
					log.Printf("[CHAT] Creation response for player %s: Created=%v, Offer=%v, Agreement=%v", 
						playerID, creationResponse.Created, creationResponse.Offer != nil, creationResponse.Agreement != nil)
				}
				
				if parseErr == nil && creationResponse != nil && creationResponse.Created {
					log.Printf("[CHAT] Processing offer/agreement creation for player %s", playerID)
					
					// Prepare network players list and offer/agreement data
					var networkPlayersCopy []string
					var responseMessage string
					
					// Lock to modify game state
					logLockAcquire("gm.mu.Lock", playerID)
					gm.mu.Lock()
					// Get fresh game state again (in case it changed)
					if g, exists := gm.games[playerID]; exists {
						currentGame = g
					} else {
						logLockRelease("gm.mu.Unlock", playerID)
						gm.mu.Unlock()
						log.Printf("[CHAT] ERROR: Game not found for player %s during offer creation", playerID)
						errorMsg := map[string]interface{}{
							"type":    "chat_response",
							"success": false,
							"message": "Game not found",
						}
						errorData, _ := json.Marshal(errorMsg)
						select {
						case wsConn.send <- errorData:
						default:
						}
						return
					}
					
					if creationResponse.Offer != nil {
						log.Printf("[CHAT] Creating offer for player %s: %s (€%.2f)", playerID, creationResponse.Offer.Title, creationResponse.Offer.Price)
						// Add offer to game and share with network
						currentGame.ActiveOffers = append(currentGame.ActiveOffers, *creationResponse.Offer)
						log.Printf("[CHAT] Added offer to player %s's game. Total offers: %d", playerID, len(currentGame.ActiveOffers))
						
						// Share with network (we already hold the lock, so use unlocked version)
						networkRoot := gm.getNetworkRootUnlocked(playerID)
						networkPlayers := []string{networkRoot}
						
						// Find all players who were invited by anyone in the network
						visited := make(map[string]bool)
						visited[networkRoot] = true
						
						// Recursively find all players in the network
						var findNetwork func(rootID string)
						findNetwork = func(rootID string) {
							for pid, g := range gm.games {
								if !visited[pid] && g.InvitedBy == rootID {
									networkPlayers = append(networkPlayers, pid)
									visited[pid] = true
									findNetwork(pid) // Recursively find players invited by this one
								}
							}
						}
						findNetwork(networkRoot)
						
						log.Printf("[CHAT] Sharing offer with network. Network players: %v", networkPlayers)
						
						for _, pid := range networkPlayers {
							if pid != playerID {
								if networkGame, exists := gm.games[pid]; exists {
									networkGame.ActiveOffers = append(networkGame.ActiveOffers, *creationResponse.Offer)
									log.Printf("[CHAT] Added offer to network player %s", pid)
								}
							}
						}
						
						// Store network players for use after releasing lock
						networkPlayersCopy = make([]string, len(networkPlayers))
						copy(networkPlayersCopy, networkPlayers)
						
						responseMessage = fmt.Sprintf("✅ Created offer: %s (€%.2f). It's now available to other players in your network!", creationResponse.Offer.Title, creationResponse.Offer.Price)
						
					} else if creationResponse.Agreement != nil {
						// For agreements, we create an offer that becomes an agreement when accepted
						// Use the price stored in the Agreement struct, or calculate from MoneyChange
						agreementPrice := creationResponse.Agreement.Price
						if agreementPrice <= 0 {
							// If price wasn't set, use absolute value of MoneyChange
							agreementPrice = -creationResponse.Agreement.MoneyChange
							if agreementPrice <= 0 {
								// If still invalid, default to a reasonable price
								agreementPrice = 50.0
							}
						}
						
						offer := &Offer{
							ID:              generateID(),
							Type:            "other",
							Title:           creationResponse.Agreement.Title,
							Description:     creationResponse.Agreement.Description,
							Price:           agreementPrice,
							ExpiresAt:       currentGame.CurrentDate.Add(7 * 24 * time.Hour),
							IsTrickery:      creationResponse.Agreement.IsTrickery,
							HealthChange:    creationResponse.Agreement.HealthChange,
							EnergyChange:    creationResponse.Agreement.EnergyChange,
							ReputationChange: creationResponse.Agreement.ReputationChange,
							MoneyChange:     creationResponse.Agreement.MoneyChange,
							IsRecurring:     true,
							RecurrenceType:  creationResponse.Agreement.RecurrenceType,
							CreatedBy:       playerID,
						}
						currentGame.ActiveOffers = append(currentGame.ActiveOffers, *offer)
						
						// Share with network (we already hold the lock, so use unlocked version)
						networkRoot := gm.getNetworkRootUnlocked(playerID)
						networkPlayers := []string{networkRoot}
						
						// Find all players who were invited by anyone in the network
						visited := make(map[string]bool)
						visited[networkRoot] = true
						
						// Recursively find all players in the network
						var findNetwork func(rootID string)
						findNetwork = func(rootID string) {
							for pid, g := range gm.games {
								if !visited[pid] && g.InvitedBy == rootID {
									networkPlayers = append(networkPlayers, pid)
									visited[pid] = true
									findNetwork(pid) // Recursively find players invited by this one
								}
							}
						}
						findNetwork(networkRoot)
						
						for _, pid := range networkPlayers {
							if pid != playerID {
								if networkGame, exists := gm.games[pid]; exists {
									networkGame.ActiveOffers = append(networkGame.ActiveOffers, *offer)
								}
							}
						}
						
						// Store network players for use after releasing lock
						networkPlayersCopy = make([]string, len(networkPlayers))
						copy(networkPlayersCopy, networkPlayers)
						
						creationResponse.Offer = offer
						responseMessage = fmt.Sprintf("✅ Created agreement offer: %s (€%.2f/%s). It's now available to other players in your network!", offer.Title, offer.Price, creationResponse.Agreement.RecurrenceType)
					} else {
						// Neither offer nor agreement - this shouldn't happen, but handle it
						logLockRelease("gm.mu.Unlock", playerID)
						gm.mu.Unlock()
						log.Printf("[CHAT] ERROR: Creation response has no offer or agreement for player %s", playerID)
						errorMsg := map[string]interface{}{
							"type":    "chat_response",
							"success": false,
							"message": "Invalid creation response",
						}
						errorData, _ := json.Marshal(errorMsg)
						select {
						case wsConn.send <- errorData:
						default:
						}
						return
					}
					
					// Get fresh game state for sending (while lock is held)
					var gameStateForSend *GameState
					if g, exists := gm.games[playerID]; exists {
						gameStateForSend = g
					}
					
					// Release lock before WebSocket operations
					logLockRelease("gm.mu.Unlock", playerID)
					gm.mu.Unlock()
					
					// Notify all network players via WebSocket (outside of lock)
					if len(networkPlayersCopy) > 0 {
						logLockAcquire("wsConnectionsMu.RLock", playerID)
						gm.wsConnectionsMu.RLock()
						notifiedCount := 0
						for _, pid := range networkPlayersCopy {
							if pid != playerID {
								if wsConn, exists := gm.wsConnections[pid]; exists {
									logLockAcquire("gm.mu.RLock", pid)
									gm.mu.RLock()
									if g, exists := gm.games[pid]; exists {
										wsConn.sendGameState(g)
										notifiedCount++
									}
									logLockRelease("gm.mu.RUnlock", pid)
									gm.mu.RUnlock()
								}
							}
						}
						logLockRelease("wsConnectionsMu.RUnlock", playerID)
						gm.wsConnectionsMu.RUnlock()
						log.Printf("[CHAT] Notified %d network players via WebSocket", notifiedCount)
					}
					
					log.Printf("[CHAT] About to update creation response message for player %s", playerID)
					// Update creation response message
					creationResponse.Message = responseMessage
					log.Printf("[CHAT] Updated creation response message for player %s", playerID)
					
					// Store response data (no lock needed here)
					log.Printf("[CHAT] About to marshal response data for player %s", playerID)
					responseData, err := json.Marshal(map[string]interface{}{
						"type":    "chat_response",
						"success": true,
						"result":  creationResponse,
					})
					if err != nil {
						log.Printf("[CHAT] ERROR marshaling response data for player %s: %v", playerID, err)
					} else {
						log.Printf("[CHAT] Successfully marshaled response data for player %s (size: %d bytes)", playerID, len(responseData))
					}
					
					log.Printf("[CHAT] Sending creation response to player %s via WebSocket", playerID)
					// Send response via WebSocket
					if err != nil {
						log.Printf("[CHAT] ERROR marshaling response for player %s: %v", playerID, err)
					} else {
						select {
						case wsConn.send <- responseData:
							log.Printf("[CHAT] Successfully sent response to player %s", playerID)
						default:
							log.Printf("[CHAT] WARNING: WebSocket send channel full for player %s", playerID)
						}
					}
					
					// Send updated state to creator
					if gameStateForSend != nil {
						log.Printf("[CHAT] About to send updated game state to player %s", playerID)
						wsConn.sendGameState(gameStateForSend)
						log.Printf("[CHAT] Sent updated game state to player %s", playerID)
					} else {
						log.Printf("[CHAT] ERROR: Game not found when sending state to player %s", playerID)
					}
					log.Printf("[CHAT] Completed offer creation for player %s", playerID)
					return
				}
				
				// Normal chat flow - get fresh game state
				log.Printf("[CHAT] Processing normal chat for player %s", playerID)
				
				// Get fresh game state for chat (in case it changed)
				var freshGameForChat *GameState
				logLockAcquire("gm.mu.RLock", playerID)
				gm.mu.RLock()
				if g, exists := gm.games[playerID]; exists {
					// Create a copy to avoid holding the lock during AI call
					freshGameForChat = g
				}
				logLockRelease("gm.mu.RUnlock", playerID)
				gm.mu.RUnlock()
				
				if freshGameForChat == nil {
					log.Printf("[CHAT] ERROR: Game not found for player %s in normal chat", playerID)
					errorMsg := map[string]interface{}{
						"type":    "chat_response",
						"success": false,
						"message": "Game not found",
					}
					errorData, _ := json.Marshal(errorMsg)
					select {
					case wsConn.send <- errorData:
					default:
					}
					return
				}
				
				// Call ChatWithGuide with timeout protection
				chatResponseChan := make(chan *ChatResponse, 1)
				errorChan := make(chan error, 1)
				chatDoneChan := make(chan bool, 1) // Signal when goroutine completes
				logChannelOp("OPEN", playerID+"_chat_resp", 0, 1)
				logChannelOp("OPEN", playerID+"_chat_err", 0, 1)
				logChannelOp("OPEN", playerID+"_chat_done", 0, 1)
				
				logGoroutineStart("ChatWithGuide", playerID)
				go func() {
					defer func() {
						logGoroutineEnd("ChatWithGuide", playerID)
						select {
						case chatDoneChan <- true:
							logChannelOp("SEND", playerID+"_chat_done", len(chatDoneChan), cap(chatDoneChan))
						default:
							log.Printf("[CHAN_FULL] chatDoneChan full for player %s", playerID)
						}
						if r := recover(); r != nil {
							log.Printf("[PANIC] PANIC in ChatWithGuide goroutine for player %s: %v", playerID, r)
							select {
							case errorChan <- fmt.Errorf("panic: %v", r):
								logChannelOp("SEND", playerID+"_chat_err", len(errorChan), cap(errorChan))
							default:
								log.Printf("[CHAN_FULL] errorChan full for player %s", playerID)
							}
						}
					}()
					
					log.Printf("[AI_CALL_START] ChatWithGuide for player %s", playerID)
					response, chatErr := gm.ai.ChatWithGuide(freshGameForChat, message, context)
					log.Printf("[AI_CALL_END] ChatWithGuide for player %s (error: %v)", playerID, chatErr != nil)
					if chatErr != nil {
						select {
						case errorChan <- chatErr:
							logChannelOp("SEND", playerID+"_chat_err", len(errorChan), cap(errorChan))
						case <-time.After(1 * time.Second):
							log.Printf("[CHAN_TIMEOUT] Could not send chat error for player %s (channel timeout)", playerID)
						}
					} else {
						select {
						case chatResponseChan <- response:
							logChannelOp("SEND", playerID+"_chat_resp", len(chatResponseChan), cap(chatResponseChan))
						case <-time.After(1 * time.Second):
							log.Printf("[CHAN_TIMEOUT] Could not send chat response for player %s (channel timeout)", playerID)
						}
					}
				}()
				
				var chatResponse *ChatResponse
				var chatErr error
				log.Printf("[SELECT_START] Waiting for ChatWithGuide response for player %s", playerID)
				select {
				case chatResponse = <-chatResponseChan:
					logChannelOp("RECV", playerID+"_chat_resp", len(chatResponseChan), cap(chatResponseChan))
					log.Printf("[CHAT] Received chat response for player %s", playerID)
				case chatErr = <-errorChan:
					logChannelOp("RECV", playerID+"_chat_err", len(errorChan), cap(errorChan))
					log.Printf("[CHAT] Received chat error for player %s: %v", playerID, chatErr)
				case <-time.After(30 * time.Second):
					log.Printf("[TIMEOUT] ChatWithGuide timeout for player %s", playerID)
					chatErr = fmt.Errorf("Chat request timed out after 30 seconds")
					// Wait a bit for goroutine to finish (non-blocking)
					select {
					case <-chatDoneChan:
						logChannelOp("RECV", playerID+"_chat_done", len(chatDoneChan), cap(chatDoneChan))
						log.Printf("[CHAT] ChatWithGuide goroutine completed after timeout for player %s", playerID)
					case <-time.After(2 * time.Second):
						log.Printf("[WARNING] ChatWithGuide goroutine still running after timeout for player %s", playerID)
					}
				}
				log.Printf("[SELECT_END] ChatWithGuide select completed for player %s", playerID)
				
				if chatErr != nil {
					log.Printf("[CHAT] ERROR in ChatWithGuide for player %s: %v", playerID, chatErr)
					errorMsg := map[string]interface{}{
						"type":    "chat_response",
						"success": false,
						"message": "Error: " + chatErr.Error(),
					}
					errorData, _ := json.Marshal(errorMsg)
					select {
					case wsConn.send <- errorData:
						log.Printf("[CHAT] Sent error response to player %s", playerID)
					default:
						log.Printf("[CHAT] WARNING: Could not send error response to player %s (channel full)", playerID)
					}
					return
				}
				
				if chatResponse == nil {
					log.Printf("[CHAT] ERROR: ChatWithGuide returned nil response for player %s", playerID)
					errorMsg := map[string]interface{}{
						"type":    "chat_response",
						"success": false,
						"message": "No response from chat agent",
					}
					errorData, _ := json.Marshal(errorMsg)
					select {
					case wsConn.send <- errorData:
					default:
					}
					return
				}
				
				// Add hint about creating offers if it's a general question
				if strings.Contains(strings.ToLower(message), "how") || strings.Contains(strings.ToLower(message), "can i") || strings.Contains(strings.ToLower(message), "create") {
					if !strings.Contains(chatResponse.Message, "create") && !strings.Contains(chatResponse.Message, "offer") {
						chatResponse.Message += "\n\n💡 Tip: You can create offers, agreements, or sell items to other players by describing what you want to offer in the chat! For example: 'I want to sell my laptop for €500' or 'I'm offering a monthly subscription service for €50/month'."
					}
				}
				
				// Send chat response via WebSocket
				log.Printf("[CHAT] Sending normal chat response to player %s", playerID)
				response := map[string]interface{}{
					"type":    "chat_response",
					"success": true,
					"result":  chatResponse,
				}
				responseData, err := json.Marshal(response)
				if err != nil {
					log.Printf("[CHAT] ERROR marshaling chat response for player %s: %v", playerID, err)
				} else {
					select {
					case wsConn.send <- responseData:
						log.Printf("[CHAT] Successfully sent chat response to player %s", playerID)
					default:
						log.Printf("[CHAT] WARNING: WebSocket send channel full for player %s", playerID)
					}
				}
				log.Printf("[CHAT] Completed normal chat for player %s", playerID)
			}()
			
			// Return immediately - response will come via WebSocket
			result = map[string]interface{}{"success": true, "message": "Chat request received, processing..."}
		}

	default:
		result = map[string]interface{}{"success": false, "message": "Unknown action"}
	}

	// Invalidate cache
	gm.stateCacheMu.Lock()
	delete(gm.stateCache, playerID)
	gm.stateCacheMu.Unlock()

	// Skip state update for certain actions that handle their own state updates or don't need immediate state
	skipState := false
	if action == "chat" {
		// Chat action handles its own state updates in the goroutine
		skipState = true
	} else if result != nil {
		// Check if result explicitly requests to skip state update (e.g., advance_time)
		if skip, ok := result["skip_state"].(bool); ok && skip {
			skipState = true
		}
	}
	
	if skipState {
		// Just send the immediate response without state update
		response := map[string]interface{}{
			"type":    "action_result",
			"action":  action,
			"result":  result,
		}
		responseData, _ := json.Marshal(response)
		select {
		case wsConn.send <- responseData:
			log.Printf("[PROCESS_ACTION] Sent %s action result (skipped state) for player %s", action, playerID)
		default:
			log.Printf("[PROCESS_ACTION] Failed to send %s action result (channel full) for player %s", action, playerID)
		}
		return
	}

	// Get fresh game state (in case it was modified)
	gm.mu.RLock()
	freshGame, exists := gm.games[playerID]
	gm.mu.RUnlock()
	
	if !exists {
		wsConn.sendError("Game not found after action")
		return
	}

	// Send updated state (always send fresh state)
	result["game_state"] = freshGame
	log.Printf("[PROCESS_ACTION] Sending state update for action %s to player %s", action, playerID)
	wsConn.sendGameState(freshGame)

	// Send action result
	response := map[string]interface{}{
		"type":    "action_result",
		"action":  action,
		"result":  result,
	}
	responseData, _ := json.Marshal(response)
	select {
	case wsConn.send <- responseData:
	default:
		log.Printf("[PROCESS_ACTION] Failed to send action result (channel full) for player %s", playerID)
	}
}

// sendError sends an error message to the WebSocket connection
func (c *wsConnection) sendError(message string) {
	msg := map[string]interface{}{
		"type":    "error",
		"message": message,
	}
	data, _ := json.Marshal(msg)
	select {
	case c.send <- data:
	default:
	}
}

// Helper functions
func getMessage(err error) string {
	if err == nil {
		return "Success"
	}
	return err.Error()
}

func getInt(data map[string]interface{}, key string) int {
	if val, ok := data[key].(float64); ok {
		return int(val)
	}
	return 0
}

