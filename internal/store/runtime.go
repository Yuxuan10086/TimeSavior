package store

import "time"

// SiteRuntime is per-site policy runtime state.
type SiteRuntime struct {
	Site           string
	FullBlockUntil time.Time
	WatchStarted   time.Time
	LastPlayAt     time.Time
	WatchExempt    bool
	ExemptLabel    string
	ExemptUntil    time.Time
}

// UnlockQuota is daily feed-unlock state (started flag via DayCount, until, started_at).
type UnlockQuota struct {
	DayKey               string
	DayCount             int // 0 = not started this policy day; >=1 = started/used
	WeekKey              string
	WeekCount            int
	TempUnlockUntil      time.Time
	TempUnlockStartedAt  time.Time
}

// RuntimeSnapshot is everything needed to restore policy.Engine after restart.
type RuntimeSnapshot struct {
	Sites  []SiteRuntime
	Unlock UnlockQuota
}

// LoadRuntime reads persisted policy runtime state.
func (d *DB) LoadRuntime() (RuntimeSnapshot, error) {
	var out RuntimeSnapshot
	if d == nil {
		return out, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	row := d.sql.QueryRow(`
		SELECT day_key, day_count, week_key, week_count, temp_unlock_until, temp_unlock_started_at
		FROM unlock_quota WHERE id = 1`)
	var until, started string
	if err := row.Scan(
		&out.Unlock.DayKey, &out.Unlock.DayCount,
		&out.Unlock.WeekKey, &out.Unlock.WeekCount, &until, &started,
	); err != nil {
		return out, err
	}
	out.Unlock.TempUnlockUntil = parseTS(until)
	out.Unlock.TempUnlockStartedAt = parseTS(started)

	rows, err := d.sql.Query(`
		SELECT site, full_block_until, watch_started, last_play_at,
		       watch_exempt, exempt_label, exempt_until
		FROM site_runtime`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var s SiteRuntime
		var fb, ws, lp, eu string
		var ex int
		if err := rows.Scan(&s.Site, &fb, &ws, &lp, &ex, &s.ExemptLabel, &eu); err != nil {
			return out, err
		}
		s.FullBlockUntil = parseTS(fb)
		s.WatchStarted = parseTS(ws)
		s.LastPlayAt = parseTS(lp)
		s.WatchExempt = ex != 0
		s.ExemptUntil = parseTS(eu)
		out.Sites = append(out.Sites, s)
	}
	return out, rows.Err()
}

// SaveRuntime replaces runtime tables with the given snapshot (atomic).
func (d *DB) SaveRuntime(snap RuntimeSnapshot) error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		UPDATE unlock_quota SET
		  day_key = ?, day_count = ?, week_key = ?, week_count = ?,
		  temp_unlock_until = ?, temp_unlock_started_at = ?
		WHERE id = 1`,
		snap.Unlock.DayKey, snap.Unlock.DayCount,
		snap.Unlock.WeekKey, snap.Unlock.WeekCount,
		formatTS(snap.Unlock.TempUnlockUntil),
		formatTS(snap.Unlock.TempUnlockStartedAt),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM site_runtime`); err != nil {
		return err
	}
	for _, s := range snap.Sites {
		ex := 0
		if s.WatchExempt {
			ex = 1
		}
		if _, err := tx.Exec(`
			INSERT INTO site_runtime(
			  site, full_block_until, watch_started, last_play_at,
			  watch_exempt, exempt_label, exempt_until
			) VALUES (?,?,?,?,?,?,?)`,
			s.Site, formatTS(s.FullBlockUntil), formatTS(s.WatchStarted), formatTS(s.LastPlayAt),
			ex, s.ExemptLabel, formatTS(s.ExemptUntil),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
