package store

import (
	"database/sql"

	"github.com/BabenkoVasiliy/rosseti_parser/rosseti"
)

func Init(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS outages (
			id TEXT PRIMARY KEY,
			region TEXT NOT NULL DEFAULT '',
			raion TEXT NOT NULL DEFAULT '',
			gorod TEXT NOT NULL DEFAULT '',
			street TEXT NOT NULL DEFAULT '',
			date_start TEXT NOT NULL DEFAULT '',
			date_finish TEXT NOT NULL DEFAULT '',
			time_start TEXT NOT NULL DEFAULT '',
			time_finish TEXT NOT NULL DEFAULT '',
			f_otkl TEXT NOT NULL DEFAULT '',
			res TEXT NOT NULL DEFAULT '',
			sent INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Insert(db *sql.DB, records []rosseti.ShutdownRecord) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO outages
			(id, region, raion, gorod, street, date_start, date_finish, time_start, time_finish, f_otkl, res)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range records {
		if _, err := stmt.Exec(r.ID, r.Region, r.Raion, r.Gorod, r.Street, r.DateStart, r.DateFinish, r.TimeStart, r.TimeFinish, r.FOtkl, r.Res); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func GetUnsent(db *sql.DB) ([]rosseti.ShutdownRecord, error) {
	rows, err := db.Query(`
		SELECT id, region, raion, gorod, street, date_start, date_finish, time_start, time_finish, f_otkl, res
		FROM outages WHERE sent = 0
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []rosseti.ShutdownRecord
	for rows.Next() {
		var r rosseti.ShutdownRecord
		if err := rows.Scan(&r.ID, &r.Region, &r.Raion, &r.Gorod, &r.Street, &r.DateStart, &r.DateFinish, &r.TimeStart, &r.TimeFinish, &r.FOtkl, &r.Res); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func MarkAllUnsentSent(db *sql.DB) error {
	_, err := db.Exec("UPDATE outages SET sent = 1 WHERE sent = 0")
	return err
}
