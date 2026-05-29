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

func filterBySettlements(records []rosseti.ShutdownRecord, settlements []Settlement) []rosseti.ShutdownRecord {
	var result []rosseti.ShutdownRecord
	for _, r := range records {
		for _, s := range settlements {
			if r.Region == s.Region &&
				strings.Contains(strings.ToLower(r.Raion), strings.ToLower(s.Raion)) &&
				strings.Contains(strings.ToLower(r.Gorod), strings.ToLower(s.Gorod)) {
				result = append(result, r)
				break
			}
		}
	}
	return result
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

	dbPath := os.Getenv("PARSER_DB_PATH")
	if dbPath == "" {
		dbPath = "outages.db"
	}

	botURL = strings.TrimRight(botURL, "/") + "/api/outages"

	db, err := store.Init(dbPath)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	log.Println("Fetching data from Rosseti API...")
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

	payload := rosseti.OutagesPayload{Outages: unsent}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}

	req, err := http.NewRequest("POST", botURL, bytes.NewReader(body))
	if err != nil {
		log.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("send to bot: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Fatalf("bot returned %d: %s", resp.StatusCode, string(respBody))
	}

	if err := store.MarkAllUnsentSent(db); err != nil {
		log.Fatalf("mark sent: %v", err)
	}

	log.Printf("Bot response: %s", strings.TrimSpace(string(respBody)))
	log.Println("Parser done")
}
