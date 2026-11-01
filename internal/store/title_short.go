package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Keep in sync with titleshort.DefaultShortMaxRunes.
const titleShortMaxRunes = 23

func titleSourceHash(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// LookupTitleShort returns a cached short name for the exact source title.
func (d *DB) LookupTitleShort(source string) (string, bool) {
	if d == nil {
		return "", false
	}
	source = strings.TrimSpace(source)
	if source == "" {
		return "", false
	}
	h := titleSourceHash(source)
	d.mu.Lock()
	defer d.mu.Unlock()
	var short string
	err := d.sql.QueryRow(
		`SELECT short_name FROM title_shortmap WHERE source_hash = ?`, h,
	).Scan(&short)
	if err != nil || short == "" {
		return "", false
	}
	return short, true
}

// UpsertTitleShort stores or refreshes a short-name mapping.
// Rejects empty or overlong short names (rune count > titleShortMaxRunes).
func (d *DB) UpsertTitleShort(source, short string) error {
	if d == nil {
		return nil
	}
	source = strings.TrimSpace(source)
	short = strings.TrimSpace(short)
	if source == "" || short == "" {
		return nil
	}
	if n := utf8.RuneCountInString(short); n > titleShortMaxRunes {
		return fmt.Errorf("title short too long (%d > %d)", n, titleShortMaxRunes)
	}
	h := titleSourceHash(source)
	ts := formatTS(time.Now())
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`
		INSERT INTO title_shortmap(source_hash, source, short_name, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(source_hash) DO UPDATE SET
		  source = excluded.source,
		  short_name = excluded.short_name,
		  updated_at = excluded.updated_at`,
		h, source, short, ts)
	return err
}
