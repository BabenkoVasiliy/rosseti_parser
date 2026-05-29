package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BabenkoVasiliy/rosseti_parser/rosseti"
	"github.com/BabenkoVasiliy/rosseti_parser/store"
	_ "modernc.org/sqlite"
)

type Settlement struct {
	Region string
	Raion  string
	Gorod  string
}

type SettlementStreets struct {
	Region  string   `json:"region"`
	Raion   string   `json:"raion"`
	Gorod   string   `json:"gorod"`
	Streets []string `json:"streets"`
}

type StreetsPayload struct {
	Settlements []SettlementStreets `json:"settlements"`
}

func parseSettlements(env string) ([]Settlement, error) {
	parts := strings.Split(env, ";")
	var result []Settlement
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, ",")
		if len(fields) != 3 {
			return nil, fmt.Errorf("settlement #%d: expected 3 fields (region,raion,gorod), got %d", i+1, len(fields))
		}
		result = append(result, Settlement{
			Region: strings.TrimSpace(fields[0]),
			Raion:  strings.TrimSpace(fields[1]),
			Gorod:  strings.TrimSpace(fields[2]),
		})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no settlements defined in PARSER_SETTLEMENTS")
	}
	return result, nil
}

func matchField(record, settlement string) bool {
	a, b := strings.ToLower(record), strings.ToLower(settlement)
	return strings.Contains(a, b) || strings.Contains(b, a)
}

func filterBySettlements(records []rosseti.ShutdownRecord, settlements []Settlement) []rosseti.ShutdownRecord {
	var result []rosseti.ShutdownRecord
	for _, r := range records {
		for _, s := range settlements {
			if r.Region == s.Region &&
				matchField(r.Raion, s.Raion) &&
				matchField(r.Gorod, s.Gorod) {
				result = append(result, r)
				break
			}
		}
	}
	return result
}

func extractStreets(records []rosseti.ShutdownRecord, settlements []Settlement) []SettlementStreets {
	seen := make(map[string]map[string]bool)
	for _, s := range settlements {
		key := s.Region + "|" + s.Raion + "|" + s.Gorod
		seen[key] = make(map[string]bool)
	}
	for _, r := range records {
		for _, s := range settlements {
			if r.Region == s.Region &&
				matchField(r.Raion, s.Raion) &&
				matchField(r.Gorod, s.Gorod) {
				key := s.Region + "|" + s.Raion + "|" + s.Gorod
				if r.Street != "" {
					seen[key][r.Street] = true
				}
				break
			}
		}
	}
	var result []SettlementStreets
	for _, s := range settlements {
		key := s.Region + "|" + s.Raion + "|" + s.Gorod
		streets := make([]string, 0, len(seen[key]))
		for st := range seen[key] {
			streets = append(streets, st)
		}
		result = append(result, SettlementStreets{
			Region:  s.Region,
			Raion:   s.Raion,
			Gorod:   s.Gorod,
			Streets: streets,
		})
	}
	return result
}

const httpTimeout = 90 * time.Second

func wakeUpBot(baseURL string) {
	log.Printf("Waking up bot at %s/health ...", baseURL)
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		log.Printf("wake-up failed (bot may already be awake): %v", err)
		return
	}
	resp.Body.Close()
	log.Println("Bot is awake")
}

func postJSON(url, apiKey string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("bot returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	log.Printf("Bot response: %s", strings.TrimSpace(string(respBody)))
	return nil
}

func runStreetsMode(botURL, apiKey string, settlements []Settlement) {
	log.Println("Fetching data from Rosseti API for streets...")
	records, err := rosseti.FetchData()
	if err != nil {
		log.Fatalf("fetch data: %v", err)
	}
	log.Printf("Fetched %d records total", len(records))

	streets := extractStreets(records, settlements)
	for _, s := range streets {
		log.Printf("Settlement %s/%s/%s: %d streets", s.Region, s.Raion, s.Gorod, len(s.Streets))
	}

	wakeUpBot(botURL)
	payload := StreetsPayload{Settlements: streets}
	if err := postJSON(botURL+"/api/streets", apiKey, payload); err != nil {
		log.Fatalf("send streets: %v", err)
	}
	log.Println("Streets sent")
}

func runOutagesMode(botURL, apiKey string, settlements []Settlement, db *sql.DB) {
	log.Println("Fetching data from Rosseti API for outages...")
	records, err := rosseti.FetchData()
	if err != nil {
		log.Fatalf("fetch data: %v", err)
	}
	log.Printf("Fetched %d records total", len(records))

	records = filterBySettlements(records, settlements)
	log.Printf("Filtered for %d settlements: %d records", len(settlements), len(records))

	if err := store.Insert(db, records); err != nil {
		log.Fatalf("store records: %v", err)
	}

	unsent, err := store.GetUnsent(db)
	if err != nil {
		log.Fatalf("get unsent: %v", err)
	}
	log.Printf("Unsent records: %d", len(unsent))

	if len(unsent) == 0 {
		log.Println("No new records, nothing to send")
		return
	}

	wakeUpBot(botURL)
	if err := postJSON(botURL+"/api/outages", apiKey, rosseti.OutagesPayload{Outages: unsent}); err != nil {
		log.Fatalf("send outages: %v", err)
	}

	if err := store.MarkAllUnsentSent(db); err != nil {
		log.Fatalf("mark sent: %v", err)
	}
}

func main() {
	botURL := os.Getenv("BOT_SERVICE_URL")
	if botURL == "" {
		log.Fatal("BOT_SERVICE_URL is required")
	}
	apiKey := os.Getenv("PARSER_API_KEY")

	settlementsEnv := os.Getenv("PARSER_SETTLEMENTS")
	if settlementsEnv == "" {
		log.Fatal("PARSER_SETTLEMENTS is required (format: region,raion,gorod;region,raion,gorod)")
	}
	settlements, err := parseSettlements(settlementsEnv)
	if err != nil {
		log.Fatalf("parse settlements: %v", err)
	}

	botURL = strings.TrimRight(botURL, "/")

	if len(os.Args) > 1 && os.Args[1] == "--streets" {
		runStreetsMode(botURL, apiKey, settlements)
		return
	}

	dbPath := os.Getenv("PARSER_DB_PATH")
	if dbPath == "" {
		dbPath = "outages.db"
	}

	db, err := store.Init(dbPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	runOutagesMode(botURL, apiKey, settlements, db)
	log.Println("Parser done")
}
