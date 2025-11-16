// Game state
let gameState = null;
let lastWebSocketState = null; // Track last state received from WebSocket
const API_BASE = '/api';
let PLAYER_ID = null; // Will be set after game creation
const STORAGE_KEY = 'planc_game_state';
let lastSaveTime = 0; // Track last save time to debounce saves

// Local time tracking for smooth UI updates
let lastServerTime = null; // Last known server time (Date object)
let lastServerTimeReceived = null; // When we received the server time (real timestamp)
let calculatedGameTime = null; // Currently calculated game time for display

// WebSocket connection
let ws = null;
let wsReconnectAttempts = 0;
const MAX_RECONNECT_ATTEMPTS = 5;
let useWebSocket = true; // Use WebSocket by default, fallback to HTTP if fails

// UI update interval
let uiUpdateInterval = null;

// Initialize on page load
document.addEventListener('DOMContentLoaded', async () => {
    // Check if invite code is in URL - if so, clear localStorage to allow new game
    const urlParams = new URLSearchParams(window.location.search);
    const inviteCodeFromUrl = urlParams.get('invite');
    
    // Check if user has existing game state in localStorage
    const savedEncryptedData = localStorage.getItem(STORAGE_KEY);
    
    if (savedEncryptedData && !inviteCodeFromUrl) {
        // Try to load existing game (only if no invite code in URL)
        try {
            await loadGameFromStorage();
            // If successful, hide modals and continue
            hideStoryModal();
            hideInviteModal();
            loadMarketData();
            setupEventListeners();
            setupTabs();
            startGameLoop();
        } catch (error) {
            console.error('Failed to load game from storage:', error);
            // Show story modal first if loading fails
            showStoryModal();
        }
    } else {
        // No saved game OR invite code in URL - show story modal first
        // If invite code is in URL, clear any existing localStorage
        if (inviteCodeFromUrl && savedEncryptedData) {
            localStorage.removeItem(STORAGE_KEY);
            console.log('Cleared existing game state to allow joining with invite code');
        }
        showStoryModal();
    }
    
    // Setup modal event listeners
    setupStoryModalListeners();
    setupInviteModalListeners();
});

// Start game loop (time advancement)
let gameLoopInterval = null;
let gameStateRefreshInterval = null;
let saveStateInterval = null;

// Connect to WebSocket
function connectWebSocket() {
    if (!PLAYER_ID || !useWebSocket) return;
    
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}${API_BASE}/ws?player_id=${PLAYER_ID}`;
    
    try {
        ws = new WebSocket(wsUrl);
        
        ws.onopen = () => {
            console.log('WebSocket connected');
            wsReconnectAttempts = 0;
        };
        
        ws.onmessage = (event) => {
            try {
                // Handle multiple messages (separated by newlines)
                const messages = event.data.split('\n').filter(m => m.trim());
                
                messages.forEach(msgText => {
                    const message = JSON.parse(msgText);
                    
                    if (message.type === 'state' && message.game_state) {
                        lastWebSocketState = message.game_state;
                        gameState = message.game_state;
                        PLAYER_ID = gameState.player_id;
                        // Update base time when receiving new state from server
                        updateBaseTimeFromState(gameState);
                        updateUI();
                    } else if (message.type === 'action_result') {
                        // Always update state from action result if available
                        if (message.result && message.result.game_state) {
                            lastWebSocketState = message.result.game_state;
                            gameState = message.result.game_state;
                            // Update base time when receiving new state from server
                            updateBaseTimeFromState(gameState);
                            updateUI();
                        } else if (message.result) {
                            // If no game_state in result, refresh from server
                            loadGameState();
                        }
                        if (message.result && message.result.message) {
                            showMessage(message.result.message, message.result.success ? 'success' : 'error');
                        }
                    } else if (message.type === 'chat_response') {
                        // Handle chat response
                        if (message.success && message.result) {
                            const result = message.result;
                            // Check if something was created
                            if (result.created && result.offer) {
                                addChatMessage('agent', result.message, 'Guide Agent', result.questions);
                                showMessage('Offer created and shared with your network!', 'success');
                                // Refresh game state to show new offer
                                loadGameState();
                            } else {
                                addChatMessage('agent', result.message, 'Guide Agent', result.questions);
                            }
                        } else {
                            addChatMessage('agent', message.message || 'Error processing chat', 'Guide Agent');
                        }
                    } else if (message.type === 'error') {
                        console.error('WebSocket error:', message.message);
                        showMessage(message.message, 'error');
                    }
                });
            } catch (error) {
                console.error('Error parsing WebSocket message:', error);
            }
        };
        
        ws.onerror = (error) => {
            console.error('WebSocket error:', error);
        };
        
        ws.onclose = () => {
            console.log('WebSocket disconnected');
            ws = null;
            
            // Attempt to reconnect
            if (wsReconnectAttempts < MAX_RECONNECT_ATTEMPTS) {
                wsReconnectAttempts++;
                setTimeout(() => {
                    console.log(`Reconnecting WebSocket (attempt ${wsReconnectAttempts})...`);
                    connectWebSocket();
                }, 2000 * wsReconnectAttempts); // Exponential backoff
            } else {
                console.warn('Max reconnection attempts reached, falling back to HTTP');
                useWebSocket = false;
                startGameLoop(); // Fallback to HTTP polling
            }
        };
    } catch (error) {
        console.error('Failed to create WebSocket:', error);
        useWebSocket = false;
        startGameLoop(); // Fallback to HTTP polling
    }
}

// Calculate current game time based on elapsed real time
function calculateCurrentGameTime() {
    if (!lastServerTime || !lastServerTimeReceived) {
        // No base time yet, return null
        return null;
    }
    
    // Calculate elapsed real time in seconds
    const now = Date.now();
    const elapsedRealSeconds = (now - lastServerTimeReceived) / 1000;
    
    // Determine time progression rate based on current state
    let timeRate = 1.0; // Default: 1 minute game time per 1 second real time
    
    if (gameState) {
        const currentTime = calculatedGameTime || lastServerTime;
        const hour = currentTime.getHours();
        const isNightTime = hour >= 0 && hour < 7;
        const isWorking = gameState.is_working || false;
        
        if (isNightTime) {
            timeRate = 20.0; // 20 minutes game time per 1 second real time
        } else if (isWorking) {
            timeRate = 10.0; // 10 minutes game time per 1 second real time
        }
    }
    
    // Calculate game time elapsed (in milliseconds)
    const gameTimeElapsedMs = elapsedRealSeconds * timeRate * 60 * 1000;
    
    // Return calculated time
    return new Date(lastServerTime.getTime() + gameTimeElapsedMs);
}

// Update the base time from server state
function updateBaseTimeFromState(state) {
    if (state && state.current_date) {
        lastServerTime = new Date(state.current_date);
        lastServerTimeReceived = Date.now();
        calculatedGameTime = lastServerTime; // Initialize calculated time
    }
}

// Compare two game states to see if they differ significantly
function statesDiffer(state1, state2) {
    if (!state1 || !state2) return true;
    
    // Compare key fields that affect UI
    return (
        state1.money !== state2.money ||
        state1.health !== state2.health ||
        state1.energy !== state2.energy ||
        state1.reputation !== state2.reputation ||
        state1.current_date !== state2.current_date ||
        state1.is_working !== state2.is_working ||
        state1.is_in_hospital !== state2.is_in_hospital ||
        state1.game_over !== state2.game_over ||
        JSON.stringify(state1.job) !== JSON.stringify(state2.job) ||
        JSON.stringify(state1.apartment) !== JSON.stringify(state2.apartment) ||
        state1.job_offers?.length !== state2.job_offers?.length ||
        state1.apartment_offers?.length !== state2.apartment_offers?.length ||
        state1.active_offers?.length !== state2.active_offers?.length ||
        state1.stock_offers?.length !== state2.stock_offers?.length ||
        state1.history?.length !== state2.history?.length ||
        JSON.stringify(state1.active_offers?.map(o => ({id: o.id, messages: o.messages}))) !== 
                       JSON.stringify(state2.active_offers?.map(o => ({id: o.id, messages: o.messages})))
    );
}

// Periodic UI update and state sync check
function startUIUpdateLoop() {
    // Clear existing interval
    if (uiUpdateInterval) clearInterval(uiUpdateInterval);
    
    // Update UI frequently (every 500ms) for smooth time progression
    uiUpdateInterval = setInterval(() => {
        if (!gameState || !PLAYER_ID) return;
        
        // Calculate current game time locally
        calculatedGameTime = calculateCurrentGameTime();
        
        // Always update UI (for smooth time display, animations, timers, etc.)
        updateUI();
        
        // If using WebSocket, verify state matches what we received
        if (useWebSocket && ws && ws.readyState === WebSocket.OPEN && lastWebSocketState) {
            if (statesDiffer(gameState, lastWebSocketState)) {
                // State differs, update from WebSocket state
                console.log('State mismatch detected, syncing from WebSocket state');
                gameState = lastWebSocketState;
                updateBaseTimeFromState(gameState); // Update base time when syncing
                updateUI();
            }
        }
    }, 500); // Update every 500ms for smooth time progression
}

function startGameLoop() {
    // Clear any existing intervals
    if (gameLoopInterval) clearInterval(gameLoopInterval);
    if (gameStateRefreshInterval) clearInterval(gameStateRefreshInterval);
    if (saveStateInterval) clearInterval(saveStateInterval);
    if (uiUpdateInterval) clearInterval(uiUpdateInterval);
    
    // Start UI update loop (always runs)
    startUIUpdateLoop();
    
    // If using WebSocket, connect and set up time advancement
    if (useWebSocket && PLAYER_ID) {
        connectWebSocket();
        
        // Time advancement via WebSocket (every 5 seconds)
        gameLoopInterval = setInterval(() => {
            if (gameState && PLAYER_ID && ws && ws.readyState === WebSocket.OPEN) {
                let isNightTime = false;
                if (gameState.current_date) {
                    const currentDate = new Date(gameState.current_date);
                    const hour = currentDate.getHours();
                    isNightTime = hour >= 0 && hour < 7;
                }
                
                const isWorking = gameState.is_working || false;
                let hoursToAdvance;
                if (isNightTime) {
                    hoursToAdvance = (20.0/60.0) * 5; // 20 minutes per second * 5 seconds
                } else if (isWorking) {
                    hoursToAdvance = (10.0/60.0) * 5; // 10 minutes per second * 5 seconds
                } else {
                    hoursToAdvance = (1.0/60.0) * 5; // 1 minute per second * 5 seconds
                }
                
                try {
                    ws.send(JSON.stringify({
                        action: 'advance_time',
                        data: { hours: hoursToAdvance }
                    }));
                } catch (error) {
                    console.error('Error sending advance_time via WebSocket:', error);
                }
            }
        }, 5000);
        
        // Save interval for WebSocket
        saveStateInterval = setInterval(() => {
            if (gameState && PLAYER_ID) {
                const now = Date.now();
                if (now - lastSaveTime >= 10000) {
                    saveGameToStorage().then(() => {
                        lastSaveTime = now;
                    }).catch(err => {
                        console.warn('Failed to save state:', err);
                    });
                }
            }
        }, 10000);
        return;
    }
    
    // HTTP fallback: Auto-advance time
    gameLoopInterval = setInterval(async () => {
        if (gameState && PLAYER_ID) {
            try {
                let isNightTime = false;
                if (gameState.current_date) {
                    const currentDate = new Date(gameState.current_date);
                    const hour = currentDate.getHours();
                    isNightTime = hour >= 0 && hour < 7;
                }
                
                const isWorking = gameState.is_working || false;
                let hoursToAdvance;
                if (isNightTime) {
                    hoursToAdvance = (20.0/60.0) * 5;
                } else if (isWorking) {
                    hoursToAdvance = (10.0/60.0) * 5;
                } else {
                    hoursToAdvance = (1.0/60.0) * 5;
                }
                
                const response = await fetch(`${API_BASE}/action?player_id=${PLAYER_ID}`, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ 
                        action: 'advance_time', 
                        data: { hours: hoursToAdvance }
                    })
                });
                
                const result = await response.json();
                if (result.success && result.game_state) {
                    gameState = result.game_state;
                    updateUI();
                }
            } catch (error) {
                console.error('Error advancing time:', error);
            }
        }
    }, 5000);
    
    // Refresh game state every 30 seconds
    gameStateRefreshInterval = setInterval(() => {
        if (PLAYER_ID) {
            loadGameState();
        }
    }, 30000);
    
    // Save state every 10 seconds
    saveStateInterval = setInterval(() => {
        if (gameState && PLAYER_ID) {
            const now = Date.now();
            if (now - lastSaveTime >= 10000) {
                saveGameToStorage().then(() => {
                    lastSaveTime = now;
                }).catch(err => {
                    console.warn('Failed to save state:', err);
                });
            }
        }
    }, 10000);
}

// Load game state (with caching support)
async function loadGameState() {
    if (!PLAYER_ID) return;
    
    try {
        // Load from server with cache headers
        const response = await fetch(`${API_BASE}/state?player_id=${PLAYER_ID}`, {
            headers: {
                'Accept-Encoding': 'gzip',
                'Cache-Control': 'max-age=5'
            }
        });
        
        if (response.status === 304) {
            // Not modified - use cached version
            return;
        }
        
        if (response.ok) {
            const serverState = await response.json();
            if (serverState) {
                gameState = serverState;
                PLAYER_ID = gameState.player_id;
                // Update base time when loading from server
                updateBaseTimeFromState(serverState);
                // Sync WebSocket state reference if using WebSocket
                if (useWebSocket) {
                    lastWebSocketState = serverState;
                }
                updateUI();
            }
            // Don't save here - let the save interval handle it to avoid redundant saves
        } else {
            console.error('Failed to load game state from server');
        }
    } catch (error) {
        console.error('Error loading game state:', error);
    }
}

// Load game from encrypted localStorage (server-side decryption for security)
async function loadGameFromStorage() {
    const savedEncryptedData = localStorage.getItem(STORAGE_KEY);
    const savedUnencryptedData = localStorage.getItem(STORAGE_KEY + '_unencrypted');
    
    if (!savedEncryptedData && !savedUnencryptedData) {
        throw new Error('No saved game found');
    }
    
    try {
        let decrypted;
        
        if (savedEncryptedData) {
            // Decrypt server-side (secure)
            const response = await fetch(`${API_BASE}/decrypt`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ encrypted: savedEncryptedData })
            });
            
            if (response.ok) {
                const result = await response.json();
                decrypted = result.decrypted;
            } else {
                throw new Error('Decryption failed');
            }
        } else {
            // Fallback to unencrypted
            decrypted = savedUnencryptedData;
        }
        
        // Parse the decrypted JSON
        gameState = JSON.parse(decrypted);
        PLAYER_ID = gameState.player_id;
        
        // Update base time from loaded state
        updateBaseTimeFromState(gameState);
        
        // Verify the game still exists on the server
        try {
            const response = await fetch(`${API_BASE}/state?player_id=${PLAYER_ID}`);
            if (response.ok) {
                // Update with server state (in case of changes)
                const serverState = await response.json();
                gameState = serverState;
                // Update base time from server state
                updateBaseTimeFromState(serverState);
            }
        } catch (error) {
            console.warn('Could not verify game state with server, using local state:', error);
        }
        
        PLAYER_ID = gameState.player_id;
        updateUI();
        return true;
    } catch (error) {
        console.error('Error loading from storage:', error);
        throw error;
    }
}

// Save game state to encrypted localStorage (server-side encryption for security)
async function saveGameToStorage() {
    if (!gameState) {
        console.warn('No game state to save');
        return;
    }
    
    try {
        // Convert game state to JSON string
        const gameStateJson = JSON.stringify(gameState);
        
        // Encrypt server-side (secure - key not exposed to client)
        const controller = new AbortController();
        const timeoutId = setTimeout(() => controller.abort(), 3000); // 3 second timeout
        
        const response = await fetch(`${API_BASE}/encrypt`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ data: gameStateJson }),
            signal: controller.signal
        });
        
        clearTimeout(timeoutId);
        
        if (response.ok) {
            const result = await response.json();
            localStorage.setItem(STORAGE_KEY, result.encrypted);
            lastSaveTime = Date.now();
        } else {
            console.warn('Encryption failed, saving unencrypted (less secure)');
            // Fallback: save unencrypted if encryption fails (not ideal but allows game to continue)
            localStorage.setItem(STORAGE_KEY + '_unencrypted', gameStateJson);
        }
    } catch (error) {
        if (error.name === 'AbortError') {
            console.warn('Encryption timeout, saving unencrypted');
            localStorage.setItem(STORAGE_KEY + '_unencrypted', JSON.stringify(gameState));
        } else {
            console.error('Error saving game to storage:', error);
        }
        // Don't throw - allow game to continue even if save fails
    }
}

// Show story modal
function showStoryModal() {
    const modal = document.getElementById('story-modal');
    if (modal) {
        modal.style.display = 'block';
    }
}

// Hide story modal
function hideStoryModal() {
    const modal = document.getElementById('story-modal');
    if (modal) {
        modal.style.display = 'none';
    }
}

// Setup story modal event listeners
function setupStoryModalListeners() {
    const continueBtn = document.getElementById('btn-continue-story');
    
    if (continueBtn) {
        continueBtn.addEventListener('click', () => {
            hideStoryModal();
            showInviteModal();
        });
    }
}

// Show invite modal
function showInviteModal() {
    const modal = document.getElementById('invite-modal');
    if (modal) {
        modal.style.display = 'block';
        
        // Check if invite code is in URL parameters
        const urlParams = new URLSearchParams(window.location.search);
        const inviteCodeFromUrl = urlParams.get('invite');
        if (inviteCodeFromUrl) {
            const inviteCodeInput = document.getElementById('invite-code-input');
            if (inviteCodeInput) {
                inviteCodeInput.value = inviteCodeFromUrl.toUpperCase();
            }
        }
    }
}

// Hide invite modal
function hideInviteModal() {
    const modal = document.getElementById('invite-modal');
    if (modal) {
        modal.style.display = 'none';
    }
}

// Setup invite modal event listeners
function setupInviteModalListeners() {
    const startGameBtn = document.getElementById('btn-start-game');
    const inviteCodeInput = document.getElementById('invite-code-input');
    
    if (startGameBtn) {
        startGameBtn.addEventListener('click', async () => {
            const inviteCode = inviteCodeInput ? inviteCodeInput.value.trim().toUpperCase() : '';
            
            // Disable button to prevent double-clicks
            if (startGameBtn) {
                startGameBtn.disabled = true;
                startGameBtn.textContent = 'Starting...';
            }
            
            // Generate player ID
            PLAYER_ID = 'player_' + Date.now() + '_' + Math.random().toString(36).substr(2, 9);
            
            try {
                let response;
                if (inviteCode) {
                    // Create game with invite code
                    console.log('Creating game with invite code:', inviteCode);
                    response = await fetch(`${API_BASE}/create-with-invite`, {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({
                            player_id: PLAYER_ID,
                            invite_code: inviteCode
                        })
                    });
                } else {
                    // Create first player game
                    response = await fetch(`${API_BASE}/state?player_id=${PLAYER_ID}`);
                }
                
                if (response.ok) {
                    gameState = await response.json();
                    PLAYER_ID = gameState.player_id;
                    
                    console.log('Game created successfully, saving to storage...');
                    
                    // Save encrypted state (don't wait if it fails)
                    try {
                        await saveGameToStorage();
                        console.log('Game state saved to storage');
                    } catch (saveError) {
                        console.warn('Failed to save game state, continuing anyway:', saveError);
                    }
                    
                    // Clear invite code from URL if present
                    const url = new URL(window.location.href);
                    url.searchParams.delete('invite');
                    window.history.replaceState({}, '', url);
                    
                    // Hide modal and initialize game
                    hideInviteModal();
                    
                    console.log('Loading market data...');
                    loadMarketData();
                    
                    console.log('Setting up event listeners...');
                    setupEventListeners();
                    setupTabs();
                    
                    console.log('Starting game loop...');
                    startGameLoop();
                    
                    console.log('Updating UI...');
                    updateUI();
                    
                    showMessage('Game started successfully!', 'success');
                    console.log('Game initialization complete!');
                } else {
                    let errorMsg = 'Invalid invite code';
                    try {
                        const error = await response.json();
                        errorMsg = error.error || error.message || errorMsg;
                    } catch (e) {
                        // If response is not JSON, try to get text
                        const text = await response.text();
                        errorMsg = text || errorMsg;
                    }
                    console.error('Failed to start game:', errorMsg);
                    showMessage('Failed to start game: ' + errorMsg, 'error');
                }
            } catch (error) {
                console.error('Error starting game:', error);
                showMessage('Error starting game: ' + error.message, 'error');
            } finally {
                // Re-enable button
                if (startGameBtn) {
                    startGameBtn.disabled = false;
                    startGameBtn.textContent = 'Start Game';
                }
            }
        });
    }
    
    // Allow Enter key to submit
    if (inviteCodeInput) {
        inviteCodeInput.addEventListener('keypress', (e) => {
            if (e.key === 'Enter' && startGameBtn) {
                startGameBtn.click();
            }
        });
    }
}

// Load market data
async function loadMarketData() {
    try {
        const [itemsRes, stocksRes, cryptoRes] = await Promise.all([
            fetch(`${API_BASE}/market/items`),
            fetch(`${API_BASE}/market/stocks`),
            fetch(`${API_BASE}/market/crypto`)
        ]);
        
        const marketItems = await itemsRes.json();
        const stockSymbols = await stocksRes.json();
        const cryptoSymbols = await cryptoRes.json();
        
        // Populate market items
        const marketBuySelect = document.getElementById('market-item-buy');
        marketItems.forEach(item => {
            const option = document.createElement('option');
            option.value = item.id;
            option.textContent = `${item.name} - €${item.market_price.toFixed(2)}`;
            option.dataset.price = item.market_price;
            marketBuySelect.appendChild(option);
        });
        
        // Populate stock symbols
        const stockSelect = document.getElementById('stock-symbol');
        stockSymbols.forEach(symbol => {
            const option = document.createElement('option');
            option.value = symbol;
            option.textContent = symbol;
            stockSelect.appendChild(option);
        });
        
        // Populate crypto symbols
        const cryptoSelect = document.getElementById('crypto-symbol');
        cryptoSymbols.forEach(symbol => {
            const option = document.createElement('option');
            option.value = symbol;
            option.textContent = symbol;
            cryptoSelect.appendChild(option);
        });
    } catch (error) {
        console.error('Error loading market data:', error);
    }
}

// Setup event listeners
function setupEventListeners() {
    // Work buttons
    document.getElementById('btn-start-work').addEventListener('click', () => performAction('start_work', {}));
    document.getElementById('btn-stop-work').addEventListener('click', () => performAction('stop_work', {}));
    document.getElementById('btn-quit-job').addEventListener('click', () => performAction('quit_job', {}));
    
    // Apartment button
    document.getElementById('btn-quit-apartment').addEventListener('click', () => performAction('quit_apartment', {}));
    
    // Stock buttons
    document.getElementById('btn-buy-stock').addEventListener('click', () => {
        const symbol = document.getElementById('stock-symbol').value;
        const shares = parseInt(document.getElementById('stock-shares').value);
        if (symbol && shares > 0) {
            performAction('buy_stock', { symbol, shares });
        }
    });
    
    document.getElementById('btn-sell-stock').addEventListener('click', () => {
        const symbol = document.getElementById('stock-symbol').value;
        const shares = parseInt(document.getElementById('stock-shares').value);
        if (symbol && shares > 0) {
            performAction('sell_stock', { symbol, shares });
        }
    });
    
    // Crypto buttons
    document.getElementById('btn-buy-crypto').addEventListener('click', () => {
        const symbol = document.getElementById('crypto-symbol').value;
        const amount = parseFloat(document.getElementById('crypto-amount').value);
        if (symbol && amount > 0) {
            performAction('buy_crypto', { symbol, amount });
        }
    });
    
    document.getElementById('btn-sell-crypto').addEventListener('click', () => {
        const symbol = document.getElementById('crypto-symbol').value;
        const amount = parseFloat(document.getElementById('crypto-amount').value);
        if (symbol && amount > 0) {
            performAction('sell_crypto', { symbol, amount });
        }
    });
    
    // Market buttons
    document.getElementById('btn-buy-item').addEventListener('click', () => {
        const select = document.getElementById('market-item-buy');
        const option = select.options[select.selectedIndex];
        if (option.value) {
            performAction('buy_item', { item_id: option.value, price: parseFloat(option.dataset.price) });
        }
    });
    
    document.getElementById('btn-sell-item').addEventListener('click', () => {
        const select = document.getElementById('market-item-sell');
        const option = select.options[select.selectedIndex];
        if (option.value) {
            performAction('sell_item', { item_id: option.value });
        }
    });
    
    // Offer buttons
    const btnGenerateTrickery = document.getElementById('btn-generate-trickery');
    if (btnGenerateTrickery) {
        btnGenerateTrickery.addEventListener('click', () => generateOffer('trickery'));
    }
    
    const btnGenerateGood = document.getElementById('btn-generate-good');
    if (btnGenerateGood) {
        btnGenerateGood.addEventListener('click', () => generateOffer('good'));
    }
    
    // Chat
    const btnSendChat = document.getElementById('btn-send-chat');
    if (btnSendChat) {
        btnSendChat.addEventListener('click', sendChat);
    }
    
    const chatInput = document.getElementById('chat-input');
    if (chatInput) {
        chatInput.addEventListener('keypress', (e) => {
            if (e.key === 'Enter') {
                sendChat();
            }
        });
    }
    
    // Copy invite code button
    const copyInviteBtn = document.getElementById('btn-copy-invite');
    if (copyInviteBtn) {
        copyInviteBtn.addEventListener('click', () => {
            const inviteCodeDisplay = document.getElementById('invite-code-display');
            if (inviteCodeDisplay && inviteCodeDisplay.textContent) {
                const inviteCode = inviteCodeDisplay.textContent.trim();
                // Create direct invite link URL
                const currentUrl = new URL(window.location.href);
                currentUrl.searchParams.set('invite', inviteCode);
                const inviteLink = currentUrl.toString();
                
                navigator.clipboard.writeText(inviteLink).then(() => {
                    showMessage('Invite link copied to clipboard!', 'success');
                    // Temporarily change button text
                    const originalText = copyInviteBtn.textContent;
                    copyInviteBtn.textContent = '✓ Copied!';
                    setTimeout(() => {
                        copyInviteBtn.textContent = originalText;
                    }, 2000);
                }).catch(err => {
                    // Fallback for older browsers
                    const textArea = document.createElement('textarea');
                    textArea.value = inviteLink;
                    textArea.style.position = 'fixed';
                    textArea.style.opacity = '0';
                    document.body.appendChild(textArea);
                    textArea.select();
                    try {
                        document.execCommand('copy');
                        showMessage('Invite link copied to clipboard!', 'success');
                        const originalText = copyInviteBtn.textContent;
                        copyInviteBtn.textContent = '✓ Copied!';
                        setTimeout(() => {
                            copyInviteBtn.textContent = originalText;
                        }, 2000);
                    } catch (err) {
                        showMessage('Failed to copy invite link', 'error');
                    }
                    document.body.removeChild(textArea);
                });
            }
        });
    }
    
    // Share invite code button
    const shareInviteBtn = document.getElementById('btn-share-invite');
    if (shareInviteBtn) {
        shareInviteBtn.addEventListener('click', async () => {
            const inviteCodeDisplay = document.getElementById('invite-code-display');
            if (inviteCodeDisplay && inviteCodeDisplay.textContent) {
                const inviteCode = inviteCodeDisplay.textContent.trim();
                const currentUrl = window.location.href;
                const shareText = `Join me in the Economic Game! Use invite code: ${inviteCode}\n\nPlay at: ${currentUrl}`;
                
                // Try Web Share API first
                if (navigator.share) {
                    try {
                        await navigator.share({
                            title: 'Economic Game - Invite',
                            text: shareText,
                            url: currentUrl,
                        });
                        showMessage('Invite link shared!', 'success');
                    } catch (err) {
                        if (err.name !== 'AbortError') {
                            // Fallback to clipboard if share is cancelled or fails
                            copyInviteLink(inviteCode, currentUrl);
                        }
                    }
                } else {
                    // Fallback: copy to clipboard with message
                    copyInviteLink(inviteCode, currentUrl);
                    showMessage('Invite link copied! Share it with your friends.', 'success');
                }
            }
        });
    }
    
    // Helper function to copy invite link
    function copyInviteLink(inviteCode, url) {
        const shareText = `Join me in the Economic Game! Use invite code: ${inviteCode}\n\nPlay at: ${url}`;
        navigator.clipboard.writeText(shareText).catch(() => {
            // Fallback for older browsers
            const textArea = document.createElement('textarea');
            textArea.value = shareText;
            textArea.style.position = 'fixed';
            textArea.style.opacity = '0';
            document.body.appendChild(textArea);
            textArea.select();
            document.execCommand('copy');
            document.body.removeChild(textArea);
        });
    }
}

// Setup tabs
function setupTabs() {
    // Main tabs
    const mainTabs = document.querySelectorAll('.main-tab');
    if (mainTabs.length === 0) {
        console.warn('No main tabs found');
        return;
    }
    
    mainTabs.forEach(tab => {
        tab.addEventListener('click', () => {
            const tabName = tab.dataset.tab;
            if (!tabName) {
                console.warn('Tab has no data-tab attribute');
                return;
            }
            
            // Remove active class from all tabs and panels
            mainTabs.forEach(t => t.classList.remove('active'));
            document.querySelectorAll('.main-tab-content').forEach(p => p.classList.remove('active'));
            
            // Add active class to clicked tab and corresponding panel
            tab.classList.add('active');
            const panelId = `${tabName}-panel`;
            const panel = document.getElementById(panelId);
            if (panel) {
                panel.classList.add('active');
            } else {
                console.warn(`Panel not found: ${panelId}`);
            }
        });
    });
}

// Perform action (via WebSocket if available, otherwise HTTP)
async function performAction(action, data) {
    if (!PLAYER_ID) {
        showMessage('Please start a game first', 'error');
        return null;
    }
    
    // Use WebSocket if available
    if (ws && ws.readyState === WebSocket.OPEN) {
        try {
            ws.send(JSON.stringify({ action, data }));
            // State will be updated via WebSocket message
            return { success: true };
        } catch (error) {
            console.error('Error sending WebSocket message:', error);
            // Fall through to HTTP fallback
        }
    }
    
    // HTTP fallback
    try {
        const response = await fetch(`${API_BASE}/action?player_id=${PLAYER_ID}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ action, data })
        });
        
        const result = await response.json();
        if (result.success && result.game_state) {
            gameState = result.game_state;
            updateUI();
            const now = Date.now();
            if (now - lastSaveTime >= 5000) {
                saveGameToStorage().then(() => {
                    lastSaveTime = now;
                }).catch(err => {
                    console.warn('Failed to save state:', err);
                });
            }
            if (result.message) {
                showMessage(result.message, 'success');
            }
            return result;
        } else {
            if (result.message) {
                showMessage(result.message, 'error');
            }
            return result;
        }
    } catch (error) {
        showMessage('Error: ' + error.message, 'error');
        return null;
    }
}

// Generate offer
async function generateOffer(type) {
    try {
        const response = await fetch(`${API_BASE}/offer?player_id=${PLAYER_ID}&type=${type}`);
        const result = await response.json();
        if (result.success) {
            gameState = result.game_state;
            updateUI();
            showMessage(`Generated ${type} offer!`, 'success');
        }
    } catch (error) {
        showMessage('Error generating offer: ' + error.message, 'error');
    }
}

// Send chat
async function sendChat() {
    const input = document.getElementById('chat-input');
    const message = input.value.trim();
    if (!message) return;
    
    addChatMessage('user', message, 'You');
    input.value = '';
    
    try {
        // Build comprehensive context
        let context = `Date: ${gameState?.current_date ? new Date(gameState.current_date).toLocaleDateString() : 'N/A'}, Money: €${gameState?.money?.toFixed(2) || 0}`;
        
        // Add health and energy
        const health = gameState?.health !== undefined ? gameState.health : 100;
        const energy = gameState?.energy !== undefined ? gameState.energy : 100;
        context += `, Health: ${health}/100, Energy: ${energy}/100`;
        
        if (gameState?.job) {
            context += `, Current Job: ${gameState.job.title}`;
            context += `, Work Type: ${gameState.job.work_type || 'hourly'}`;
            if (gameState.job.work_type === 'fixed_time') {
                context += ` (${gameState.job.work_start || '09:00'} - ${gameState.job.work_end || '17:00'})`;
            }
            context += `, Currently Working: ${gameState.is_working ? 'Yes' : 'No'}`;
        }
        
        if (gameState?.apartment) {
            context += `, Apartment: ${gameState.apartment.title} (Rent: €${gameState.apartment.rent.toFixed(2)}/month)`;
        } else {
            context += `, Apartment: None ⚠️ (Will lose 2 health each night!)`;
        }
        
        if (gameState?.reputation !== undefined) {
            context += `, Reputation: ${gameState.reputation}`;
        }
        
        // Add recent work-related context
        if (gameState?.history && gameState.history.length > 0) {
            const workEvents = ['job_accepted', 'job_quit', 'work_start', 'work_end', 'work_stop', 'salary', 'hint_purchased'];
            const recentWorkEvents = gameState.history
                .filter(e => workEvents.includes(e.type))
                .slice(-3)
                .map(e => `${e.type}: ${e.message}`)
                .join('; ');
            if (recentWorkEvents) {
                context += `, Recent: ${recentWorkEvents}`;
            }
        }
        
        // Try WebSocket first if available
        if (useWebSocket && ws && ws.readyState === WebSocket.OPEN) {
            // Send chat via WebSocket
            try {
                ws.send(JSON.stringify({
                    action: 'chat',
                    data: {
                        message: message,
                        context: context
                    }
                }));
                // Response will come via WebSocket message handler
                return;
            } catch (error) {
                console.error('Error sending chat via WebSocket, falling back to HTTP:', error);
            }
        }
        
        // Fallback to HTTP
        const response = await fetch(`${API_BASE}/chat?player_id=${PLAYER_ID}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                agent: 'guide',
                message: message,
                context: context
            })
        });
        
        const result = await response.json();
        
        // Check if something was created
        if (result.created && result.offer) {
            addChatMessage('agent', result.message, 'Guide Agent', result.questions);
            showMessage('Offer created and shared with your network!', 'success');
            // Refresh game state to show new offer
            await loadGameState();
        } else {
            addChatMessage('agent', result.message, 'Guide Agent', result.questions);
        }
    } catch (error) {
        showMessage('Error sending chat: ' + error.message, 'error');
    }
}

// Add chat message
function addChatMessage(type, message, sender, questions = []) {
    const messagesDiv = document.getElementById('chat-messages');
    const messageDiv = document.createElement('div');
    messageDiv.className = `chat-message ${type}`;
    
    let html = `<h4>${sender}</h4><p>${message}</p>`;
    if (questions && questions.length > 0) {
        html += '<ul class="questions">';
        questions.forEach(q => {
            html += `<li>${q}</li>`;
        });
        html += '</ul>';
    }
    messageDiv.innerHTML = html;
    messagesDiv.appendChild(messageDiv);
    messagesDiv.scrollTop = messagesDiv.scrollHeight;
}

// Update UI
function updateUI() {
    if (!gameState) return;
    
    // Update stats
    const money = gameState.money || 0;
    document.getElementById('money').textContent = `€${money.toFixed(2)}`;
    
    // Update health and energy
    const health = gameState.health !== undefined ? gameState.health : 100;
    const energy = gameState.energy !== undefined ? gameState.energy : 100;
    document.getElementById('health').textContent = health;
    document.getElementById('energy').textContent = energy;
    
    // Update hospital status
    const hospitalStatus = document.getElementById('hospital-status');
    const hospitalInfo = document.getElementById('hospital-info');
    if (gameState.is_in_hospital) {
        if (hospitalStatus) hospitalStatus.style.display = 'inline';
        if (hospitalInfo) hospitalInfo.style.display = 'block';
    } else {
        if (hospitalStatus) hospitalStatus.style.display = 'none';
        if (hospitalInfo) hospitalInfo.style.display = 'none';
    }
    
    // Update game over status
    const gameOverInfo = document.getElementById('game-over-info');
    const gameOverReason = document.getElementById('game-over-reason');
    if (gameState.game_over) {
        if (gameOverInfo) gameOverInfo.style.display = 'block';
        if (gameOverReason && gameState.game_over_reason) {
            gameOverReason.textContent = gameState.game_over_reason;
        }
    } else {
        if (gameOverInfo) gameOverInfo.style.display = 'none';
    }
    
    // Update reputation
    const reputation = gameState.reputation || 0;
    document.getElementById('reputation').textContent = reputation;
    const repElement = document.getElementById('reputation');
    if (reputation > 0) {
        repElement.className = 'reputation positive';
    } else if (reputation < 0) {
        repElement.className = 'reputation negative';
    } else {
        repElement.className = 'reputation';
    }
    
    // Update invite code section
    const inviteCodeSection = document.getElementById('invite-code-section');
    const inviteCodeDisplay = document.getElementById('invite-code-display');
    if (gameState.invite_code) {
        if (inviteCodeSection) inviteCodeSection.style.display = 'block';
        if (inviteCodeDisplay) inviteCodeDisplay.textContent = gameState.invite_code;
    } else {
        if (inviteCodeSection) inviteCodeSection.style.display = 'none';
    }
    
    // Update date and time (use calculated time if available, otherwise use server time)
    const displayTime = calculatedGameTime || (gameState.current_date ? new Date(gameState.current_date) : null);
    if (displayTime) {
        document.getElementById('current-date').textContent = displayTime.toLocaleDateString('en-US', { year: 'numeric', month: '2-digit', day: '2-digit' });
        document.getElementById('current-time').textContent = displayTime.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false });
    }
    
    document.getElementById('job').textContent = gameState.job ? gameState.job.title : 'None';
    document.getElementById('apartment').textContent = gameState.apartment ? gameState.apartment.title : 'None';
    
    // Update apartment quit button
    const quitApartmentBtn = document.getElementById('btn-quit-apartment');
    quitApartmentBtn.disabled = !gameState.apartment;
    
    // Show health warning if no apartment
    const healthWarning = document.getElementById('health-warning');
    if (!gameState.apartment) {
        healthWarning.style.display = 'block';
    } else {
        healthWarning.style.display = 'none';
    }
    
    // Update work schedule information
    updateWorkSchedule();
    
    // Update work status and timer
    updateWorkStatus();
    
    // Update work buttons based on work type
    const startWorkBtn = document.getElementById('btn-start-work');
    const stopWorkBtn = document.getElementById('btn-stop-work');
    const quitJobBtn = document.getElementById('btn-quit-job');
    
    if (gameState.job) {
        const workType = gameState.job.work_type || 'hourly';
        
        if (workType === 'fixed_time') {
            // Fixed-time jobs: hide start/stop buttons
            startWorkBtn.style.display = 'none';
            stopWorkBtn.style.display = 'none';
            quitJobBtn.disabled = false;
        } else {
            // Hourly jobs: show start/stop buttons
            startWorkBtn.style.display = 'inline-block';
            stopWorkBtn.style.display = gameState.is_working ? 'inline-block' : 'none';
            startWorkBtn.disabled = gameState.is_working;
            stopWorkBtn.disabled = !gameState.is_working;
            quitJobBtn.disabled = false;
        }
    } else {
        startWorkBtn.style.display = 'inline-block';
        startWorkBtn.disabled = true;
        stopWorkBtn.style.display = 'none';
        quitJobBtn.disabled = true;
    }
    
    // Update investments
    updateInvestments();
    
    // Update inventory
    updateInventory();
    
    // Update offers
    updateOffers();
    
    // Update job offers
    updateJobOffers();
    
    // Update apartment offers
    updateApartmentOffers();
    
    // Update all offers tab
    updateAllOffers();
    
    // Update agreements
    updateAgreements();
    
    // Update dashboard
    updateDashboard();
    
    // Update history
    updateHistory();
    
    // Update message modal if it's open
    if (currentMessageOfferId && gameState && gameState.active_offers) {
        const offer = gameState.active_offers.find(o => o.id === currentMessageOfferId && o.type === 'other');
        if (offer) {
            updateOfferMessageModalMessages(offer);
        }
    }
}

// Update work schedule
function updateWorkSchedule() {
    const workScheduleDiv = document.getElementById('work-schedule');
    const workScheduleSpan = document.getElementById('work-schedule-time');
    
    if (gameState.job) {
        const workType = gameState.job.work_type || 'hourly';
        
        if (workType === 'fixed_time') {
            // Fixed-time job: show schedule
            const workStart = gameState.job.work_start || '09:00';
            const workEnd = gameState.job.work_end || '17:00';
            
            // Check if currently in work hours
            let scheduleText = `${workStart} - ${workEnd}`;
            if (gameState.current_date) {
                const currentDate = new Date(gameState.current_date);
                const currentTime = currentDate.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false });
                const [currentHour, currentMin] = currentTime.split(':').map(Number);
                const [startHour, startMin] = workStart.split(':').map(Number);
                const [endHour, endMin] = workEnd.split(':').map(Number);
                
                const currentMinutes = currentHour * 60 + currentMin;
                const startMinutes = startHour * 60 + startMin;
                const endMinutes = endHour * 60 + endMin;
                
                if (currentMinutes >= startMinutes && currentMinutes < endMinutes) {
                    scheduleText += ' (Working Now - Unavailable)';
                    workScheduleSpan.className = 'work-schedule active';
                } else if (currentMinutes < startMinutes) {
                    const hoursUntil = Math.floor((startMinutes - currentMinutes) / 60);
                    const minsUntil = (startMinutes - currentMinutes) % 60;
                    scheduleText += ` (Starts in ${hoursUntil}h ${minsUntil}m)`;
                    workScheduleSpan.className = 'work-schedule upcoming';
                } else {
                    scheduleText += ' (Work Ended Today)';
                    workScheduleSpan.className = 'work-schedule ended';
                }
            }
            
            workScheduleSpan.textContent = scheduleText;
            workScheduleDiv.style.display = 'block';
        } else {
            // Hourly job: show flexible hours info
            const hoursPerDay = gameState.job.hours_per_day || 8;
            if (gameState.is_working) {
                workScheduleSpan.textContent = `Flexible Hours (Working Now - Unavailable, ${hoursPerDay}h/day)`;
                workScheduleSpan.className = 'work-schedule active';
            } else {
                workScheduleSpan.textContent = `Flexible Hours (Available, ${hoursPerDay}h/day expected)`;
                workScheduleSpan.className = 'work-schedule available';
            }
            workScheduleDiv.style.display = 'block';
        }
    } else {
        workScheduleDiv.style.display = 'none';
    }
}

// Update work status
function updateWorkStatus() {
    const workStatusDiv = document.getElementById('work-status');
    const workTimerSpan = document.getElementById('work-timer');
    
    if (gameState.is_working && gameState.work_end_time) {
        workStatusDiv.style.display = 'block';
        const endTime = new Date(gameState.work_end_time);
        // Use calculated time for smooth countdown
        const currentTime = calculatedGameTime || (gameState.current_date ? new Date(gameState.current_date) : new Date());
        const remaining = endTime - currentTime;
        
        if (remaining > 0) {
            const hours = Math.floor(remaining / (1000 * 60 * 60));
            const minutes = Math.floor((remaining % (1000 * 60 * 60)) / (1000 * 60));
            workTimerSpan.textContent = `Working... ${hours}h ${minutes}m remaining`;
            workTimerSpan.className = 'work-timer working';
        } else {
            workTimerSpan.textContent = 'Work complete!';
            workTimerSpan.className = 'work-timer complete';
        }
    } else {
        workStatusDiv.style.display = 'none';
    }
}

// Update job offers
function updateJobOffers() {
    const div = document.getElementById('job-offers-list');
    if (!div) {
        console.warn('job-offers-list element not found');
        return;
    }
    
    if (!gameState.job_offers || gameState.job_offers.length === 0) {
        div.innerHTML = '<p class="empty">No job offers available. New offers will appear automatically!</p>';
        return;
    }
    
    // Check if it's night time
    let isNightTime = false;
    if (gameState.current_date) {
        const currentDate = new Date(gameState.current_date);
        const hour = currentDate.getHours();
        isNightTime = hour >= 0 && hour < 7; // 00:00 - 07:00
    }
    
    let html = '';
    gameState.job_offers.forEach(offer => {
        const offerClass = offer.is_trickery ? 'trickery' : 'good';
        const expiresDate = new Date(offer.expires_at);
        const salary = offer.salary || 0;
        const hoursPerDay = offer.hours_per_day || 8;
        const salaryPerHour = salary > 0 && hoursPerDay > 0 ? (salary / (hoursPerDay * 20)).toFixed(2) : 0;
        
        // Don't show if it's trickery or good - hide the class
        const workType = offer.work_type || 'hourly';
        const workTypeDisplay = workType === 'fixed_time' 
            ? `Fixed Schedule: ${offer.work_start || '09:00'} - ${offer.work_end || '17:00'}` 
            : 'Flexible Hours (Start/Stop Work)';
        
        html += `
            <div class="job-offer-item">
                <h4>${offer.title}</h4>
                <p>${offer.description}</p>
                <div class="job-offer-details">
                    <p><strong>Monthly Salary:</strong> €${salary.toFixed(2)}</p>
                    <p><strong>Hours per Day:</strong> ${hoursPerDay}</p>
                    <p><strong>Work Type:</strong> ${workTypeDisplay}</p>
                    ${salaryPerHour > 0 ? `<p><strong>Salary per Hour:</strong> €${salaryPerHour}</p>` : '<p><strong>Commission Only</strong></p>'}
                    ${(offer.upfront_cost || 0) > 0 ? `<p style="color: #ff6b6b; font-weight: bold;"><strong>⚠️ Upfront Cost:</strong> €${offer.upfront_cost.toFixed(2)} (training fees, materials, etc.)</p>` : ''}
                    <p><strong>Expires:</strong> ${expiresDate.toLocaleDateString()}</p>
                </div>
                <div id="hint-${offer.id}" style="display: ${offer.hint_shown ? 'block' : 'none'};">
                    ${offer.reason ? `<p class="offer-reason"><em>${offer.reason}</em></p>` : ''}
                    <p class="offer-type-badge ${offerClass}">${offer.is_trickery ? '⚠️ SCAM JOB' : '✅ LEGITIMATE JOB'}</p>
                </div>
                <div style="margin-top: 10px;">
                    <button class="btn btn-info" onclick="showHint('${offer.id}')" id="hint-btn-${offer.id}" ${offer.hint_shown ? 'disabled' : ''}>${offer.hint_shown ? 'Hint (Used)' : 'Hint (€10)'}</button>
                    ${isNightTime 
                        ? '<button class="btn btn-primary" disabled title="Cannot accept jobs during night hours (00:00 - 07:00)">Accept Job (Night Time)</button>' 
                        : `<button class="btn btn-primary" onclick="acceptJobOffer('${offer.id}')">Accept Job${(offer.upfront_cost || 0) > 0 ? ` (€${offer.upfront_cost.toFixed(2)})` : ''}</button>`}
                </div>
            </div>
        `;
    });
    div.innerHTML = html;
}

// Accept job offer
async function acceptJobOffer(offerId) {
    await performAction('accept_job_offer', { offer_id: offerId });
}

// Show hint for job offer (costs 10 EUR)
async function showHint(offerId) {
    try {
        const response = await fetch(`${API_BASE}/action?player_id=${PLAYER_ID}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ 
                action: 'show_hint', 
                data: { offer_id: offerId }
            })
        });
        
        const result = await response.json();
        if (result.success) {
            gameState = result.game_state;
            // Show the hint
            const hintDiv = document.getElementById(`hint-${offerId}`);
            const hintBtn = document.getElementById(`hint-btn-${offerId}`);
            if (hintDiv) {
                hintDiv.style.display = 'block';
            }
            if (hintBtn) {
                hintBtn.disabled = true;
                hintBtn.textContent = 'Hint (Used)';
            }
            updateUI();
            showMessage(result.message, 'success');
        } else {
            showMessage(result.message, 'error');
        }
    } catch (error) {
        showMessage('Error showing hint: ' + error.message, 'error');
    }
}

// Update investments
function updateInvestments() {
    const div = document.getElementById('investments');
    if (gameState.stocks.length === 0 && gameState.crypto.length === 0) {
        div.innerHTML = '<p class="empty">No investments yet</p>';
        return;
    }
    
    let html = '';
    gameState.stocks.forEach(stock => {
        const buyPrice = stock.buy_price || 0;
        const currentPrice = stock.current_price || buyPrice;
        const shares = stock.shares || 0;
        const profit = (currentPrice - buyPrice) * shares;
        const profitClass = profit >= 0 ? 'positive' : 'negative';
        html += `
            <div class="investment-item">
                <strong>${stock.symbol}</strong>: ${shares} shares
                <br>Bought: €${buyPrice.toFixed(2)} | Current: €${currentPrice.toFixed(2)}
                <span class="${profitClass}">(${profit >= 0 ? '+' : ''}€${profit.toFixed(2)})</span>
            </div>
        `;
    });
    
    gameState.crypto.forEach(crypto => {
        const buyPrice = crypto.buy_price || 0;
        const currentPrice = crypto.current_price || buyPrice;
        const amount = crypto.amount || 0;
        const profit = (currentPrice - buyPrice) * amount;
        const profitClass = profit >= 0 ? 'positive' : 'negative';
        html += `
            <div class="investment-item">
                <strong>${crypto.symbol}</strong>: ${amount.toFixed(4)}
                <br>Bought: €${buyPrice.toFixed(2)} | Current: €${currentPrice.toFixed(2)}
                <span class="${profitClass}">(${profit >= 0 ? '+' : ''}€${profit.toFixed(2)})</span>
            </div>
        `;
    });
    
    div.innerHTML = html;
}

// Update inventory
function updateInventory() {
    const div = document.getElementById('inventory');
    if (gameState.inventory.length === 0) {
        div.innerHTML = '<p class="empty">No items</p>';
        return;
    }
    
    let html = '';
    gameState.inventory.forEach(item => {
        const buyPrice = item.buy_price || 0;
        const marketPrice = item.market_price || buyPrice;
        
        // Show stat effects if any
        const statEffects = [];
        if (item.health_change !== undefined && item.health_change !== null && item.health_change !== 0) {
            statEffects.push(`Health: ${item.health_change > 0 ? '+' : ''}${item.health_change}`);
        }
        if (item.energy_change !== undefined && item.energy_change !== null && item.energy_change !== 0) {
            statEffects.push(`Energy: ${item.energy_change > 0 ? '+' : ''}${item.energy_change}`);
        }
        if (item.reputation_change !== undefined && item.reputation_change !== null && item.reputation_change !== 0) {
            statEffects.push(`Reputation: ${item.reputation_change > 0 ? '+' : ''}${item.reputation_change}`);
        }
        if (item.money_change !== undefined && item.money_change !== null && item.money_change !== 0) {
            statEffects.push(`Money: ${item.money_change > 0 ? '+' : ''}€${item.money_change.toFixed(2)}`);
        }
        
        html += `
            <div class="inventory-item">
                <strong>${item.name}</strong>
                <br>Bought: €${buyPrice.toFixed(2)} | Market: €${marketPrice.toFixed(2)}
                ${statEffects.length > 0 ? `<br><strong>Effects:</strong> ${statEffects.join(', ')} ${item.effect_frequency ? `(${item.effect_frequency})` : ''}` : ''}
            </div>
        `;
    });
    div.innerHTML = html;
    updateInventorySelect();
}

// Update inventory select for selling
function updateInventorySelect() {
    const select = document.getElementById('market-item-sell');
    const currentValue = select.value;
    
    // Clear and repopulate with inventory items
    select.innerHTML = '<option value="">Select item to sell...</option>';
    gameState.inventory.forEach(item => {
        const option = document.createElement('option');
        option.value = item.id;
        option.textContent = `${item.name} - Resell: €${item.market_price.toFixed(2)}`;
        option.dataset.price = item.market_price;
        select.appendChild(option);
    });
    
    // Restore selection if still valid
    if (currentValue) {
        select.value = currentValue;
    }
    
    // Enable/disable sell button
    document.getElementById('btn-sell-item').disabled = select.value === '';
}

// Update offers (this is now handled in updateAllOffers, but keeping for compatibility)
function updateOffers() {
    // This function is no longer needed as offers are shown in the "All Offers" tab
    // But we keep it to avoid errors if called
    // The actual update is done in updateAllOffers()
    updateAllOffers();
}

// Accept offer
async function acceptOffer(offerId) {
    await performAction('accept_offer', { offer_id: offerId });
}

// Send message to an offer
async function sendOfferMessage(offerId) {
    const input = document.getElementById(`message-input-${offerId}`);
    if (!input) {
        console.error('Message input not found for offer:', offerId);
        return;
    }
    
    const message = input.value.trim();
    if (!message) {
        alert('Please enter a message');
        return;
    }
    
    // Clear input
    input.value = '';
    
    // Send via WebSocket or HTTP
    try {
        await performAction('offer_message', {
            offer_id: offerId,
            message: message
        });
        
        // The UI will be updated when the state is refreshed
        // We could also show a loading indicator here
    } catch (error) {
        console.error('Error sending message:', error);
        alert('Failed to send message. Please try again.');
        // Restore message in input
        input.value = message;
    }
}

// Helper function to escape HTML
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Show hint for other offer (costs 10 EUR)
async function showOtherOfferHint(offerId) {
    try {
        await performAction('show_other_offer_hint', { offer_id: offerId });
        // UI will update automatically when state refreshes
    } catch (error) {
        console.error('Error showing hint:', error);
        alert('Failed to show hint. Please try again.');
    }
}

// Current offer ID for message modal
let currentMessageOfferId = null;

// Open message modal for an offer
function openOfferMessageModal(offerId) {
    if (!gameState || !gameState.active_offers) return;
    
    // Find the offer
    const offer = gameState.active_offers.find(o => o.id === offerId && o.type === 'other');
    if (!offer) {
        console.error('Offer not found:', offerId);
        return;
    }
    
    currentMessageOfferId = offerId;
    
    // Update modal header
    const titleEl = document.getElementById('offer-message-title');
    const descEl = document.getElementById('offer-message-description');
    if (titleEl) titleEl.textContent = offer.title;
    if (descEl) descEl.textContent = offer.description || '';
    
    // Update message list
    updateOfferMessageModalMessages(offer);
    
    // Show modal
    const modal = document.getElementById('offer-message-modal');
    if (modal) {
        modal.style.display = 'block';
        // Close modal when clicking outside
        modal.onclick = function(event) {
            if (event.target === modal) {
                closeOfferMessageModal();
            }
        };
        // Focus on input
        setTimeout(() => {
            const input = document.getElementById('offer-message-input-field');
            if (input) input.focus();
        }, 100);
    }
}

// Close message modal
function closeOfferMessageModal() {
    const modal = document.getElementById('offer-message-modal');
    if (modal) {
        modal.style.display = 'none';
    }
    currentMessageOfferId = null;
    // Clear input
    const input = document.getElementById('offer-message-input-field');
    if (input) input.value = '';
}

// Update message list in modal
function updateOfferMessageModalMessages(offer) {
    const messageListEl = document.getElementById('offer-message-list');
    if (!messageListEl) return;
    
    if (!offer.messages || offer.messages.length === 0) {
        messageListEl.innerHTML = '<p style="text-align: center; color: #999; padding: 20px;">No messages yet. Start the conversation!</p>';
        return;
    }
    
    let html = '';
    offer.messages.forEach((msg, idx) => {
        const isPlayer = idx % 2 === 0;
        html += `<div class="message-item ${isPlayer ? 'message-player' : 'message-response'}">`;
        html += `<span class="message-label">${isPlayer ? 'You:' : 'Response:'}</span> `;
        html += `<span class="message-text">${escapeHtml(msg)}</span>`;
        html += '</div>';
    });
    messageListEl.innerHTML = html;
    
    // Scroll to bottom
    messageListEl.scrollTop = messageListEl.scrollHeight;
}

// Send message from modal
async function sendOfferMessageFromModal() {
    if (!currentMessageOfferId) return;
    
    const input = document.getElementById('offer-message-input-field');
    if (!input) return;
    
    const message = input.value.trim();
    if (!message) {
        alert('Please enter a message');
        return;
    }
    
    // Clear input
    input.value = '';
    
    try {
        await performAction('offer_message', {
            offer_id: currentMessageOfferId,
            message: message
        });
        
        // Update modal messages after a short delay to allow state to update
        setTimeout(() => {
            if (gameState && gameState.active_offers) {
                const offer = gameState.active_offers.find(o => o.id === currentMessageOfferId && o.type === 'other');
                if (offer) {
                    updateOfferMessageModalMessages(offer);
                }
            }
        }, 500);
    } catch (error) {
        console.error('Error sending message:', error);
        alert('Failed to send message. Please try again.');
        // Restore message in input
        input.value = message;
    }
}

// Update history
function updateHistory() {
    const div = document.getElementById('history-list');
    if (!gameState.history || gameState.history.length === 0) {
        div.innerHTML = '<p class="empty">No history yet</p>';
        return;
    }
    
    let html = '';
    [...gameState.history].reverse().forEach(event => {
        const amount = event.amount || 0;
        const amountClass = amount > 0 ? 'positive' : amount < 0 ? 'negative' : '';
        const date = new Date(event.timestamp);
        html += `
            <div class="history-item ${amountClass}">
                <strong>${event.type}</strong>
                <p>${event.message}</p>
                ${amount !== 0 ? `<p>Amount: ${amount >= 0 ? '+' : ''}€${amount.toFixed(2)}</p>` : ''}
                <div class="timestamp">${date.toLocaleString()}</div>
            </div>
        `;
    });
    div.innerHTML = html;
}

// Update apartment offers
function updateApartmentOffers() {
    const div = document.getElementById('apartment-offers-list');
    if (!div) {
        console.warn('apartment-offers-list element not found');
        return;
    }
    
    if (!gameState.apartment_offers || gameState.apartment_offers.length === 0) {
        div.innerHTML = '<p class="empty">No apartment offers available. New offers will appear automatically!</p>';
        return;
    }
    
    let html = '';
    gameState.apartment_offers.forEach(offer => {
        const offerClass = offer.is_trickery ? 'trickery' : 'good';
        const expiresDate = new Date(offer.expires_at);
        const rent = offer.rent || 0;
        const healthGain = offer.health_gain || 0;
        const energyGain = offer.energy_gain || 0;
        
        html += `
            <div class="apartment-offer-item">
                <h4>${offer.title}</h4>
                <p>${offer.description}</p>
                <div class="apartment-offer-details">
                    <p><strong>Monthly Rent:</strong> €${rent.toFixed(2)}</p>
                    <p><strong>Health Gain:</strong> +${healthGain}/hour</p>
                    <p><strong>Energy Gain:</strong> +${energyGain}/hour</p>
                    <p><strong>Expires:</strong> ${expiresDate.toLocaleDateString()}</p>
                </div>
                <div id="apartment-hint-${offer.id}" style="display: ${offer.hint_shown ? 'block' : 'none'};">
                    ${offer.reason ? `<p class="offer-reason"><em>${offer.reason}</em></p>` : ''}
                    <p class="offer-type-badge ${offerClass}">${offer.is_trickery ? '⚠️ SCAM APARTMENT' : '✅ LEGITIMATE APARTMENT'}</p>
                </div>
                <div style="margin-top: 10px;">
                    <button class="btn btn-info" onclick="showApartmentHint('${offer.id}')" id="apartment-hint-btn-${offer.id}" ${offer.hint_shown ? 'disabled' : ''}>${offer.hint_shown ? 'Hint (Used)' : 'Hint (€10)'}</button>
                    <button class="btn btn-primary" onclick="acceptApartmentOffer('${offer.id}')">Rent Apartment</button>
                </div>
            </div>
        `;
    });
    div.innerHTML = html;
}

// Accept apartment offer
async function acceptApartmentOffer(offerId) {
    await performAction('accept_apartment_offer', { offer_id: offerId });
}

// Show hint for apartment offer (costs 10 EUR)
async function showApartmentHint(offerId) {
    try {
        const response = await fetch(`${API_BASE}/action?player_id=${PLAYER_ID}`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ 
                action: 'show_apartment_hint', 
                data: { offer_id: offerId }
            })
        });
        
        const result = await response.json();
        if (result.success) {
            gameState = result.game_state;
            // Show the hint
            const hintDiv = document.getElementById(`apartment-hint-${offerId}`);
            const hintBtn = document.getElementById(`apartment-hint-btn-${offerId}`);
            if (hintDiv) {
                hintDiv.style.display = 'block';
            }
            if (hintBtn) {
                hintBtn.disabled = true;
                hintBtn.textContent = 'Hint (Used)';
            }
            updateUI();
            showMessage(result.message, 'success');
        } else {
            showMessage(result.message, 'error');
        }
    } catch (error) {
        showMessage('Error showing hint: ' + error.message, 'error');
    }
}

// Update all offers tab
function updateAllOffers() {
    // Update job offers in all offers tab
    const jobOffersDiv = document.getElementById('all-job-offers');
    if (!jobOffersDiv) {
        console.warn('all-job-offers element not found');
        return;
    }
    
    if (!gameState.job_offers || gameState.job_offers.length === 0) {
        jobOffersDiv.innerHTML = '<p class="empty">No job offers</p>';
    } else {
        let html = '';
        gameState.job_offers.forEach(offer => {
            const salary = offer.salary || 0;
            const hoursPerDay = offer.hours_per_day || 8;
            html += `
                <div class="job-offer-item">
                    <h4>${offer.title}</h4>
                    <p>${offer.description}</p>
                    <p><strong>Salary:</strong> €${salary.toFixed(2)}/month | <strong>Hours:</strong> ${hoursPerDay}/day</p>
                    <button class="btn btn-primary btn-sm" onclick="acceptJobOffer('${offer.id}')">Accept</button>
                </div>
            `;
        });
        jobOffersDiv.innerHTML = html;
    }
    
    // Update apartment offers in all offers tab
    const apartmentOffersDiv = document.getElementById('all-apartment-offers');
    if (!apartmentOffersDiv) {
        console.warn('all-apartment-offers element not found');
        return;
    }
    
    if (!gameState.apartment_offers || gameState.apartment_offers.length === 0) {
        apartmentOffersDiv.innerHTML = '<p class="empty">No apartment offers</p>';
    } else {
        let html = '';
        gameState.apartment_offers.forEach(offer => {
            const rent = offer.rent || 0;
            html += `
                <div class="apartment-offer-item">
                    <h4>${offer.title}</h4>
                    <p>${offer.description}</p>
                    <p><strong>Rent:</strong> €${rent.toFixed(2)}/month | <strong>Health:</strong> +${offer.health_gain || 0}/h | <strong>Energy:</strong> +${offer.energy_gain || 0}/h</p>
                    <button class="btn btn-primary btn-sm" onclick="acceptApartmentOffer('${offer.id}')">Rent</button>
                </div>
            `;
        });
        apartmentOffersDiv.innerHTML = html;
    }
    
    // Update other offers in all offers tab
    const otherOffersDiv = document.getElementById('all-other-offers');
    if (!otherOffersDiv) {
        console.warn('all-other-offers element not found');
        return;
    }
    
    // Filter for "other" type offers
    const otherOffers = (gameState.active_offers || []).filter(offer => offer.type === 'other');
    
    if (otherOffers.length === 0) {
        otherOffersDiv.innerHTML = '<p class="empty">No other offers</p>';
    } else {
        let html = '';
        otherOffers.forEach(offer => {
            const price = offer.price || 0;
            const offerClass = offer.is_trickery ? 'trickery' : 'good';
            const statEffects = [];
            if (offer.health_change !== undefined && offer.health_change !== null && offer.health_change !== 0) {
                statEffects.push(`Health: ${offer.health_change > 0 ? '+' : ''}${offer.health_change}`);
            }
            if (offer.energy_change !== undefined && offer.energy_change !== null && offer.energy_change !== 0) {
                statEffects.push(`Energy: ${offer.energy_change > 0 ? '+' : ''}${offer.energy_change}`);
            }
            if (offer.reputation_change !== undefined && offer.reputation_change !== null && offer.reputation_change !== 0) {
                statEffects.push(`Reputation: ${offer.reputation_change > 0 ? '+' : ''}${offer.reputation_change}`);
            }
            if (offer.money_change !== undefined && offer.money_change !== null && offer.money_change !== 0) {
                statEffects.push(`Money: ${offer.money_change > 0 ? '+' : ''}€${offer.money_change.toFixed(2)}`);
            }
            
            const createdByText = offer.created_by ? ` <span style="font-size: 0.85em; color: #666; font-weight: normal;">(by ${offer.created_by})</span>` : '';
            
            // Don't show messages inline anymore - they're in the modal
            // But we'll show a link if there are messages
            const hasMessages = offer.messages && offer.messages.length > 0;
            
            // Hint section (shown ONLY when hint is purchased)
            // DO NOT show reason or hint info unless hint_shown is true
            let hintHtml = '';
            if (offer.hint_shown === true) {
                hintHtml = `
                    <div id="hint-${offer.id}" class="offer-hint-section">
                        ${offer.reason ? `<p class="offer-reason"><em>${offer.reason}</em></p>` : ''}
                        <p class="offer-type-badge ${offerClass}">${offer.is_trickery ? '⚠️ TRICKERY/SCAM' : '✅ LEGITIMATE OFFER'}</p>
                    </div>
                `;
            }
            
            html += `
                <div class="offer-item ${offerClass}">
                    <h4>${offer.title}${createdByText}</h4>
                    <p>${offer.description}</p>
                    <p><strong>Price:</strong> €${price.toFixed(2)}</p>
                    ${statEffects.length > 0 ? `<p><strong>Effects:</strong> ${statEffects.join(', ')}</p>` : ''}
                    ${hintHtml}
                    <div class="offer-actions">
                        <button class="btn btn-primary btn-sm" onclick="acceptOffer('${offer.id}')">Accept</button>
                        ${offer.hint_shown === undefined || offer.hint_shown === false ? `<button class="btn btn-info btn-sm" onclick="showOtherOfferHint('${offer.id}')">Hint (€10)</button>` : ''}
                        <button class="btn btn-secondary btn-sm" onclick="openOfferMessageModal('${offer.id}')">💬 Message${hasMessages ? ` (${offer.messages.length})` : ''}</button>
                    </div>
                    ${hasMessages ? `<div style="margin-top: 10px;"><button class="btn btn-link btn-sm" onclick="openOfferMessageModal('${offer.id}')" style="padding: 0; text-decoration: underline; color: #667eea; background: none; border: none; cursor: pointer;">View ${offer.messages.length} message${offer.messages.length !== 1 ? 's' : ''}</button></div>` : ''}
                </div>
            `;
        });
        otherOffersDiv.innerHTML = html;
    }
}

// Update agreements list
function updateAgreements() {
    const div = document.getElementById('agreements-list');
    if (!div) {
        console.warn('agreements-list element not found');
        return;
    }
    
    if (!gameState.agreements || gameState.agreements.length === 0) {
        div.innerHTML = '<p class="empty">No active agreements. Agreements are created when you accept recurring offers.</p>';
        return;
    }
    
    let html = '';
    gameState.agreements.forEach(agreement => {
        const startedDate = new Date(agreement.started_at);
        const currentDate = new Date(gameState.current_date);
        const daysActive = Math.floor((currentDate - startedDate) / (1000 * 60 * 60 * 24));
        
        // Calculate potential penalty
        let penalty = 0;
        let penaltyText = '';
        switch(agreement.recurrence_type) {
            case 'daily':
                if (daysActive < 1) penalty = 50;
                else if (daysActive < 7) penalty = 25;
                break;
            case 'weekly':
                if (daysActive < 7) penalty = 100;
                else if (daysActive < 30) penalty = 50;
                break;
            case 'monthly':
                if (daysActive < 30) penalty = 200;
                else if (daysActive < 90) penalty = 100;
                break;
        }
        
        if (penalty > 0) {
            penaltyText = `⚠️ Early termination penalty: €${penalty.toFixed(2)}`;
        } else {
            penaltyText = 'No early termination penalty';
        }
        
        const statEffects = [];
        if (agreement.health_change !== undefined && agreement.health_change !== null && agreement.health_change !== 0) {
            statEffects.push(`Health: ${agreement.health_change > 0 ? '+' : ''}${agreement.health_change}`);
        }
        if (agreement.energy_change !== undefined && agreement.energy_change !== null && agreement.energy_change !== 0) {
            statEffects.push(`Energy: ${agreement.energy_change > 0 ? '+' : ''}${agreement.energy_change}`);
        }
        if (agreement.reputation_change !== undefined && agreement.reputation_change !== null && agreement.reputation_change !== 0) {
            statEffects.push(`Reputation: ${agreement.reputation_change > 0 ? '+' : ''}${agreement.reputation_change}`);
        }
        if (agreement.money_change !== undefined && agreement.money_change !== null && agreement.money_change !== 0) {
            statEffects.push(`Money: ${agreement.money_change > 0 ? '+' : ''}€${agreement.money_change.toFixed(2)}`);
        }
        
        html += `
            <div class="agreement-item ${agreement.is_trickery ? 'trickery' : 'good'}">
                <h4>${agreement.title}</h4>
                <p>${agreement.description}</p>
                <div class="agreement-details">
                    <p><strong>Recurrence:</strong> ${agreement.recurrence_type}</p>
                    <p><strong>Started:</strong> ${startedDate.toLocaleDateString()}</p>
                    <p><strong>Days Active:</strong> ${daysActive}</p>
                    <p><strong>Effects per ${agreement.recurrence_type}:</strong> ${statEffects.length > 0 ? statEffects.join(', ') : 'None'}</p>
                    <p class="penalty-text">${penaltyText}</p>
                    ${agreement.reason ? `<p class="agreement-reason"><em>${agreement.reason}</em></p>` : ''}
                </div>
                <button class="btn btn-danger btn-sm" onclick="quitAgreement('${agreement.id}')">Cancel Agreement</button>
            </div>
        `;
    });
    div.innerHTML = html;
}

// Quit agreement
async function quitAgreement(agreementId) {
    if (!confirm('Are you sure you want to cancel this agreement? You may have to pay an early termination penalty.')) {
        return;
    }
    await performAction('quit_agreement', { agreement_id: agreementId });
}

// Update dashboard
function updateDashboard() {
    const money = gameState.money || 0;
    document.getElementById('dashboard-money').textContent = `€${money.toFixed(2)}`;
    
    // Calculate investments value
    let investmentsValue = 0;
    if (gameState.stocks) {
        gameState.stocks.forEach(stock => {
            investmentsValue += (stock.current_price || stock.buy_price || 0) * (stock.shares || 0);
        });
    }
    if (gameState.crypto) {
        gameState.crypto.forEach(crypto => {
            investmentsValue += (crypto.current_price || crypto.buy_price || 0) * (crypto.amount || 0);
        });
    }
    document.getElementById('dashboard-investments').textContent = `€${investmentsValue.toFixed(2)}`;
    document.getElementById('dashboard-total').textContent = `€${(money + investmentsValue).toFixed(2)}`;
    
    // Update health and energy
    const health = gameState.health !== undefined ? gameState.health : 100;
    const energy = gameState.energy !== undefined ? gameState.energy : 100;
    document.getElementById('dashboard-health').textContent = health;
    document.getElementById('dashboard-energy').textContent = energy;
    
    // Update status
    document.getElementById('dashboard-job').textContent = gameState.job ? gameState.job.title : 'None';
    document.getElementById('dashboard-apartment').textContent = gameState.apartment ? gameState.apartment.title : 'None';
    document.getElementById('dashboard-reputation').textContent = gameState.reputation || 0;
}

// Show message
function showMessage(text, type = 'info') {
    const messageDiv = document.getElementById('message');
    messageDiv.textContent = text;
    messageDiv.className = `message ${type}`;
    messageDiv.style.display = 'block';
    
    setTimeout(() => {
        messageDiv.style.display = 'none';
    }, 3000);
}
