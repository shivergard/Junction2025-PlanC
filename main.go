package main

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file if it exists
	_ = godotenv.Load()
	
	// Load configuration from config.json (with env var overrides)
	config := LoadConfig()
	
	// Initialize game manager
	gm := NewGameManager()
	
	// Setup router
	r := mux.NewRouter()
	
	// API routes
	api := r.PathPrefix("/api").Subrouter()
	// WebSocket endpoint (primary for real-time updates)
	api.HandleFunc("/ws", gm.HandleWebSocket)
	// HTTP endpoints (fallback/compatibility)
	api.HandleFunc("/state", gm.HandleGetState).Methods("GET")
	api.HandleFunc("/action", gm.HandleAction).Methods("POST")
	api.HandleFunc("/offer", gm.HandleGenerateOffer).Methods("GET")
	api.HandleFunc("/job-offer", gm.HandleGenerateJobOffer).Methods("GET")
	api.HandleFunc("/chat", gm.HandleChat).Methods("POST")
	api.HandleFunc("/market/items", gm.HandleGetMarketItems).Methods("GET")
	api.HandleFunc("/market/stocks", gm.HandleGetStockSymbols).Methods("GET")
	api.HandleFunc("/market/crypto", gm.HandleGetCryptoSymbols).Methods("GET")
	// Multiplayer/Invite endpoints
	api.HandleFunc("/create-with-invite", gm.HandleCreateWithInvite).Methods("POST")
	api.HandleFunc("/encrypt", gm.HandleEncrypt).Methods("POST")
	api.HandleFunc("/decrypt", gm.HandleDecrypt).Methods("POST")
	// Offer messaging endpoint (n8n integration)
	api.HandleFunc("/offer/message", gm.HandleOfferMessage).Methods("POST")
	
	// Serve static files
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./web/")))
	
	// Optimize HTTP server
	server := &http.Server{
		Addr:         ":" + config.Server.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}
	
	log.Printf("Server starting on port %s", config.Server.Port)
	log.Fatal(server.ListenAndServe())
}

