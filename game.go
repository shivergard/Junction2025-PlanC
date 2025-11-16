package main

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

func abs(x float64) float64 {
	return math.Abs(x)
}


// Random seed is automatically initialized in Go 1.20+

const (
	InitialMoney = 10000.0
	GameStartDate = "2000-01-02T08:00:00Z"
	WorkDayDuration = 8 * time.Hour // 8 hours of work
	SalaryPaymentDay = 1 // 1st of each month
	NightStartHour = 0  // Night starts at 00:00
	NightEndHour = 7    // Night ends at 07:00
)

var (
	availableJobs = []Job{
		{ID: "job1", Title: "Junior Developer", Salary: 500, Description: "Entry-level programming job", HoursPerDay: 8},
		{ID: "job2", Title: "Freelance Designer", Salary: 300, Description: "Flexible design work", HoursPerDay: 6},
		{ID: "job3", Title: "Part-time Retail", Salary: 200, Description: "Retail store assistant", HoursPerDay: 4},
		{ID: "job4", Title: "Tutor", Salary: 400, Description: "Teaching students", HoursPerDay: 5},
	}
	
	marketItems = []Item{
		{ID: "item1", Name: "Laptop", MarketPrice: 800, BuyPrice: 0},
		{ID: "item2", Name: "Phone", MarketPrice: 500, BuyPrice: 0},
		{ID: "item3", Name: "Watch", MarketPrice: 200, BuyPrice: 0},
		{ID: "item4", Name: "Headphones", MarketPrice: 150, BuyPrice: 0},
		{ID: "item5", Name: "Tablet", MarketPrice: 400, BuyPrice: 0},
	}
	
	stockSymbols = []string{"TECH", "FIN", "ENERGY", "HEALTH", "RETAIL"}
	cryptoSymbols = []string{"BTC", "ETH", "SOL", "ADA", "DOT"}
)

// NewGame creates a new game state
func NewGame(playerID string) *GameState {
	startDate, _ := time.Parse(time.RFC3339, GameStartDate)
	return &GameState{
		PlayerID:      playerID,
		Money:         InitialMoney,
		InitialMoney:  InitialMoney,
		Reputation:    0,
		Health:        100, // Start with full health
		Energy:        100, // Start with full energy
		CurrentDate:   startDate,
		Stocks:        []Stock{},
		Crypto:        []Crypto{},
		Inventory:     []Item{},
		History:       []Event{},
		ActiveOffers:  []Offer{},
		JobOffers:     []JobOffer{},
		ApartmentOffers: []ApartmentOffer{},
		StockOffers:   []StockOffer{},
		StockHistory:  []StockHistory{},
		Agreements:    []Agreement{},
		IsWorking:     false,
		IsInHospital:  false,
		GameOver:      false,
		IsFirstPlayer: false,
		CreatedAt:     time.Now(),
	}
}

// StartWork starts a work session (only for hourly jobs)
func (gs *GameState) StartWork() error {
	if gs.GameOver {
		return &GameError{Message: "Game is over. You cannot perform actions."}
	}
	if gs.IsInHospital {
		return &GameError{Message: "You are in the hospital and cannot work. You'll be released when your health reaches 20."}
	}
	if gs.Job == nil {
		return &GameError{Message: "You don't have a job. Accept a job offer first!"}
	}
	
	if gs.Job.WorkType == "fixed_time" {
		return &GameError{Message: "This is a fixed-time job. Work starts and ends automatically at scheduled times."}
	}
	
	if gs.IsWorking {
		return &GameError{Message: "You are already working"}
	}
	
	gs.IsWorking = true
	gs.WorkStartTime = gs.CurrentDate
	// For hourly jobs, work duration is based on hours per day
	workDuration := time.Duration(gs.Job.HoursPerDay) * time.Hour
	gs.WorkEndTime = gs.CurrentDate.Add(workDuration)
	gs.addEvent("work_start", "Started working at "+gs.Job.Title+" (Time moves 10x faster while working)", 0)
	return nil
}

// StopWork stops a work session (only for hourly jobs)
func (gs *GameState) StopWork() error {
	if gs.Job == nil {
		return &GameError{Message: "You don't have a job"}
	}
	
	if gs.Job.WorkType == "fixed_time" {
		return &GameError{Message: "This is a fixed-time job. You cannot stop work manually."}
	}
	
	if !gs.IsWorking {
		return &GameError{Message: "You are not currently working"}
	}
	
	// Calculate hours worked
	hoursWorked := gs.CurrentDate.Sub(gs.WorkStartTime).Hours()
	if hoursWorked < 0 {
		hoursWorked = 0
	}
	
	gs.IsWorking = false
	gs.WorkStartTime = time.Time{}
	gs.WorkEndTime = time.Time{}
	
	gs.addEvent("work_stop", fmt.Sprintf("Stopped working at %s (Worked %.2f hours)", gs.Job.Title, hoursWorked), 0)
	return nil
}

// CheckWorkStatus checks if work is complete and processes salary
func (gs *GameState) CheckWorkStatus() {
	if gs.Job == nil {
		return
	}
	
	// Handle fixed-time jobs
	if gs.Job.WorkType == "fixed_time" {
		gs.checkFixedTimeWork()
		return
	}
	
	// Handle hourly jobs
	if !gs.IsWorking {
		return
	}
	
	// Check if work day is complete for hourly jobs
	if gs.CurrentDate.After(gs.WorkEndTime) || gs.CurrentDate.Equal(gs.WorkEndTime) {
		gs.IsWorking = false
		gs.addEvent("work_end", "Finished working at "+gs.Job.Title, 0)
	}
	
	// Process salary payment (same for both types)
	gs.processSalary()
}

// checkFixedTimeWork handles fixed-time job work schedule
func (gs *GameState) checkFixedTimeWork() {
	if gs.Job.WorkStart == "" || gs.Job.WorkEnd == "" {
		return
	}
	
	// Parse work hours (format: "HH:MM")
	currentTime := gs.CurrentDate.Format("15:04")
	
	// Check if current time is within work hours
	if currentTime >= gs.Job.WorkStart && currentTime < gs.Job.WorkEnd {
		if !gs.IsWorking {
			gs.IsWorking = true
			gs.WorkStartTime = gs.CurrentDate
			gs.addEvent("work_start", "Started working at "+gs.Job.Title+" (Fixed schedule: "+gs.Job.WorkStart+"-"+gs.Job.WorkEnd+")", 0)
		}
		// Lose health and energy while working
		gs.loseHealthEnergyFromWork()
	} else {
		if gs.IsWorking {
			gs.IsWorking = false
			gs.WorkStartTime = time.Time{}
			gs.WorkEndTime = time.Time{}
			gs.addEvent("work_end", "Finished working at "+gs.Job.Title+" (Fixed schedule ended)", 0)
		}
	}
}

// loseHealthEnergyFromWork reduces health and energy while working (using AI-determined rates)
func (gs *GameState) loseHealthEnergyFromWork() {
	if !gs.IsWorking || gs.Job == nil {
		return
	}
	
	// Use AI-determined health and energy loss rates from the job
	// This is called periodically, so we lose small amounts based on the job's characteristics
	healthLoss := int(gs.Job.HealthLossPerHour)
	energyLoss := int(gs.Job.EnergyLossPerHour)
	
	// Ensure at least 1 point is lost if the rate is very low
	if healthLoss < 1 && gs.Job.HealthLossPerHour > 0 {
		healthLoss = 1
	}
	if energyLoss < 1 && gs.Job.EnergyLossPerHour > 0 {
		energyLoss = 1
	}
	
	gs.Health -= healthLoss
	if gs.Health < 0 {
		gs.Health = 0
	}
	
	gs.Energy -= energyLoss
	if gs.Energy < 0 {
		gs.Energy = 0
	}
}

// gainHealthEnergyFromApartment increases health and energy while in apartment
func (gs *GameState) gainHealthEnergyFromApartment() {
	if gs.Apartment == nil {
		return
	}
	
	// Gain health and energy per hour in apartment
	healthGain := gs.Apartment.HealthGain
	energyGain := gs.Apartment.EnergyGain
	
	gs.Health += healthGain
	if gs.Health > 100 {
		gs.Health = 100
	}
	
	gs.Energy += energyGain
	if gs.Energy > 100 {
		gs.Energy = 100
	}
}

// processSalary processes monthly salary payment and rent
func (gs *GameState) processSalary() {
	// Check if it's salary payment day (1st of month)
	if gs.CurrentDate.Day() == SalaryPaymentDay {
		// Process salary
		if gs.Job != nil {
			// Check if we haven't paid this month yet
			if gs.LastSalaryDate.IsZero() || 
			   gs.LastSalaryDate.Year() != gs.CurrentDate.Year() || 
			   gs.LastSalaryDate.Month() != gs.CurrentDate.Month() {
				gs.Money += gs.Job.Salary
				gs.LastSalaryDate = gs.CurrentDate
				gs.addEvent("salary", "Received monthly salary: €"+formatMoney(gs.Job.Salary)+" from "+gs.Job.Title, gs.Job.Salary)
			}
		}
		
		// Process rent payment
		if gs.Apartment != nil {
			if gs.Money >= gs.Apartment.Rent {
				gs.Money -= gs.Apartment.Rent
				gs.addEvent("rent_paid", "Paid monthly rent: €"+formatMoney(gs.Apartment.Rent)+" for "+gs.Apartment.Title, -gs.Apartment.Rent)
			} else {
				gs.addEvent("rent_failed", "Failed to pay rent: €"+formatMoney(gs.Apartment.Rent)+" for "+gs.Apartment.Title+" (Not enough money!)", 0)
				// Could add logic to evict player if rent not paid
			}
		}
	}
}

// IsNightTime checks if current time is during night hours (00:00 - 07:00)
func (gs *GameState) IsNightTime() bool {
	hour := gs.CurrentDate.Hour()
	return hour >= NightStartHour && hour < NightEndHour
}

// AcceptJobOffer accepts a job offer
func (gs *GameState) AcceptJobOffer(offerID string) error {
	if gs.GameOver {
		return &GameError{Message: "Game is over. You cannot perform actions."}
	}
	if gs.IsInHospital {
		return &GameError{Message: "You are in the hospital and cannot accept job offers. You'll be released when your health reaches 20."}
	}
	if gs.Job != nil {
		return &GameError{Message: "You already have a job: " + gs.Job.Title + ". Quit first to accept a new one."}
	}
	
	// Check if it's night time - cannot accept jobs during night
	if gs.IsNightTime() {
		return &GameError{Message: "You cannot accept job offers during night hours (00:00 - 07:00). Please wait until morning."}
	}
	
	offerIndex := -1
	for i, offer := range gs.JobOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Job offer not found or expired"}
	}
	
	offer := gs.JobOffers[offerIndex]
	if gs.CurrentDate.After(offer.ExpiresAt) {
		gs.JobOffers = append(gs.JobOffers[:offerIndex], gs.JobOffers[offerIndex+1:]...)
		return &GameError{Message: "Job offer has expired"}
	}
	
	// Check if there's an upfront cost (common in scam jobs)
	if offer.UpfrontCost > 0 {
		if gs.Money < offer.UpfrontCost {
			return &GameError{Message: "Not enough money for upfront cost. Need €" + formatMoney(offer.UpfrontCost) + " (training fees, materials, etc.)"}
		}
		gs.Money -= offer.UpfrontCost
	}
	
	// Create job from offer
	gs.Job = &Job{
		ID:                offer.ID,
		Title:             offer.Title,
		Salary:            offer.Salary,
		Description:       offer.Description,
		HoursPerDay:       offer.HoursPerDay,
		WorkType:          offer.WorkType,
		WorkStart:         offer.WorkStart,
		WorkEnd:           offer.WorkEnd,
		HealthLossPerHour: offer.HealthLossPerHour,
		EnergyLossPerHour: offer.EnergyLossPerHour,
		IsTrickery:        offer.IsTrickery,
		Reason:            offer.Reason,
	}
	
	gs.JobOffers = append(gs.JobOffers[:offerIndex], gs.JobOffers[offerIndex+1:]...)
	
	eventMsg := "Accepted job: " + offer.Title + " - Monthly salary: €" + formatMoney(offer.Salary)
	if offer.UpfrontCost > 0 {
		eventMsg += " - Paid upfront cost: €" + formatMoney(offer.UpfrontCost)
	}
	gs.addEvent("job_accepted", eventMsg, -offer.UpfrontCost)
	return nil
}

// ShowHint shows a hint about a job offer (costs 10 EUR)
func (gs *GameState) ShowHint(offerID string) error {
	const hintCost = 10.0
	
	if gs.Money < hintCost {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(hintCost) + " for hint"}
	}
	
	offerIndex := -1
	for i, offer := range gs.JobOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Job offer not found"}
	}
	
	// Check if hint already shown
	if gs.JobOffers[offerIndex].HintShown {
		return &GameError{Message: "Hint already purchased for this offer"}
	}
	
	gs.Money -= hintCost
	gs.JobOffers[offerIndex].HintShown = true
	gs.addEvent("hint_purchased", "Purchased hint for job offer: "+gs.JobOffers[offerIndex].Title, -hintCost)
	return nil
}

// AcceptApartmentOffer accepts an apartment offer
func (gs *GameState) AcceptApartmentOffer(offerID string) error {
	if gs.Apartment != nil {
		return &GameError{Message: "You already have an apartment: " + gs.Apartment.Title + ". You can only have one apartment at a time."}
	}
	
	offerIndex := -1
	for i, offer := range gs.ApartmentOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Apartment offer not found or expired"}
	}
	
	offer := gs.ApartmentOffers[offerIndex]
	if gs.CurrentDate.After(offer.ExpiresAt) {
		gs.ApartmentOffers = append(gs.ApartmentOffers[:offerIndex], gs.ApartmentOffers[offerIndex+1:]...)
		return &GameError{Message: "Apartment offer has expired"}
	}
	
	// Create apartment from offer
	gs.Apartment = &Apartment{
		ID:          offer.ID,
		Title:       offer.Title,
		Rent:        offer.Rent,
		Description: offer.Description,
		HealthGain:  offer.HealthGain,
		EnergyGain:  offer.EnergyGain,
		IsTrickery:  offer.IsTrickery,
		Reason:      offer.Reason,
	}
	
	gs.ApartmentOffers = append(gs.ApartmentOffers[:offerIndex], gs.ApartmentOffers[offerIndex+1:]...)
	
	eventMsg := "Rented apartment: " + offer.Title + " - Monthly rent: €" + formatMoney(offer.Rent)
	gs.addEvent("apartment_rented", eventMsg, 0)
	return nil
}

// ShowApartmentHint shows a hint about an apartment offer (costs 10 EUR)
func (gs *GameState) ShowApartmentHint(offerID string) error {
	const hintCost = 10.0
	
	if gs.Money < hintCost {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(hintCost) + " for hint"}
	}
	
	offerIndex := -1
	for i, offer := range gs.ApartmentOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Apartment offer not found"}
	}
	
	if gs.ApartmentOffers[offerIndex].HintShown {
		return &GameError{Message: "Hint already shown for this offer"}
	}
	
	gs.Money -= hintCost
	gs.ApartmentOffers[offerIndex].HintShown = true
	gs.addEvent("hint_purchased", "Purchased hint for apartment offer: "+gs.ApartmentOffers[offerIndex].Title, -hintCost)
	return nil
}

// QuitApartment quits the current apartment
func (gs *GameState) QuitApartment() error {
	if gs.Apartment == nil {
		return &GameError{Message: "You don't have an apartment"}
	}
	
	apartmentTitle := gs.Apartment.Title
	gs.Apartment = nil
	gs.addEvent("apartment_quit", "Moved out of apartment: "+apartmentTitle, 0)
	return nil
}

// QuitJob quits the current job (Rage Quit - can quit even while working)
func (gs *GameState) QuitJob() error {
	if gs.Job == nil {
		return &GameError{Message: "You don't have a job to quit"}
	}
	
	jobTitle := gs.Job.Title
	isTrickery := gs.Job.IsTrickery
	
	// Stop working if currently working
	if gs.IsWorking {
		gs.IsWorking = false
		gs.WorkStartTime = time.Time{}
		gs.WorkEndTime = time.Time{}
	}
	
	gs.Job = nil
	gs.LastSalaryDate = time.Time{}
	
	// Apply reputation penalty if it was NOT a scam job
	if !isTrickery {
		gs.Reputation -= 1
		gs.addEvent("job_quit", "Rage Quit job: "+jobTitle+" (Lost 1 Reputation - it was a legitimate job!)", 0)
	} else {
		gs.addEvent("job_quit", "Rage Quit job: "+jobTitle+" (No reputation loss - it was a scam!)", 0)
	}
	
	return nil
}

// BuyStock purchases stock shares from a stock offer
func (gs *GameState) BuyStock(offerID string, shares int) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	if shares <= 0 {
		return &GameError{Message: "Invalid number of shares"}
	}
	
	// Find stock offer
	offerIndex := -1
	for i, offer := range gs.StockOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Stock offer not found or expired"}
	}
	
	offer := gs.StockOffers[offerIndex]
	if gs.CurrentDate.After(offer.ExpiresAt) {
		gs.StockOffers = append(gs.StockOffers[:offerIndex], gs.StockOffers[offerIndex+1:]...)
		return &GameError{Message: "Stock offer has expired"}
	}
	
	price := offer.CurrentPrice
	totalCost := price * float64(shares)
	
	if gs.Money < totalCost {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(totalCost)}
	}
	
	gs.Money -= totalCost
	stock := Stock{
		Symbol:       offer.Symbol,
		Shares:       shares,
		BuyPrice:     price,
		CurrentPrice: price,
		BoughtAt:     gs.CurrentDate,
	}
	gs.Stocks = append(gs.Stocks, stock)
	
	// Add to stock history
	gs.StockHistory = append(gs.StockHistory, StockHistory{
		Symbol: offer.Symbol,
		Price:  price,
		Date:   gs.CurrentDate,
		Event:  "buy",
	})
	
	gs.addEvent("stock_buy", fmt.Sprintf("Bought %d shares of %s (%s) at €%.2f", shares, offer.Symbol, offer.CompanyName, price), -totalCost)
	return nil
}

// SellStock sells stock shares
func (gs *GameState) SellStock(symbol string, shares int) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	if shares <= 0 {
		return &GameError{Message: "Invalid number of shares"}
	}
	
	// Find stock
	stockIndex := -1
	for i, s := range gs.Stocks {
		if s.Symbol == symbol {
			stockIndex = i
			break
		}
	}
	
	if stockIndex == -1 {
		return &GameError{Message: "You don't own any " + symbol + " stock"}
	}
	
	stock := &gs.Stocks[stockIndex]
	if stock.Shares < shares {
		return &GameError{Message: "You only own " + formatInt(stock.Shares) + " shares"}
	}
	
	// Update price with some volatility
	change := (rand.Float64() - 0.5) * 0.2 // ±10% change
	stock.CurrentPrice = stock.BuyPrice * (1 + change)
	
	revenue := stock.CurrentPrice * float64(shares)
	gs.Money += revenue
	stock.Shares -= shares
	
	if stock.Shares == 0 {
		gs.Stocks = append(gs.Stocks[:stockIndex], gs.Stocks[stockIndex+1:]...)
	}
	
	profit := revenue - (stock.BuyPrice * float64(shares))
	gs.addEvent("stock_sell", "Sold "+formatInt(shares)+" shares of "+symbol+" for €"+formatMoney(revenue), revenue)
	if profit > 0 {
		gs.addEvent("profit", "Made a profit of €"+formatMoney(profit), profit)
	} else {
		gs.addEvent("loss", "Lost €"+formatMoney(-profit), -profit)
	}
	return nil
}

// ShowStockHint shows a hint about a stock offer (costs 10 EUR)
func (gs *GameState) ShowStockHint(offerID string) error {
	const hintCost = 10.0
	
	if gs.Money < hintCost {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(hintCost) + " for hint"}
	}
	
	offerIndex := -1
	for i, offer := range gs.StockOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Stock offer not found"}
	}
	
	if gs.StockOffers[offerIndex].HintShown {
		return &GameError{Message: "Hint already shown for this stock offer"}
	}
	
	gs.Money -= hintCost
	gs.StockOffers[offerIndex].HintShown = true
	gs.addEvent("stock_hint", "Purchased hint for stock "+gs.StockOffers[offerIndex].Symbol, -hintCost)
	return nil
}

// ShowOtherOfferHint shows a hint about an "other" offer (costs 10 EUR)
func (gs *GameState) ShowOtherOfferHint(offerID string) error {
	const hintCost = 10.0
	
	if gs.Money < hintCost {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(hintCost) + " for hint"}
	}
	
	offerIndex := -1
	for i, offer := range gs.ActiveOffers {
		if offer.ID == offerID && offer.Type == "other" {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Offer not found or not an 'other' type offer"}
	}
	
	if gs.ActiveOffers[offerIndex].HintShown {
		return &GameError{Message: "Hint already shown for this offer"}
	}
	
	gs.Money -= hintCost
	gs.ActiveOffers[offerIndex].HintShown = true
	gs.addEvent("offer_hint", "Purchased hint for offer: "+gs.ActiveOffers[offerIndex].Title, -hintCost)
	return nil
}

// BuyCrypto purchases cryptocurrency
func (gs *GameState) BuyCrypto(symbol string, amount float64) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	if amount <= 0 {
		return &GameError{Message: "Invalid amount"}
	}
	
	// Generate random price between 1000-5000
	price := 1000.0 + rand.Float64()*4000.0
	totalCost := price * amount
	
	if gs.Money < totalCost {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(totalCost)}
	}
	
	gs.Money -= totalCost
	crypto := Crypto{
		Symbol:      symbol,
		Amount:      amount,
		BuyPrice:    price,
		CurrentPrice: price,
		BoughtAt:    time.Now(),
	}
	gs.Crypto = append(gs.Crypto, crypto)
	gs.addEvent("crypto_buy", "Bought "+formatFloat(amount)+" "+symbol+" at €"+formatMoney(price), -totalCost)
	return nil
}

// SellCrypto sells cryptocurrency
func (gs *GameState) SellCrypto(symbol string, amount float64) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	if amount <= 0 {
		return &GameError{Message: "Invalid amount"}
	}
	
	// Find crypto
	cryptoIndex := -1
	for i, c := range gs.Crypto {
		if c.Symbol == symbol {
			cryptoIndex = i
			break
		}
	}
	
	if cryptoIndex == -1 {
		return &GameError{Message: "You don't own any " + symbol}
	}
	
	crypto := &gs.Crypto[cryptoIndex]
	if crypto.Amount < amount {
		return &GameError{Message: "You only own " + formatFloat(crypto.Amount) + " " + symbol}
	}
	
	// Update price with volatility
	change := (rand.Float64() - 0.5) * 0.3 // ±15% change
	crypto.CurrentPrice = crypto.BuyPrice * (1 + change)
	
	revenue := crypto.CurrentPrice * amount
	gs.Money += revenue
	crypto.Amount -= amount
	
	if crypto.Amount == 0 {
		gs.Crypto = append(gs.Crypto[:cryptoIndex], gs.Crypto[cryptoIndex+1:]...)
	}
	
	profit := revenue - (crypto.BuyPrice * amount)
	gs.addEvent("crypto_sell", "Sold "+formatFloat(amount)+" "+symbol+" for €"+formatMoney(revenue), revenue)
	if profit > 0 {
		gs.addEvent("profit", "Made a profit of €"+formatMoney(profit), profit)
	} else {
		gs.addEvent("loss", "Lost €"+formatMoney(-profit), -profit)
	}
	return nil
}

// BuyItem purchases an item from market
func (gs *GameState) BuyItem(itemID string, price float64) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	if gs.Money < price {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(price)}
	}
	
	// Find item template
	var itemTemplate *Item
	for i := range marketItems {
		if marketItems[i].ID == itemID {
			itemTemplate = &marketItems[i]
			break
		}
	}
	
	if itemTemplate == nil {
		return &GameError{Message: "Item not found"}
	}
	
	gs.Money -= price
	item := Item{
		ID:          itemID,
		Name:        itemTemplate.Name,
		BuyPrice:    price,
		MarketPrice: itemTemplate.MarketPrice,
		BoughtAt:    time.Now(),
	}
	gs.Inventory = append(gs.Inventory, item)
	gs.addEvent("item_buy", "Bought "+item.Name+" for €"+formatMoney(price), -price)
	return nil
}

// SellItem sells an item from inventory
func (gs *GameState) SellItem(itemID string) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	itemIndex := -1
	for i, item := range gs.Inventory {
		if item.ID == itemID {
			itemIndex = i
			break
		}
	}
	
	if itemIndex == -1 {
		return &GameError{Message: "Item not found in inventory"}
	}
	
	item := gs.Inventory[itemIndex]
	// Resale at market price (could be less than buy price)
	revenue := item.MarketPrice
	gs.Money += revenue
	gs.Inventory = append(gs.Inventory[:itemIndex], gs.Inventory[itemIndex+1:]...)
	
	profit := revenue - item.BuyPrice
	gs.addEvent("item_sell", "Sold "+item.Name+" for €"+formatMoney(revenue), revenue)
	if profit < 0 {
		gs.addEvent("loss", "Lost €"+formatMoney(-profit)+" on resale", -profit)
	}
	return nil
}

// AcceptOffer accepts an AI-generated offer
func (gs *GameState) AcceptOffer(offerID string) error {
	if gs.GameOver {
		return &GameError{Message: "Game is over. You cannot perform actions."}
	}
	if !gs.CanPerformAction() {
		if gs.IsInHospital {
			return &GameError{Message: "You are in the hospital and cannot perform this action. You'll be released when your health reaches 20."}
		}
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	offerIndex := -1
	for i, offer := range gs.ActiveOffers {
		if offer.ID == offerID {
			offerIndex = i
			break
		}
	}
	
	if offerIndex == -1 {
		return &GameError{Message: "Offer not found or expired"}
	}
	
	offer := gs.ActiveOffers[offerIndex]
	if gs.CurrentDate.After(offer.ExpiresAt) {
		gs.ActiveOffers = append(gs.ActiveOffers[:offerIndex], gs.ActiveOffers[offerIndex+1:]...)
		return &GameError{Message: "Offer has expired"}
	}
	
	if gs.Money < offer.Price {
		return &GameError{Message: "Not enough money. Need €" + formatMoney(offer.Price)}
	}
	
	// Deduct price
	gs.Money -= offer.Price
	
	// Apply immediate stat effects
	if offer.HealthChange != 0 {
		gs.Health += offer.HealthChange
		if gs.Health < 0 {
			gs.Health = 0
		}
		if gs.Health > 100 {
			gs.Health = 100
		}
	}
	
	if offer.EnergyChange != 0 {
		gs.Energy += offer.EnergyChange
		if gs.Energy < 0 {
			gs.Energy = 0
		}
		if gs.Energy > 100 {
			gs.Energy = 100
		}
	}
	
	if offer.ReputationChange != 0 {
		gs.Reputation += offer.ReputationChange
	}
	
	if offer.MoneyChange != 0 {
		gs.Money += offer.MoneyChange
	}
	
	// Determine if this is a recurring agreement or a one-time item
	if offer.IsRecurring {
		// Create an Agreement
		recurrenceType := offer.RecurrenceType
		if recurrenceType == "" {
			recurrenceType = "monthly" // Default to monthly
		}
		
		agreement := Agreement{
			ID:              fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(10000)),
			Title:           offer.Title,
			Description:     offer.Description,
			RecurrenceType:  recurrenceType,
			StartedAt:       gs.CurrentDate,
			LastProcessedAt: gs.CurrentDate,
			HealthChange:    offer.HealthChange,
			EnergyChange:    offer.EnergyChange,
			ReputationChange: offer.ReputationChange,
			MoneyChange:     offer.MoneyChange,
			IsTrickery:      offer.IsTrickery,
			Reason:          offer.Reason,
			IsReciprocal:    false, // Buyer's agreement (not reciprocal)
			OtherPartyID:    offer.CreatedBy, // Track the creator if it's a player-created offer
			OriginalPrice:   offer.Price, // Store original price for penalty calculation
		}
		gs.Agreements = append(gs.Agreements, agreement)
		
		eventMsg := fmt.Sprintf("Started agreement: %s (Recurring: %s)", offer.Title, recurrenceType)
		gs.addEvent("agreement_started", eventMsg, -offer.Price+offer.MoneyChange)
	} else {
		// Create an Item
		item := Item{
			ID:              fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(10000)),
			Name:            offer.Title,
			BuyPrice:        offer.Price,
			MarketPrice:     offer.Price * 0.7, // Resale value is 70% of buy price
			BoughtAt:        gs.CurrentDate,
			HealthChange:    offer.HealthChange,
			EnergyChange:    offer.EnergyChange,
			ReputationChange: offer.ReputationChange,
			MoneyChange:     offer.MoneyChange,
			EffectFrequency: "on_use", // Items typically have effects on use, but can be daily
		}
		gs.Inventory = append(gs.Inventory, item)
		
		eventMsg := "Purchased item: " + offer.Title
		gs.addEvent("item_purchased", eventMsg, -offer.Price+offer.MoneyChange)
	}
	
	// Remove offer
	gs.ActiveOffers = append(gs.ActiveOffers[:offerIndex], gs.ActiveOffers[offerIndex+1:]...)
	
	// Build event message for immediate effects
	if offer.HealthChange != 0 || offer.EnergyChange != 0 || offer.ReputationChange != 0 || offer.MoneyChange != 0 {
		var statChanges []string
		if offer.HealthChange != 0 {
			statChanges = append(statChanges, fmt.Sprintf("Health: %+d", offer.HealthChange))
		}
		if offer.EnergyChange != 0 {
			statChanges = append(statChanges, fmt.Sprintf("Energy: %+d", offer.EnergyChange))
		}
		if offer.ReputationChange != 0 {
			statChanges = append(statChanges, fmt.Sprintf("Reputation: %+d", offer.ReputationChange))
		}
		if offer.MoneyChange != 0 {
			statChanges = append(statChanges, fmt.Sprintf("Money: %+.2f€", offer.MoneyChange))
		}
		if len(statChanges) > 0 {
			eventMsg := "Immediate effects: " + strings.Join(statChanges, ", ")
			gs.addEvent("offer_effects", eventMsg, 0)
		}
	}
	
	if offer.IsTrickery {
		gs.addEvent("trickery_warning", "⚠️ This was a trickery offer!", 0)
	}
	
	return nil
}

// NextDay advances the game to the next day
func (gs *GameState) NextDay() {
	gs.AdvanceTime(24 * time.Hour)
	gs.addEvent("day_advanced", "Date: "+gs.CurrentDate.Format("2006-01-02"), 0)
}

// AdvanceTime advances the game time by specified duration
func (gs *GameState) AdvanceTime(duration time.Duration) {
	// Check for game over first
	if gs.GameOver {
		return // Don't advance time if game is over
	}
	
	gs.CurrentDate = gs.CurrentDate.Add(duration)
	
	// Check for game over condition (negative money for > 1 month)
	gs.checkGameOver()
	if gs.GameOver {
		return
	}
	
	// Check if health dropped below 0 and admit to hospital if needed
	if gs.Health < 0 && !gs.IsInHospital {
		gs.admitToHospital()
	}
	
	// Process hospital stay if in hospital
	if gs.IsInHospital {
		gs.processHospitalStay(duration)
	}
	
	// Check work status (can't work while in hospital)
	if !gs.IsInHospital {
		gs.CheckWorkStatus()
	}
	
	// Process agreements (recurring effects)
	gs.processAgreements(duration)
	
	// Process health/energy changes based on time (only if not in hospital)
	if !gs.IsInHospital {
		hoursPassed := duration.Hours()
		
		// Check if it's night time (00:00 - 07:00)
		isNightTime := gs.IsNightTime()
		
		// Lose health at night if no apartment (2 health per night, once per night period)
		if isNightTime && gs.Apartment == nil {
		// Check if we've already lost health this night period
		// Lose health once per day when entering night (at 00:00)
		currentDate := gs.CurrentDate.Format("2006-01-02")
		lastLossDate := gs.LastNightHealthLossDate.Format("2006-01-02")
		
		// Check if we crossed into 00:00 (new day, new night period)
		previousTime := gs.CurrentDate.Add(-duration)
		previousHour := previousTime.Hour()
		currentHour := gs.CurrentDate.Hour()
		
		// If we just crossed into 00:00 (entered night time) and haven't lost health today
		if currentHour == 0 && previousHour >= 7 && currentDate != lastLossDate {
			gs.Health -= 2
			if gs.Health < 0 {
				gs.Health = 0
			}
			gs.LastNightHealthLossDate = gs.CurrentDate
			gs.addEvent("health_lost_no_apartment", "Lost 2 health - You need an apartment! Sleeping on the street is dangerous.", 0)
		}
	}
	
		// Lose health/energy while working (AI-determined rates per job)
		if gs.IsWorking && gs.Job != nil && gs.Job.HealthLossPerHour > 0 && gs.Job.EnergyLossPerHour > 0 {
			// Calculate loss based on hours passed
			healthLossFloat := hoursPassed * gs.Job.HealthLossPerHour
			energyLossFloat := hoursPassed * gs.Job.EnergyLossPerHour
			
			// For very small time increments, use probabilistic loss to ensure it happens
			healthLoss := int(healthLossFloat)
			energyLoss := int(energyLossFloat)
			
			// Handle fractional loss: if we have fractional part, lose 1 point probabilistically
			healthFraction := healthLossFloat - float64(healthLoss)
			if healthFraction > 0 && rand.Float64() < healthFraction {
				healthLoss++
			}
			// Always lose at least 1 point if we've worked at least 1/60 hour (1 minute) and rate > 0
			if hoursPassed >= 1.0/60.0 && healthLoss == 0 && gs.Job.HealthLossPerHour > 0 {
				healthLoss = 1
			}
			
			energyFraction := energyLossFloat - float64(energyLoss)
			if energyFraction > 0 && rand.Float64() < energyFraction {
				energyLoss++
			}
			// Always lose at least 1 point if we've worked at least 1/60 hour (1 minute) and rate > 0
			if hoursPassed >= 1.0/60.0 && energyLoss == 0 && gs.Job.EnergyLossPerHour > 0 {
				energyLoss = 1
			}
			
			gs.Health -= healthLoss
			if gs.Health < 0 {
				gs.Health = 0
			}
			
			gs.Energy -= energyLoss
			if gs.Energy < 0 {
				gs.Energy = 0
			}
		}
		
		// Gain health/energy while in apartment (if not working)
		if gs.Apartment != nil && !gs.IsWorking {
			healthGain := int(hoursPassed * float64(gs.Apartment.HealthGain))
			energyGain := int(hoursPassed * float64(gs.Apartment.EnergyGain))
			
			gs.Health += healthGain
			if gs.Health > 100 {
				gs.Health = 100
			}
			
			gs.Energy += energyGain
			if gs.Energy > 100 {
				gs.Energy = 100
			}
		}
		
		// Update stock and crypto prices (daily volatility) - check if a full day has passed
		// We need to track the last update day
		lastUpdateDay := gs.CurrentDate.Add(-duration).Day()
		currentDay := gs.CurrentDate.Day()
		
		// If we crossed a day boundary, update prices
		if currentDay != lastUpdateDay || duration >= 24*time.Hour {
			for i := range gs.Stocks {
				change := (rand.Float64() - 0.5) * 0.1
				oldPrice := gs.Stocks[i].CurrentPrice
				gs.Stocks[i].CurrentPrice = gs.Stocks[i].BuyPrice * (1 + change)
				
				// Add to history if significant change (5% or more)
				if abs(oldPrice - gs.Stocks[i].CurrentPrice) > oldPrice * 0.05 {
					event := "surge"
					if gs.Stocks[i].CurrentPrice < oldPrice {
						event = "crash"
					}
					gs.StockHistory = append(gs.StockHistory, StockHistory{
						Symbol: gs.Stocks[i].Symbol,
						Price:  gs.Stocks[i].CurrentPrice,
						Date:   gs.CurrentDate,
						Event:  event,
					})
				}
			}
			for i := range gs.Crypto {
				change := (rand.Float64() - 0.5) * 0.15
				gs.Crypto[i].CurrentPrice = gs.Crypto[i].BuyPrice * (1 + change)
			}
		}
	} // End of "if !gs.IsInHospital" block
	
	// Remove expired offers
	gs.removeExpiredOffers()
}

// removeExpiredOffers removes expired job offers, apartment offers, stock offers, and regular offers
func (gs *GameState) removeExpiredOffers() {
	// Remove expired job offers
	validJobOffers := []JobOffer{}
	for _, offer := range gs.JobOffers {
		if gs.CurrentDate.Before(offer.ExpiresAt) {
			validJobOffers = append(validJobOffers, offer)
		}
	}
	gs.JobOffers = validJobOffers
	
	// Remove expired apartment offers
	validApartmentOffers := []ApartmentOffer{}
	for _, offer := range gs.ApartmentOffers {
		if gs.CurrentDate.Before(offer.ExpiresAt) {
			validApartmentOffers = append(validApartmentOffers, offer)
		}
	}
	gs.ApartmentOffers = validApartmentOffers
	
	// Remove expired stock offers
	validStockOffers := []StockOffer{}
	for _, offer := range gs.StockOffers {
		if gs.CurrentDate.Before(offer.ExpiresAt) {
			validStockOffers = append(validStockOffers, offer)
		}
	}
	gs.StockOffers = validStockOffers
	
	// Remove expired regular offers
	validOffers := []Offer{}
	for _, offer := range gs.ActiveOffers {
		if gs.CurrentDate.Before(offer.ExpiresAt) {
			validOffers = append(validOffers, offer)
		}
	}
	gs.ActiveOffers = validOffers
}

// admitToHospital automatically admits player to hospital when health drops below 0
func (gs *GameState) admitToHospital() {
	gs.IsInHospital = true
	gs.HospitalEntryTime = gs.CurrentDate
	gs.IsWorking = false // Can't work while in hospital
	gs.addEvent("hospital_admission", "⚠️ Health critical! Admitted to hospital. Cost: €100/hour. You'll be released when health reaches 20.", 0)
}

// processHospitalStay processes the hospital stay: recovers health, charges money, releases when health >= 20
func (gs *GameState) processHospitalStay(duration time.Duration) {
	hoursPassed := duration.Hours()
	
	// Charge hospital fees: €100 per hour
	totalCost := hoursPassed * 100.0
	gs.Money -= totalCost
	
	// Recover health: +1 per hour
	healthRecovery := int(hoursPassed)
	gs.Health += healthRecovery
	
	// Check if health reached 20 or above (release condition)
	if gs.Health >= 20 {
		gs.Health = 20 // Cap at 20 for release
		daysInHospital := gs.CurrentDate.Sub(gs.HospitalEntryTime).Hours() / 24
		totalHospitalCost := daysInHospital * 24 * 100.0
		gs.addEvent("hospital_release", fmt.Sprintf("Released from hospital after %.1f days. Total cost: €%.2f", daysInHospital, totalHospitalCost), -totalHospitalCost)
		gs.IsInHospital = false
		gs.HospitalEntryTime = time.Time{}
	} else {
		// Still in hospital - add event for hourly charges if significant time passed
		if hoursPassed >= 1.0 {
			gs.addEvent("hospital_stay", fmt.Sprintf("Hospital stay: +%d health, -€%.2f (Health: %d/100)", healthRecovery, totalCost, gs.Health), -totalCost)
		}
	}
	
	// Ensure health doesn't go above 100
	if gs.Health > 100 {
		gs.Health = 100
	}
}

// checkGameOver checks if the game should end (negative money for > 1 month)
func (gs *GameState) checkGameOver() {
	if gs.Money < 0 {
		// Money is negative
		if gs.NegativeMoneyStartDate.IsZero() {
			// First time going negative - record the date
			gs.NegativeMoneyStartDate = gs.CurrentDate
		} else {
			// Check how long money has been negative
			daysNegative := gs.CurrentDate.Sub(gs.NegativeMoneyStartDate).Hours() / 24
			if daysNegative >= 30 {
				// Game over - negative money for more than 1 month
				gs.GameOver = true
				gs.GameOverReason = fmt.Sprintf("Game Over: You've had negative money for %.0f days (more than 1 month). You couldn't recover financially.", daysNegative)
				gs.addEvent("game_over", gs.GameOverReason, 0)
			}
		}
	} else {
		// Money is positive again - reset the negative money tracking
		if !gs.NegativeMoneyStartDate.IsZero() {
			gs.NegativeMoneyStartDate = time.Time{}
		}
	}
}

// processAgreements processes recurring agreements and applies their effects
func (gs *GameState) processAgreements(duration time.Duration) {
	now := gs.CurrentDate
	
	for i := range gs.Agreements {
		agreement := &gs.Agreements[i]
		shouldProcess := false
		
		switch agreement.RecurrenceType {
		case "daily":
			// Process once per day
			daysSinceLastProcess := now.Sub(agreement.LastProcessedAt).Hours() / 24
			if daysSinceLastProcess >= 1.0 {
				shouldProcess = true
			}
		case "weekly":
			// Process once per week
			daysSinceLastProcess := now.Sub(agreement.LastProcessedAt).Hours() / 24
			if daysSinceLastProcess >= 7.0 {
				shouldProcess = true
			}
		case "monthly":
			// Process once per month (on the same day of month)
			lastDay := agreement.LastProcessedAt.Day()
			currentDay := now.Day()
			lastMonth := agreement.LastProcessedAt.Month()
			currentMonth := now.Month()
			lastYear := agreement.LastProcessedAt.Year()
			currentYear := now.Year()
			
			// Check if we've crossed a month boundary and it's the same day or later
			if (currentYear > lastYear) || 
			   (currentYear == lastYear && currentMonth > lastMonth) ||
			   (currentYear == lastYear && currentMonth == lastMonth && currentDay >= lastDay && now.Sub(agreement.LastProcessedAt).Hours() >= 24*24) {
				shouldProcess = true
			}
		default:
			// Default to monthly
			lastDay := agreement.LastProcessedAt.Day()
			currentDay := now.Day()
			lastMonth := agreement.LastProcessedAt.Month()
			currentMonth := now.Month()
			lastYear := agreement.LastProcessedAt.Year()
			currentYear := now.Year()
			
			if (currentYear > lastYear) || 
			   (currentYear == lastYear && currentMonth > lastMonth) ||
			   (currentYear == lastYear && currentMonth == lastMonth && currentDay >= lastDay && now.Sub(agreement.LastProcessedAt).Hours() >= 24*24) {
				shouldProcess = true
			}
		}
		
		if shouldProcess {
			// Apply agreement effects
			gs.Health += agreement.HealthChange
			if gs.Health > 100 {
				gs.Health = 100
			}
			if gs.Health < 0 {
				gs.Health = 0
			}
			
			gs.Energy += agreement.EnergyChange
			if gs.Energy > 100 {
				gs.Energy = 100
			}
			if gs.Energy < 0 {
				gs.Energy = 0
			}
			
			gs.Reputation += agreement.ReputationChange
			
			gs.Money += agreement.MoneyChange
			
			// Build event message
			var statChanges []string
			if agreement.HealthChange != 0 {
				statChanges = append(statChanges, fmt.Sprintf("Health: %+d", agreement.HealthChange))
			}
			if agreement.EnergyChange != 0 {
				statChanges = append(statChanges, fmt.Sprintf("Energy: %+d", agreement.EnergyChange))
			}
			if agreement.ReputationChange != 0 {
				statChanges = append(statChanges, fmt.Sprintf("Reputation: %+d", agreement.ReputationChange))
			}
			if agreement.MoneyChange != 0 {
				statChanges = append(statChanges, fmt.Sprintf("Money: %+.2f€", agreement.MoneyChange))
			}
			
			eventMsg := fmt.Sprintf("Agreement: %s (%s)", agreement.Title, agreement.RecurrenceType)
			if len(statChanges) > 0 {
				eventMsg += " - " + strings.Join(statChanges, ", ")
			}
			
			gs.addEvent("agreement_processed", eventMsg, agreement.MoneyChange)
			agreement.LastProcessedAt = now
		}
	}
}

// QuitAgreement cancels an active agreement (may have early termination penalty)
func (gs *GameState) QuitAgreement(agreementID string) error {
	if !gs.CanPerformAction() {
		return &GameError{Message: "You are currently working and cannot perform this action"}
	}
	
	agreementIndex := -1
	for i, agreement := range gs.Agreements {
		if agreement.ID == agreementID {
			agreementIndex = i
			break
		}
	}
	
	if agreementIndex == -1 {
		return &GameError{Message: "Agreement not found"}
	}
	
	agreement := gs.Agreements[agreementIndex]
	
	// Calculate early termination penalty based on how long the agreement has been active
	daysActive := gs.CurrentDate.Sub(agreement.StartedAt).Hours() / 24
	penalty := 0.0
	
	// Penalty is higher if cancelled early (within first period)
	switch agreement.RecurrenceType {
	case "daily":
		if daysActive < 1 {
			penalty = 50.0 // 50 EUR penalty for cancelling same day
		} else if daysActive < 7 {
			penalty = 25.0 // 25 EUR penalty for cancelling within first week
		}
	case "weekly":
		if daysActive < 7 {
			penalty = 100.0 // 100 EUR penalty for cancelling within first week
		} else if daysActive < 30 {
			penalty = 50.0 // 50 EUR penalty for cancelling within first month
		}
	case "monthly":
		if daysActive < 30 {
			penalty = 200.0 // 200 EUR penalty for cancelling within first month
		} else if daysActive < 90 {
			penalty = 100.0 // 100 EUR penalty for cancelling within first 3 months
		}
	}
	
	// Apply penalty if any
	if penalty > 0 {
		if gs.Money < penalty {
			return &GameError{Message: fmt.Sprintf("Not enough money to pay early termination penalty. Need €%.2f", penalty)}
		}
		gs.Money -= penalty
		gs.addEvent("agreement_penalty", fmt.Sprintf("Paid early termination penalty: €%.2f for cancelling %s", penalty, agreement.Title), -penalty)
	}
	
	// Remove agreement
	gs.Agreements = append(gs.Agreements[:agreementIndex], gs.Agreements[agreementIndex+1:]...)
	
	eventMsg := fmt.Sprintf("Cancelled agreement: %s", agreement.Title)
	if penalty > 0 {
		eventMsg += fmt.Sprintf(" (Early termination penalty: €%.2f)", penalty)
	}
	gs.addEvent("agreement_cancelled", eventMsg, -penalty)
	return nil
}

// CanPerformAction checks if player can perform actions (not working)
func (gs *GameState) CanPerformAction() bool {
	return !gs.IsWorking
}

// addEvent adds an event to history
func (gs *GameState) addEvent(eventType, message string, amount float64) {
	event := Event{
		ID:        generateID(),
		Type:      eventType,
		Message:   message,
		Amount:    amount,
		Timestamp: time.Now(),
	}
	gs.History = append(gs.History, event)
}

// GameError represents a game error
type GameError struct {
	Message string
}

func (e *GameError) Error() string {
	return e.Message
}

// Helper functions
func formatMoney(amount float64) string {
	return formatFloat(amount)
}

func formatFloat(f float64) string {
	return formatFloatPrecision(f, 2)
}

func formatFloatPrecision(f float64, prec int) string {
	// Simple formatting
	multiplier := 1.0
	for i := 0; i < prec; i++ {
		multiplier *= 10
	}
	rounded := int(f * multiplier)
	whole := rounded / int(multiplier)
	decimal := rounded % int(multiplier)
	if decimal < 0 {
		decimal = -decimal
	}
	return formatInt(whole) + "." + formatIntWithPadding(decimal, prec)
}

func formatInt(i int) string {
	if i == 0 {
		return "0"
	}
	if i < 0 {
		return "-" + formatInt(-i)
	}
	result := ""
	for i > 0 {
		result = string(rune('0' + i%10)) + result
		i /= 10
	}
	return result
}

func formatIntWithPadding(i int, width int) string {
	str := formatInt(i)
	for len(str) < width {
		str = "0" + str
	}
	return str
}

func generateID() string {
	return time.Now().Format("20060102150405") + formatInt(rand.Intn(10000))
}

