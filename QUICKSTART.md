# Quick Start Guide

## Prerequisites
- Go 1.21 or higher installed
- OpenAI API key (from BasicsHackathon.md or your own)

## Setup Steps

1. **Install Dependencies**
   ```bash
   go mod download
   ```

2. **Set Environment Variable**
   
   Option 1: Create a `.env` file:
   ```
   OPENAI_API_KEY=your_api_key_here
   PORT=8755
   ```
   
   Option 2: Set environment variable directly:
   - Windows PowerShell: `$env:OPENAI_API_KEY="your_api_key_here"`
   - Windows CMD: `set OPENAI_API_KEY=your_api_key_here`
   - Linux/Mac: `export OPENAI_API_KEY="your_api_key_here"`

3. **Run the Server**
   ```bash
   go run .
   ```

4. **Open Browser**
   Navigate to: `http://localhost:8755`

## How to Play

1. **Start**: You begin with $10,000
2. **Find a Job**: Click "Find Job" to get a random job
3. **Work**: Click "Work" to earn your daily salary
4. **Invest**: 
   - Buy/sell stocks (select symbol and shares)
   - Buy/sell crypto (select symbol and amount)
5. **Market**: 
   - Buy items from the market
   - Sell items from your inventory
6. **AI Offers**: 
   - Click "Get Trickery Offer" to receive a potentially deceptive offer
   - Click "Get Good Offer" to receive a legitimate offer
   - Accept offers to test your financial literacy
7. **Chat with Guide**: Ask the guide agent questions about your financial decisions
8. **Next Day**: Advance time to see market changes

## Tips

- Watch out for trickery offers - they seem good but are actually scams!
- The guide agent can help you think through decisions
- Check the history tab to review your past actions
- Stock and crypto prices change each day

## Troubleshooting

- **API Key Error**: Make sure your OpenAI API key is set correctly
- **Port Already in Use**: Change PORT in .env or set environment variable (default is 8755)
- **CORS Issues**: The server serves static files from ./web/ directory

