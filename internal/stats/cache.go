package stats

import (
	"strconv"
	"sync"
	"time"

	"timesavior/internal/store"
)

var (
	cacheMu    sync.Mutex
	cacheAt    time.Time
	cacheKey   string
	cacheDir   string
	cacheDB    string
	cacheRep   Report
	cacheOpen  string // fingerprint of open focus duration
)

// InvalidateCache forces the next BuildCached call to recompute.
func InvalidateCache() {
	cacheMu.Lock()
	cacheAt = time.Time{}
	cacheMu.Unlock()
}

// BuildCached returns a cached Build result (3s TTL) for the last `days` days.
func BuildCached(logDir string, days int, openFocus map[string]any, db *store.DB) Report {
	return BuildCachedQuery(logDir, Query{Days: days}, openFocus, db)
}

// BuildCachedQuery returns a cached BuildQuery result (3s TTL).
func BuildCachedQuery(logDir string, q Query, openFocus map[string]any, db *store.DB) Report {
	fp := ""
	if openFocus != nil {
		if v, ok := openFocus["duration_sec"]; ok {
			fp = stringify(v)
		}
		if b, _ := openFocus["bucket"].(string); b != "" {
			fp += "|" + b
		}
	}
	dbPath := ""
	if db != nil {
		dbPath = db.Path()
	}
	key := cacheQueryKey(q)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if logDir == cacheDir && dbPath == cacheDB && key == cacheKey && fp == cacheOpen && time.Since(cacheAt) < 3*time.Second {
		return cacheRep
	}
	rep := BuildQuery(logDir, q, openFocus, db)
	cacheDir, cacheDB, cacheKey, cacheOpen, cacheAt, cacheRep = logDir, dbPath, key, fp, time.Now(), rep
	return rep
}

func cacheQueryKey(q Query) string {
	if q.From != "" || q.To != "" {
		return "r:" + q.From + ".." + q.To
	}
	return "d:" + strconv.Itoa(q.Days)
}

func stringify(v any) string {
	switch t := v.(type) {
	case float64:
		return time.Duration(t * float64(time.Second)).String()
	default:
		return ""
	}
}
