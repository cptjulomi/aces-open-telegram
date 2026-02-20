package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	stdhttp "net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	jsonrepair "github.com/RealAlexandreAI/json-repair"
	http "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"
)

// Telegram Configuration
const (
	TELEGRAM_BOT_TOKEN = "7254036271:AAGLUoSDDgIFaAEV2NPGwe6Fj5lqnIKj5dE"
	NEW_BETS_CHANNEL   = "-1002675079062"
	LOG_CHANNEL        = "-5053088058"
)

var URL_BASE = "https://www.winamax.fr"
var SocketURL = "https://sports-eu-west-3.winamax.fr"

var SpanishPage = "/apuestas-deportivas/sports/5"
var FrenchPage = "/paris-sportifs/sports/5"

// Files for comparison
const CURRENT_FILE = "winamax_aces.json"
const PAST_FILE = "winamax_aces_past.json"

type Option struct {
	Seuil float64 `json:"seuil"`
	Cote  float64 `json:"cote"`
}

type Bet struct {
	Type    string   `json:"type"`
	Cut     float64  `json:"cut,omitempty"`
	Plus    float64  `json:"plus,omitempty"`
	Moins   float64  `json:"moins,omitempty"`
	Options []Option `json:"options,omitempty"`
}

type Match struct {
	Joueurs string `json:"joueurs"`
	Lien    string `json:"lien"`
}

type MatchData struct {
	Match Match `json:"match"`
	Bet   []Bet `json:"bet"`
}

// Send message to Telegram using standard HTTP client
func sendTelegramMessage(chatID, message string) error {
	telegramURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", TELEGRAM_BOT_TOKEN)

	data := url.Values{}
	data.Set("chat_id", chatID)
	data.Set("text", message)
	data.Set("parse_mode", "HTML")
	data.Set("disable_web_page_preview", "true")

	// Use standard net/http client for Telegram (not the TLS client)
	client := &stdhttp.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(telegramURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram error: %s", string(body))
	}
	return nil
}

// Send log message
func sendLog(message string) {
	timestamp := time.Now().Format("15:04:05")
	logMsg := fmt.Sprintf("🔔 [%s] %s", timestamp, message)
	if err := sendTelegramMessage(LOG_CHANNEL, logMsg); err != nil {
		log.Println("Failed to send log:", err)
	}
}

// Generate bet key for comparison - uses only bet type name to avoid duplicates from incomplete scraping
func generateBetKey(bet Bet) string {
	// Use only the bet type name as key to avoid issues when scraping returns incomplete data
	// (e.g., sometimes only 5 options instead of 55)
	return bet.Type
}

// Generate a signature of the bet including cotes for comparison
func generateBetSignature(bet Bet) string {
	if len(bet.Options) > 0 {
		// For dynamic bets, include all thresholds and cotes
		var parts []string
		for _, opt := range bet.Options {
			parts = append(parts, fmt.Sprintf("%.0f@%.2f", opt.Seuil, opt.Cote))
		}
		return fmt.Sprintf("%s|%s", bet.Type, strings.Join(parts, ","))
	}
	return fmt.Sprintf("%s|%.1f|%.2f|%.2f", bet.Type, bet.Cut, bet.Plus, bet.Moins)
}

// Check if two bets are effectively the same (same type and similar options)
func betsAreEqual(prev, curr Bet) bool {
	// Different types are never equal
	if prev.Type != curr.Type {
		return false
	}

	// For dynamic bets (paliers), check if the options overlap significantly
	if len(prev.Options) > 0 && len(curr.Options) > 0 {
		// Build a map of previous options
		prevOpts := make(map[float64]float64) // seuil -> cote
		for _, opt := range prev.Options {
			prevOpts[opt.Seuil] = opt.Cote
		}

		// Check if all current options exist in previous with same cotes
		allMatch := true
		for _, opt := range curr.Options {
			if prevCote, exists := prevOpts[opt.Seuil]; !exists || prevCote != opt.Cote {
				allMatch = false
				break
			}
		}
		return allMatch
	}

	// For plus/moins bets, check cut and cotes
	if prev.Cut != 0 || curr.Cut != 0 {
		return prev.Cut == curr.Cut && prev.Plus == curr.Plus && prev.Moins == curr.Moins
	}

	return true
}

// Load previous data from winamax_aces_past.json
func loadPreviousData() []MatchData {
	data, err := os.ReadFile("winamax_aces_past.json")
	if err != nil {
		// File doesn't exist yet, return empty
		return nil
	}

	var previousData []MatchData
	if err := json.Unmarshal(data, &previousData); err != nil {
		log.Println("Error parsing winamax_aces_past.json:", err)
		return nil
	}
	return previousData
}

// Notification history file to track sent notifications
const NOTIFICATION_HISTORY_FILE = "sent_notifications.txt"
const NOTIFICATION_EXPIRY_DAYS = 7 // Entries older than this are removed

// Load notification history - returns map of joueurs -> timestamp
// Key is now joueurs (player names) to avoid issues with incorrect/changing links
func loadNotificationHistory() map[string]time.Time {
	history := make(map[string]time.Time)

	data, err := os.ReadFile(NOTIFICATION_HISTORY_FILE)
	if err != nil {
		return history // File doesn't exist yet
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: timestamp|joueurs|matchLink
		parts := strings.SplitN(line, "|", 3)
		if len(parts) >= 2 {
			timestamp, err := time.Parse(time.RFC3339, parts[0])
			if err != nil {
				continue
			}
			joueurs := parts[1] // Key is now joueurs
			history[joueurs] = timestamp
		}
	}
	return history
}

// Save notification history with joueurs and link - cleans up old entries
// Format: timestamp|joueurs|matchLink (joueurs is the key, link is for reference)
func saveNotificationHistoryWithLink(history map[string]time.Time, joueursToLink map[string]string) {
	var lines []string
	now := time.Now()

	for joueurs, timestamp := range history {
		// Skip entries older than NOTIFICATION_EXPIRY_DAYS
		if now.Sub(timestamp).Hours() > float64(NOTIFICATION_EXPIRY_DAYS*24) {
			continue
		}
		link := joueursToLink[joueurs]
		if link == "" {
			link = "unknown"
		}
		lines = append(lines, fmt.Sprintf("%s|%s|%s", timestamp.Format(time.RFC3339), joueurs, link))
	}

	if err := os.WriteFile(NOTIFICATION_HISTORY_FILE, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		log.Println("Error writing notification history:", err)
	}
}

// Check if notification was already sent for this match (by joueurs)
func wasNotificationSent(joueurs string) bool {
	history := loadNotificationHistory()
	if timestamp, exists := history[joueurs]; exists {
		// Check if entry is still valid (not expired)
		if time.Since(timestamp).Hours() <= float64(NOTIFICATION_EXPIRY_DAYS*24) {
			return true
		}
	}
	return false
}

// Load joueurs to link mapping from existing file
func loadJoueursToLinkMapping() map[string]string {
	mapping := make(map[string]string)
	data, err := os.ReadFile(NOTIFICATION_HISTORY_FILE)
	if err != nil {
		return mapping
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) >= 3 {
			joueurs := parts[1]
			link := parts[2]
			mapping[joueurs] = link
		}
	}
	return mapping
}

// Mark notification as sent for this match (by joueurs, with link for reference)
func markNotificationSent(joueurs, matchLink string) {
	history := loadNotificationHistory()
	joueursToLink := loadJoueursToLinkMapping()
	history[joueurs] = time.Now()
	joueursToLink[joueurs] = matchLink
	saveNotificationHistoryWithLink(history, joueursToLink)
}

// Build a map of matchLink -> betType -> Bet for quick lookup
func buildBetMap(data []MatchData) map[string]map[string]Bet {
	result := make(map[string]map[string]Bet)
	for _, matchData := range data {
		if result[matchData.Match.Lien] == nil {
			result[matchData.Match.Lien] = make(map[string]Bet)
		}
		for _, bet := range matchData.Bet {
			// Use bet type as key
			result[matchData.Match.Lien][bet.Type] = bet
		}
	}
	return result
}

// Compare current data with previous and send notifications for new bets
// ONLY notifies for FIRST APPEARANCE of a bet type - ignores cote/cut changes
// Uses notification history to avoid duplicate notifications for same match
func compareAndNotify(currentData []MatchData) {
	previousData := loadPreviousData()
	previousMap := buildBetMap(previousData)

	for _, matchData := range currentData {
		// Skip if we already sent notification for this match (check by joueurs to avoid link issues)
		if wasNotificationSent(matchData.Match.Joueurs) {
			continue
		}

		var newBets []Bet

		previousMatchBets := previousMap[matchData.Match.Lien]
		if previousMatchBets == nil {
			// Entire match is new - all bets are new
			newBets = matchData.Bet
		} else {
			// Check each bet - only notify for NEW bet types
			for _, bet := range matchData.Bet {
				_, exists := previousMatchBets[bet.Type]
				if !exists {
					// Completely new bet type - first appearance only
					newBets = append(newBets, bet)
				}
				// If bet type already exists, ignore any changes in cotes/cut
			}
		}

		// Send notification if there are new bets
		if len(newBets) > 0 {
			notifyNewBetsGrouped(matchData.Match, newBets)
			// Mark this match as notified (by joueurs, with link for reference)
			markNotificationSent(matchData.Match.Joueurs, matchData.Match.Lien)
		}
	}
}

// Save current data as past for next comparison
func saveAsPast() {
	// Copy winamax_aces.json to winamax_aces_past.json
	data, err := os.ReadFile("winamax_aces.json")
	if err != nil {
		log.Println("Error reading winamax_aces.json for copy:", err)
		return
	}
	if err := os.WriteFile("winamax_aces_past.json", data, 0644); err != nil {
		log.Println("Error writing winamax_aces_past.json:", err)
	}
}

// Send ONE grouped notification for all new bets in a match
// Order: 1) Plus/Moins individual players, 2) Plus/Moins match total, 3) Paliers individual players, 4) Paliers match total
func notifyNewBetsGrouped(match Match, bets []Bet) {
	var message strings.Builder

	// Add match title (player names) as header so users know which match even if link changes
	message.WriteString(fmt.Sprintf("🎾 <b>%s</b>\n\n", match.Joueurs))

	// 1. Plus/Moins for individual players (contain "de" but not "(paliers)")
	for _, bet := range bets {
		if len(bet.Options) == 0 && strings.Contains(bet.Type, " de ") {
			message.WriteString(fmt.Sprintf("<b>%s</b>  + %.1f @ %.2f / - %.1f @ %.2f\n\n",
				bet.Type, bet.Cut, bet.Plus, bet.Cut, bet.Moins))
		}
	}

	// 2. Plus/Moins match total (no "de" and no "(paliers)")
	for _, bet := range bets {
		if len(bet.Options) == 0 && !strings.Contains(bet.Type, " de ") {
			message.WriteString(fmt.Sprintf("<b>%s</b>  + %.1f @ %.2f / - %.1f @ %.2f\n\n",
				bet.Type, bet.Cut, bet.Plus, bet.Cut, bet.Moins))
		}
	}

	// 3. Paliers for individual players (contain " - " with player name)
	for _, bet := range bets {
		if len(bet.Options) > 0 && strings.Contains(bet.Type, " - ") && !strings.HasSuffix(bet.Type, "(paliers)") {
			message.WriteString(fmt.Sprintf("<b>%s</b> :\n", bet.Type))
			for _, opt := range bet.Options {
				message.WriteString(fmt.Sprintf("%.0f @ %.2f\n", opt.Seuil, opt.Cote))
			}
			message.WriteString("\n")
		}
	}

	// 4. Paliers match total (ends with just "(paliers)" without player name dash)
	for _, bet := range bets {
		if len(bet.Options) > 0 && !strings.Contains(bet.Type, " - ") {
			message.WriteString(fmt.Sprintf("<b>%s</b> :\n", bet.Type))
			for _, opt := range bet.Options {
				message.WriteString(fmt.Sprintf("%.0f @ %.2f\n", opt.Seuil, opt.Cote))
			}
			message.WriteString("\n")
		}
	}

	// Add link at the end
	message.WriteString(fmt.Sprintf("🔗 <a href=\"%s\">LIEN</a>", match.Lien))

	if err := sendTelegramMessage(NEW_BETS_CHANNEL, message.String()); err != nil {
		log.Println("Failed to send new bet notification:", err)
	} else {
		log.Printf("Notification sent for new bets: %s (%d bets)\n", match.Joueurs, len(bets))
	}
}

// Sort bets in correct order:
// 1. Plus/Moins Player 1
// 2. Plus/Moins Player 2
// 3. Plus/Moins Match Total
// 4. Paliers Player 1
// 5. Paliers Player 2
// 6. Paliers Match Total
func sortBets(bets []Bet, matchTitle string) []Bet {
	// Extract player names from match title (format: "Player1 - Player2")
	players := strings.Split(matchTitle, " - ")
	player1Initial := ""
	player2Initial := ""

	if len(players) >= 2 {
		// Get last name initial for matching (e.g., "F. Auger-Aliassime" -> "F. Auger-Aliassime")
		player1Parts := strings.Fields(players[0])
		player2Parts := strings.Fields(players[1])

		if len(player1Parts) > 0 {
			// Get initial letter of first name + last name initial
			player1Initial = string(player1Parts[0][0]) + "."
		}
		if len(player2Parts) > 0 {
			player2Initial = string(player2Parts[0][0]) + "."
		}
	}

	// Categorize bets
	var plusMoinsPlayer1, plusMoinsPlayer2, plusMoinsMatch []Bet
	var paliersPlayer1, paliersPlayer2, paliersMatch []Bet

	for _, bet := range bets {
		isPaliers := len(bet.Options) > 0
		isPlayer1 := strings.Contains(bet.Type, player1Initial) || containsPlayerName(bet.Type, players, 0)
		isPlayer2 := strings.Contains(bet.Type, player2Initial) || containsPlayerName(bet.Type, players, 1)

		if isPaliers {
			if isPlayer1 && !isPlayer2 {
				paliersPlayer1 = append(paliersPlayer1, bet)
			} else if isPlayer2 && !isPlayer1 {
				paliersPlayer2 = append(paliersPlayer2, bet)
			} else {
				paliersMatch = append(paliersMatch, bet)
			}
		} else {
			if isPlayer1 && !isPlayer2 {
				plusMoinsPlayer1 = append(plusMoinsPlayer1, bet)
			} else if isPlayer2 && !isPlayer1 {
				plusMoinsPlayer2 = append(plusMoinsPlayer2, bet)
			} else {
				plusMoinsMatch = append(plusMoinsMatch, bet)
			}
		}
	}

	// Combine in correct order
	var result []Bet
	result = append(result, plusMoinsPlayer1...)
	result = append(result, plusMoinsPlayer2...)
	result = append(result, plusMoinsMatch...)
	result = append(result, paliersPlayer1...)
	result = append(result, paliersPlayer2...)
	result = append(result, paliersMatch...)

	return result
}

// Helper function to check if bet type contains a player name
func containsPlayerName(betType string, players []string, playerIndex int) bool {
	if playerIndex >= len(players) {
		return false
	}

	playerName := players[playerIndex]
	// Check for last name
	parts := strings.Fields(playerName)
	if len(parts) > 0 {
		lastName := parts[len(parts)-1]
		if strings.Contains(betType, lastName) {
			return true
		}
	}
	return false
}

func getInitialCookies(client tls_client.HttpClient) error {
	url := URL_BASE + FrenchPage
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	hostName := extractHostname(URL_BASE)
	req.Header = http.Header{
		"Host":                      {hostName},
		"Sec-Ch-Ua":                 {`"Google Chrome";v="137", "Chromium";v="137", "Not/A)Brand";v="24"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"},
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"fr-FR,fr;q=0.9"},
		http.HeaderOrderKey: {
			"Host",
			"Sec-Ch-Ua",
			"Sec-Ch-Ua-Mobile",
			"Sec-Ch-Ua-Platform",
			"Upgrade-Insecure-Requests",
			"User-Agent",
			"Accept",
			"Sec-Fetch-Site",
			"Sec-Fetch-Mode",
			"Sec-Fetch-User",
			"Sec-Fetch-Dest",
			"Accept-Encoding",
			"Accept-Language",
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	fmt.Println("Initial GET status code:", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("initial request failed with status: %d", resp.StatusCode)
	}
	return nil
}

func getSessionID(client tls_client.HttpClient) (string, error) {
	t := fmt.Sprintf("%d", time.Now().UnixNano()/1e6)
	url := fmt.Sprintf("%s/uof-sports-server/socket.io/?language=FR&version=3.15.1&embed=false&EIO=3&transport=polling&t=%s", SocketURL, t)
	req, _ := http.NewRequest(http.MethodGet, url, nil)

	hostName := extractHostname(URL_BASE)
	req.Header = http.Header{
		"Host":                      {hostName},
		"Sec-Ch-Ua":                 {`"Google Chrome";v="137", "Chromium";v="137", "Not/A)Brand";v="24"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"},
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"fr-FR,fr;q=0.9"},
		http.HeaderOrderKey: {
			"Host",
			"Sec-Ch-Ua",
			"Sec-Ch-Ua-Mobile",
			"Sec-Ch-Ua-Platform",
			"Upgrade-Insecure-Requests",
			"User-Agent",
			"Accept",
			"Sec-Fetch-Site",
			"Sec-Fetch-Mode",
			"Sec-Fetch-User",
			"Sec-Fetch-Dest",
			"Accept-Encoding",
			"Accept-Language",
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	re := regexp.MustCompile(`\d+:\d+({.*})`)
	match := re.FindStringSubmatch(string(body))
	if len(match) < 2 {
		return "", fmt.Errorf("SID JSON not found in response")
	}

	var sidData struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(strings.NewReader(match[1])).Decode(&sidData); err != nil {
		return "", fmt.Errorf("failed to decode SID JSON: %w", err)
	}

	fmt.Println("Extracted SID:", sidData.SID)
	return sidData.SID, nil
}

func getInitialDataWithSID(client tls_client.HttpClient, sid string) ([]byte, error) {
	t := fmt.Sprintf("%d", time.Now().UnixNano()/1e6)
	url := fmt.Sprintf("%s/uof-sports-server/socket.io/?language=FR&version=3.15.1&embed=false&EIO=3&transport=polling&t=%s&sid=%s", SocketURL, t, sid)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	hostName := extractHostname(URL_BASE)
	req.Header = http.Header{
		"Host":                      {hostName},
		"Sec-Ch-Ua":                 {`"Google Chrome";v="137", "Chromium";v="137", "Not/A)Brand";v="24"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"},
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"fr-FR,fr;q=0.9"},
		http.HeaderOrderKey: {
			"Host",
			"Sec-Ch-Ua",
			"Sec-Ch-Ua-Mobile",
			"Sec-Ch-Ua-Platform",
			"Upgrade-Insecure-Requests",
			"User-Agent",
			"Accept",
			"Sec-Fetch-Site",
			"Sec-Fetch-Mode",
			"Sec-Fetch-User",
			"Sec-Fetch-Dest",
			"Accept-Encoding",
			"Accept-Language",
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

func postSubscription(client tls_client.HttpClient, sid, payload string) error {
	t := fmt.Sprintf("%d", time.Now().UnixNano()/1e6)
	url := fmt.Sprintf("%s/uof-sports-server/socket.io/?language=FR&version=3.15.1&embed=false&EIO=3&transport=polling&t=%s&sid=%s", SocketURL, t, sid)

	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return err
	}

	hostName := extractHostname(URL_BASE)
	req.Header = http.Header{
		"Host":                      {hostName},
		"Sec-Ch-Ua":                 {`"Google Chrome";v="137", "Chromium";v="137", "Not/A)Brand";v="24"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"},
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"en-US,fr;q=0.9"},
		http.HeaderOrderKey: {
			"Host",
			"Sec-Ch-Ua",
			"Sec-Ch-Ua-Mobile",
			"Sec-Ch-Ua-Platform",
			"Upgrade-Insecure-Requests",
			"User-Agent",
			"Accept",
			"Sec-Fetch-Site",
			"Sec-Fetch-Mode",
			"Sec-Fetch-User",
			"Sec-Fetch-Dest",
			"Accept-Encoding",
			"Accept-Language",
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println("POST subscription body:", string(body))
	if !strings.Contains(string(body), "ok") {
		if !strings.Contains(string(body), "matches") {
			return fmt.Errorf("POST subscription failed")
		}
	}
	return nil
}

func getFinalData(client tls_client.HttpClient, sid string) ([]byte, error) {
	t := fmt.Sprintf("%d", time.Now().UnixNano()/1e6)
	url := fmt.Sprintf("%s/uof-sports-server/socket.io/?language=FR&version=3.15.1&embed=false&EIO=3&transport=polling&t=%s&sid=%s", SocketURL, t, sid)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	hostName := extractHostname(URL_BASE)
	req.Header = http.Header{
		"Host":                      {hostName},
		"Sec-Ch-Ua":                 {`"Google Chrome";v="137", "Chromium";v="137", "Not/A)Brand";v="24"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Windows"`},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"},
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-User":            {"?1"},
		"Sec-Fetch-Dest":            {"document"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"fr-FR,fr;q=0.9"},
		http.HeaderOrderKey: {
			"Host",
			"Sec-Ch-Ua",
			"Sec-Ch-Ua-Mobile",
			"Sec-Ch-Ua-Platform",
			"Upgrade-Insecure-Requests",
			"User-Agent",
			"Accept",
			"Sec-Fetch-Site",
			"Sec-Fetch-Mode",
			"Sec-Fetch-User",
			"Sec-Fetch-Dest",
			"Accept-Encoding",
			"Accept-Language",
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return bodyBytes, nil
}

// Load all proxies from file and convert to URL format
func loadProxies(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Println("Error reading proxy file:", err)
		return nil
	}

	// Normalize line endings and split
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	var proxies []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Format: host:port:user:pass -> http://user:pass@host:port
		parts := strings.Split(line, ":")
		if len(parts) >= 4 {
			host := strings.TrimSpace(parts[0])
			port := strings.TrimSpace(parts[1])
			user := strings.TrimSpace(parts[2])
			pass := strings.TrimSpace(strings.Join(parts[3:], ":")) // In case password contains ":"
			proxyURL := fmt.Sprintf("http://%s:%s@%s:%s", user, pass, host, port)
			proxies = append(proxies, proxyURL)
		} else if strings.HasPrefix(line, "http") {
			// Already in URL format
			proxies = append(proxies, line)
		}
	}

	return proxies
}

// Get a random proxy from the list
func getRandomProxy(proxies []string) string {
	if len(proxies) == 0 {
		return ""
	}
	return proxies[rand.Intn(len(proxies))]
}

// Get short proxy name for logging (just session ID)
func getProxyShortName(proxyURL string) string {
	if strings.Contains(proxyURL, "session-") {
		re := regexp.MustCompile(`session-(\d+)`)
		match := re.FindStringSubmatch(proxyURL)
		if len(match) > 1 {
			return "session-" + match[1]
		}
	}
	return "proxy"
}

// Random sleep between 1 and 5 seconds to humanize requests
func randomSleep() {
	delay := time.Duration(1000+rand.Intn(4000)) * time.Millisecond
	log.Printf("Waiting %.1fs...\n", delay.Seconds())
	time.Sleep(delay)
}

// Check if error is proxy-related (should not count as real failure)
func isProxyError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "Proxy") ||
		strings.Contains(errStr, "proxy") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "Bad Gateway") ||
		strings.Contains(errStr, "Bad Request")
}

func extractHostname(urlStr string) string {
	// Remove protocol
	hostname := urlStr
	if strings.Contains(urlStr, "://") {
		hostname = strings.Split(urlStr, "://")[1]
	}
	// Remove path if any
	if strings.Contains(hostname, "/") {
		hostname = strings.Split(hostname, "/")[0]
	}
	return hostname
}

func main() {
	// Load all proxies
	proxies := loadProxies("proxy.txt")
	if len(proxies) == 0 {
		log.Println("No proxies loaded, running without proxy")
	} else {
		log.Printf("Loaded %d proxies\n", len(proxies))
	}

	sendLog(fmt.Sprintf("🚀 Démarrage scraper - %d proxies chargés", len(proxies)))

	retryCount := 0
	loopCount := 0
	var currentProxy string

	for {
		loopCount++

		// Get a random proxy for this iteration
		currentProxy = getRandomProxy(proxies)
		proxyName := getProxyShortName(currentProxy)

		jar := tls_client.NewCookieJar()
		options := []tls_client.HttpClientOption{
			tls_client.WithTimeoutSeconds(30),
			tls_client.WithClientProfile(profiles.Chrome_133),
			tls_client.WithCookieJar(jar),
		}

		// Add proxy if available
		if currentProxy != "" {
			options = append(options, tls_client.WithProxyUrl(currentProxy))
		}

		client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), options...)
		if err != nil {
			log.Println("Client error:", err)
			retryCount++
			if retryCount >= 3 {
				sendLog("❌ Échec 3 fois de suite, arrêt du scraper")
				log.Fatalln("Failed 3 times in a row, exiting.")
			}
			time.Sleep(10 * time.Second)
			continue
		}

		// get init page session cookies
		if err := getInitialCookies(client); err != nil {
			log.Printf("Failed to get initial cookies (%s): %v\n", proxyName, err)
			if !isProxyError(err) {
				retryCount++
				if retryCount >= 3 {
					sendLog(fmt.Sprintf("❌ Échec cookies [%s]", proxyName))
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
			} else {
				log.Println("⚠️ Proxy error, trying another proxy...")
				sendLog(fmt.Sprintf("⚠️ Erreur proxy [%s] - changement...", proxyName))
			}
			time.Sleep(10 * time.Second)
			continue
		}

		sendLog(fmt.Sprintf("🔄 Cycle #%d démarré [%s]", loopCount, proxyName))

		// get socketio session id
		sid, err := getSessionID(client)
		if err != nil {
			log.Println("Failed to get SID:", err)
			if !isProxyError(err) {
				retryCount++
				if retryCount >= 3 {
					sendLog("❌ Échec SID 3 fois de suite, arrêt du scraper")
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
			} else {
				log.Println("⚠️ Proxy error, trying another proxy...")
				sendLog(fmt.Sprintf("⚠️ Erreur proxy SID [%s]", proxyName))
			}
			time.Sleep(10 * time.Second)
			continue
		}

		// do first request and get 2:40 back
		if _, err := getInitialDataWithSID(client, sid); err != nil {
			log.Println("Failed to get initial data:", err)
			if !isProxyError(err) {
				retryCount++
				if retryCount >= 3 {
					sendLog("❌ Échec données initiales 3 fois de suite, arrêt du scraper")
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
			} else {
				log.Println("⚠️ Proxy error, trying another proxy...")
			}
			time.Sleep(10 * time.Second)
			continue
		}

		requestId := uuid.New().String()

		// First data
		postData := fmt.Sprintf("78:42[\"m\",{\"route\":\"sport:5\",\"requestId\":\"%s\"}]", requestId)

		if err := postSubscription(client, sid, postData); err != nil {
			log.Println("POST subscription failed:", err)
			if !isProxyError(err) {
				retryCount++
				if retryCount >= 3 {
					sendLog("❌ Échec subscription 3 fois de suite, arrêt du scraper")
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
			} else {
				log.Println("⚠️ Proxy error, trying another proxy...")
			}
			time.Sleep(10 * time.Second)
			continue
		}

		finalData, err := getFinalData(client, sid)
		if err != nil {
			log.Println("Error fetching final data:", err)
			if !isProxyError(err) {
				retryCount++
				if retryCount >= 3 {
					sendLog("❌ Échec données finales 3 fois de suite, arrêt du scraper")
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
			} else {
				log.Println("⚠️ Proxy error, trying another proxy...")
			}
			time.Sleep(10 * time.Second)
			continue
		}
		fmt.Println("Got first data")
		_ = finalData

		clientTime := time.Now().UnixMilli()
		postData = fmt.Sprintf(`129:42["m",{"requestId":"%s","route":"sport:5","data":true,"menu":true,"clientTime":%d}]`, requestId, clientTime)

		// Data with categories
		if err := postSubscription(client, sid, postData); err != nil {
			log.Println("POST subscription failed:", err)
			if !isProxyError(err) {
				retryCount++
				if retryCount >= 3 {
					sendLog("❌ Échec subscription catégories 3 fois de suite, arrêt du scraper")
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
			} else {
				log.Println("⚠️ Proxy error, trying another proxy...")
			}
			time.Sleep(10 * time.Second)
			continue
		}

		finalData, err = getFinalData(client, sid)
		if err != nil {
			log.Println("Error fetching final data:", err)
			retryCount++
			if retryCount >= 3 {
				log.Fatalln("Failed 3 times in a row, exiting.")
			}
			time.Sleep(10 * time.Second)
			continue
		}

		if err := os.WriteFile("final_response.txt", finalData, 0644); err != nil {
			log.Println("Error writing response to file:", err)
			retryCount++
			if retryCount >= 3 {
				log.Fatalln("Failed 3 times in a row, exiting.")
			}
			time.Sleep(10 * time.Second)
			continue
		}

		// Filter Matches
		jsonStr := string(finalData)
		jsonStr = strings.TrimSpace(jsonStr)
		if strings.HasPrefix(jsonStr, "//") {
			jsonStr = jsonStr[strings.Index(jsonStr, "\n")+1:]
		}

		// Remove numeric prefix before every ["m", and any ] before that
		re := regexp.MustCompile(`\](\d+:)?\["m",`)
		jsonStr = re.ReplaceAllString(jsonStr, `,["m",`)

		// Remove numeric prefix at the start (if any)
		if idx := strings.Index(jsonStr, "["); idx > 0 {
			jsonStr = jsonStr[idx:]
		}

		var arr []interface{}
		if err := json.Unmarshal([]byte(jsonStr), &arr); err != nil {
			log.Println("JSON parse error:", err)
			sendLog("⚠️ Erreur parsing JSON - retry...")
			continue
		}
		if len(arr) < 2 {
			log.Println("Unexpected JSON structure, less than 2 elements")
			sendLog("⚠️ Structure JSON inattendue - retry...")
			continue
		}
		root, ok := arr[1].(map[string]interface{})
		if !ok {
			log.Println("Root element is not a map")
			sendLog("⚠️ Format réponse invalide - retry...")
			continue
		}

		matches, _ := root["matches"].(map[string]interface{})
		if matches == nil {
			log.Println("No matches found in the response")
			sendLog("⚠️ Pas de 'matches' dans la réponse - retry...")
			continue
		}

		// loop all matches - get filters key, and check if 548 is in there
		var matchIDs []float64
		for _, m := range matches {
			match, _ := m.(map[string]interface{})
			rawFilters, ok := match["filters"]
			if !ok {
				log.Println("No filters found for a match")
				continue
			}

			filterList, ok := rawFilters.([]interface{})
			if !ok {
				log.Println("Filters are not in expected format")
				continue
			}

			found := false
			for _, f := range filterList {
				filterVal, ok := f.(float64) // JSON numbers are float64 by default
				if ok && int(filterVal) == 548 {
					found = true
					matchID, _ := match["matchId"].(float64)
					if matchID != 0 {
						matchIDs = append(matchIDs, matchID)
					} else {
						log.Println("Match ID is empty for a match with filter 548")
					}
					break
				}
			}

			if !found {
				continue
			}
		}

		fmt.Println("Matches found - Aces:", len(matchIDs))
		if len(matchIDs) > 0 {
			sendLog(fmt.Sprintf("🎾 %d match(s) avec aces trouvé(s)", len(matchIDs)))
		}

		// Fetch Data for each match
		var allResults []MatchData
		for _, matchID := range matchIDs {
			matchIDStr := fmt.Sprintf("%.0f", matchID)

			payload := fmt.Sprintf(`85:42["m",{"route":"match:%s","data":true,"menu":true,"clientTime":%s}]`, matchIDStr, fmt.Sprintf("%d", time.Now().UnixMilli()))
			if err := postSubscription(client, sid, payload); err != nil {
				log.Println("POST subscription for match failed:", err)
				if !isProxyError(err) {
					retryCount++
				} else {
					log.Println("⚠️ Proxy error on match request, skipping...")
				}
				continue
			}

			finalData, err = getFinalData(client, sid)
			if err != nil {
				log.Println("Error fetching final data for match:", err)
				retryCount++
				continue
			}

			// extract data
			jsonStr := string(finalData)
			jsonStr = strings.TrimSpace(jsonStr)
			if strings.HasPrefix(jsonStr, "//") {
				jsonStr = jsonStr[strings.Index(jsonStr, "\n")+1:]
			}

			// Remove numeric prefix before every ["m", and any ] before that
			re := regexp.MustCompile(`\]?\d+:\d+\["m",`)
			jsonStr = re.ReplaceAllString(jsonStr, `,["m",`)

			// Remove numeric prefix at the start (if any)
			if idx := strings.Index(jsonStr, "["); idx > 0 {
				jsonStr = jsonStr[idx:]
			}

			jsonStr = strings.TrimSpace(jsonStr)
			if !strings.HasSuffix(jsonStr, "]]") {
				jsonStr += "]]"
			}

			jsonStr, err = jsonrepair.RepairJSON(jsonStr)
			if err != nil {
				log.Println("Failed to repair JSON:", err)
				continue
			}

			if err := os.WriteFile("match_response.txt", []byte(jsonStr), 0644); err != nil {
				log.Println("Error writing response to file:", err)
				retryCount++
				if retryCount >= 3 {
					log.Fatalln("Failed 3 times in a row, exiting.")
				}
				time.Sleep(10 * time.Second)
				continue
			}

			var arr []interface{}
			if err := json.Unmarshal([]byte(jsonStr), &arr); err != nil {
				fmt.Println("Failed to unmarshal JSON:", err)
				continue
			}
			if len(arr) < 2 {
				fmt.Println("Unexpected JSON structure, expected at least 2 elements")
				continue
			}

			var bets map[string]interface{}
			var ok bool

			bets, outcomes, odds, ok := findLargestBetBlock(arr)
			if !ok {
				log.Println("No complete bet block found in the response")
				retryCount++
				continue
			}
			fmt.Println("Bets found:", len(bets))

			var finalBets []Bet
			for _, b := range bets {
				bet := b.(map[string]interface{})

				// check if contains nombre d'aces
				betTypeName, _ := bet["betTypeName"].(string)
				if !strings.Contains(strings.ToLower(betTypeName), "nombre d'aces") {
					continue
				}

				rawOutcomes, ok := bet["outcomes"].([]interface{})
				if !ok {
					log.Printf("dynamic bet %q has no outcomes\n", betTypeName)
					continue
				}

				// Bet Type
				template, _ := bet["template"].(string)

				if template == "dynamic" {
					var opts []Option
					for _, id := range rawOutcomes {
						key := fmt.Sprintf("%.0f", id.(float64)) // map keys are strings
						if oc, ok := outcomes[key].(map[string]interface{}); ok {
							lbl, _ := oc["label"].(string)
							seuil, _ := strconv.ParseFloat(regexp.MustCompile(`[\d.]+`).FindString(lbl), 64)

							if cote, ok := odds[key].(float64); ok {
								cote = math.Round(cote*20) / 20
								opts = append(opts, Option{Seuil: seuil, Cote: cote})
							}
						}
					}

					finalBets = append(finalBets, Bet{
						Type:    betTypeName,
						Options: opts,
					})
					continue
				}

				total := strings.TrimPrefix(bet["specialBetValue"].(string), "total=")
				cut, _ := strconv.ParseFloat(total, 64)
				overKey := fmt.Sprintf("%.0f", rawOutcomes[0].(float64))
				underKey := fmt.Sprintf("%.0f", rawOutcomes[1].(float64))

				plus := odds[overKey].(float64)
				moins := odds[underKey].(float64)
				plus = math.Round(plus*20) / 20
				moins = math.Round(moins*20) / 20

				// save
				finalBets = append(finalBets, Bet{
					Type:  betTypeName,
					Cut:   cut,
					Plus:  plus,
					Moins: moins,
				})
			}

			// save match
			matchTitle := ""
			if matchObj, ok := matches[matchIDStr].(map[string]interface{}); ok {
				matchTitle, _ = matchObj["title"].(string)
			}

			link := fmt.Sprintf("https://www.winamax.fr/paris-sportifs/match/%s", matchIDStr)

			// Sort bets in correct order based on player names from match title
			sortedBets := sortBets(finalBets, matchTitle)

			matchData := MatchData{
				Match: Match{
					Joueurs: matchTitle,
					Lien:    link,
				},
				Bet: sortedBets,
			}

			allResults = append(allResults, matchData)

		}

		// save matches
		output, err := json.MarshalIndent(allResults, "", "  ")
		if err != nil {
			log.Fatalln("Failed to marshal final output:", err)
		}
		if err := os.WriteFile(CURRENT_FILE, output, 0644); err != nil {
			log.Fatalln("Failed to write output JSON:", err)
		}

		fmt.Println("Saved aces bets to", CURRENT_FILE)

		// Compare with previous data and notify for new bets
		compareAndNotify(allResults)

		// Save current as past for next iteration
		saveAsPast()

		// Reset retry count on success
		retryCount = 0

		// Count total bets
		totalBets := 0
		for _, m := range allResults {
			totalBets += len(m.Bet)
		}

		// Send log for each check cycle with proxy info
		timestamp := time.Now().Format("15:04:05")
		sendLog(fmt.Sprintf("✅ [%s] OK - %d matchs, %d paris [%s]", timestamp, len(allResults), totalBets, proxyName))

		time.Sleep(15 * time.Second)
	}
}

func findLargestBetBlock(node interface{}) (bets, outcomes, odds map[string]interface{}, found bool) {
	var maxLen int

	var walk func(interface{})
	walk = func(n interface{}) {
		switch v := n.(type) {
		case map[string]interface{}:
			if b, ok := v["bets"].(map[string]interface{}); ok {
				if len(b) > maxLen {
					if o, ok1 := v["outcomes"].(map[string]interface{}); ok1 {
						if oddsMap, ok2 := v["odds"].(map[string]interface{}); ok2 {
							// Save the biggest block with bets, outcomes, and odds
							maxLen = len(b)
							bets = b
							outcomes = o
							odds = oddsMap
							found = true
						}
					}
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []interface{}:
			for _, child := range v {
				walk(child)
			}
		}
	}

	walk(node)
	return
}
