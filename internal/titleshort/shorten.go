// Package titleshort compresses long stats labels via LLM with SQLite cache.
package titleshort

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"timesavior/internal/llm"
	"timesavior/internal/store"
)

const (
	// DefaultTriggerRunes: labels longer than this get a short name.
	DefaultTriggerRunes = 28
	// DefaultShortMaxRunes: LLM / truncate target length (display runes).
	DefaultShortMaxRunes = 23
)

// OnUpdated is called after a new short name is cached (e.g. invalidate stats + SSE).
type OnUpdated func(source, short string)

// OnLLMDialog is called after an LLM HTTP call is written to llm_dialogs (success or fail).
type OnLLMDialog func()

// Shortener resolves display short names; cache hits are sync, misses enqueue LLM.
type Shortener struct {
	db          *store.DB
	client      *llm.Client
	trigger     int
	shortMax    int
	onUpdated   OnUpdated
	onLLMDialog OnLLMDialog

	mu      sync.Mutex
	pending map[string]bool
}

// Options configures Shortener.
type Options struct {
	TriggerRunes int
	ShortMax     int
	OnUpdated    OnUpdated
	OnLLMDialog  OnLLMDialog
}

// New returns a shortener. client may be nil (truncate-only fallback).
func New(db *store.DB, client *llm.Client, opt Options) *Shortener {
	if opt.TriggerRunes <= 0 {
		opt.TriggerRunes = DefaultTriggerRunes
	}
	if opt.ShortMax <= 0 {
		opt.ShortMax = DefaultShortMaxRunes
	}
	return &Shortener{
		db:          db,
		client:      client,
		trigger:     opt.TriggerRunes,
		shortMax:    opt.ShortMax,
		onUpdated:   opt.OnUpdated,
		onLLMDialog: opt.OnLLMDialog,
		pending:     map[string]bool{},
	}
}

// Resolve returns (display, full). full is non-empty when display is shortened.
// Cache hits are immediate; misses return a truncated fallback and enqueue LLM.
func (s *Shortener) Resolve(source string) (display, full string) {
	source = strings.TrimSpace(source)
	if source == "" {
		return source, ""
	}
	if utf8.RuneCountInString(source) <= s.trigger {
		return source, ""
	}
	if s.db != nil {
		if short, ok := s.db.LookupTitleShort(source); ok && short != "" {
			if utf8.RuneCountInString(short) <= s.shortMax {
				return short, source
			}
			// overlong cache entry: ignore and re-shorten
		}
	}
	display = truncateRunes(source, s.shortMax)
	s.enqueue(source)
	return display, source
}

func (s *Shortener) enqueue(source string) {
	if s == nil || s.client == nil || s.db == nil || source == "" {
		return
	}
	s.mu.Lock()
	if s.pending[source] {
		s.mu.Unlock()
		return
	}
	s.pending[source] = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.pending, source)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		now := time.Now()
		userPrompt := llm.BuildShortTitleUserPrompt(source, s.shortMax)
		requestRaw := llm.FormatMessagesRequest(llm.ShortTitleSystem, userPrompt)
		res, err := s.client.ShortenTitle(ctx, source, s.shortMax)
		cost := 0.0
		respRaw := ""
		short := ""
		errMsg := ""
		reason := ""
		if err != nil {
			errMsg = err.Error()
			log.Printf("title short: %v", err)
			reason = "标题短名失败"
		} else {
			short = res.Short
			cost = res.CostCNY
			respRaw = res.RawContent
			if utf8.RuneCountInString(short) > s.shortMax {
				errMsg = "short title too long"
				log.Printf("title short: reject overlong (%d > %d): %q", utf8.RuneCountInString(short), s.shortMax, short)
				reason = "短名超长已拒绝"
				short = ""
			} else {
				reason = "短名：" + short
			}
		}
		_ = s.db.AddLLMUsage(now.Format("2006-01-02"), cost)
		_ = s.db.InsertLLMDialog(store.LLMDialogInput{
			Day:         now.Format("2006-01-02"),
			Site:        "title_short",
			Kind:        store.LLMKindTitleShort,
			CostCNY:     cost,
			Reason:      reason,
			ErrMsg:      errMsg,
			RequestRaw:  requestRaw,
			ResponseRaw: respRaw,
			VideosJSON:  "[]",
			At:          now,
		})
		if s.onLLMDialog != nil {
			s.onLLMDialog()
		}
		if short == "" {
			return
		}
		if err := s.db.UpsertTitleShort(source, short); err != nil {
			log.Printf("title short save: %v", err)
			return
		}
		if s.onUpdated != nil {
			s.onUpdated(source, short)
		}
	}()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
