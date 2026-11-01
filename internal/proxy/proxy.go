package proxy

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/elazarl/goproxy"

	"timesavior/internal/activity"
	"timesavior/internal/audit"
	"timesavior/internal/config"
	"timesavior/internal/policy"
	"timesavior/internal/ui"
	"timesavior/internal/upstream"
	"timesavior/internal/watchmeta"
)

type Server struct {
	cfg      *config.Config
	engine   *policy.Engine
	upstream *upstream.Manager
	audit    *audit.Logger
	recent   *activity.Recent
	proxy    *goproxy.ProxyHttpServer
	caCert   tls.Certificate
}

func New(cfg *config.Config, engine *policy.Engine, up *upstream.Manager, al *audit.Logger, recent *activity.Recent) (*Server, error) {
	ca, err := loadOrCreateCA(filepath.Join(cfg.DataDir, "ca"))
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		engine:   engine,
		upstream: up,
		audit:    al,
		recent:   recent,
		caCert:   ca,
	}
	s.proxy = s.build()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.proxy }

func (s *Server) CACertPath() string {
	return filepath.Join(s.cfg.DataDir, "ca", "ca.crt")
}

func (s *Server) build() *goproxy.ProxyHttpServer {
	p := goproxy.NewProxyHttpServer()
	p.Verbose = false
	p.Tr = s.upstream.Transport()

	goproxy.GoproxyCa = s.caCert
	goproxy.OkConnect = &goproxy.ConnectAction{Action: goproxy.ConnectAccept, TLSConfig: goproxy.TLSConfigFromCA(&s.caCert)}
	goproxy.MitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&s.caCert)}
	goproxy.HTTPMitmConnect = &goproxy.ConnectAction{Action: goproxy.ConnectHTTPMitm, TLSConfig: goproxy.TLSConfigFromCA(&s.caCert)}
	goproxy.RejectConnect = &goproxy.ConnectAction{Action: goproxy.ConnectReject, TLSConfig: goproxy.TLSConfigFromCA(&s.caCert)}

	// Tunnel-only hosts still need Clash when alive; OkConnect uses ConnectDial*.
	p.ConnectDialWithReq = func(req *http.Request, network, addr string) (net.Conn, error) {
		if u := s.upstream.ProxyURL(); u != nil {
			return p.NewConnectDialToProxy(u.String())(network, addr)
		}
		if p.Tr != nil && p.Tr.DialContext != nil {
			return p.Tr.DialContext(req.Context(), network, addr)
		}
		var d net.Dialer
		return d.DialContext(req.Context(), network, addr)
	}

	// Cursor (and similar) break under AlwaysMitm (HTTP/2 / TLS reset). Tunnel only.
	p.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if tunnelOnlyHost(host) {
			return goproxy.OkConnect, host
		}
		return goproxy.MitmConnect, host
	})
	p.OnRequest().DoFunc(s.onRequest)
	p.OnResponse().DoFunc(s.onResponse)
	return p
}

func (s *Server) onRequest(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	path := "/"
	rawQuery := ""
	if req.URL != nil {
		if req.URL.Path != "" {
			path = req.URL.Path
		}
		rawQuery = req.URL.RawQuery
	}
	// Prefer plain responses for whitelist + watch-meta sniffing.
	hl := strings.ToLower(host)
	if strings.Contains(hl, "youtube") ||
		strings.Contains(hl, "bilibili") ||
		strings.Contains(hl, "biliapi") ||
		strings.Contains(hl, "douyin") {
		req.Header.Del("Accept-Encoding")
	}

	res := s.engine.EvaluateMeta(policy.RequestMeta{
		Host:     host,
		Path:     path,
		RawQuery: rawQuery,
		Referer:  req.Header.Get("Referer"),
	}, time.Now())

	now := time.Now()
	kind := ""
	if site, _ := s.engine.MatchSite(host, path); site != nil {
		kind = site.Kind
	}
	if s.recent != nil {
		interesting := res.Site != "" || res.Category == "play" || s.recent.HostIsAI(host)
		if interesting {
			s.recent.Observe(host, res.Site, string(res.Category), kind, now)
		}
	}
	if s.audit != nil && res.Category == "play" && res.Site != "" {
		s.audit.Play(res.Site)
	}

	if s.cfg.Logging.RequestLog {
		log.Printf("%s %s%s site=%s cat=%s decision=%s reason=%s upstream=%s",
			req.Method, host, path, res.Site, res.Category, res.Decision, res.Reason, s.upstream.Status())
	}

	if s.audit != nil && s.cfg.Logging.AuditLog && res.Site != "" {
		// Play duration lives in SQLite play_segments; request JSONL only for
		// blocks (default) or full managed dump when request_all_managed=true.
		shouldReq := s.cfg.Logging.RequestAllManaged || res.Decision == policy.Block
		if shouldReq {
			s.audit.Request(map[string]any{
				"method":   req.Method,
				"host":     host,
				"path":     path,
				"site":     res.Site,
				"category": string(res.Category),
				"decision": string(res.Decision),
				"reason":   string(res.Reason),
				"upstream": s.upstream.Status(),
			})
		}
		active, until := s.engine.FullBlockActive(res.Site, time.Now())
		untilStr := ""
		if active {
			untilStr = until.Format(time.RFC3339)
		}
		s.audit.NoteFullBlock(res.Site, active, untilStr)
	}

	if res.Decision == policy.Block {
		w := &captureResponse{header: make(http.Header), status: http.StatusForbidden}
		ui.WriteBlock(w, req, res.Reason, res.Site)
		body := w.body
		hdr := w.header.Clone()
		if hdr.Get("Content-Length") == "" {
			hdr.Set("Content-Length", fmt.Sprintf("%d", len(body)))
		}
		return req, &http.Response{
			StatusCode:    w.status,
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        hdr,
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
			Request:       req,
		}
	}
	return req, nil
}

const sniffMax = 1536 << 10 // 1.5 MiB — YouTube watch HTML often exceeds 512KiB before videoDetails

func (s *Server) onResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if resp == nil || resp.Request == nil || resp.Body == nil {
		return resp
	}
	req := resp.Request
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	path := "/"
	if req.URL != nil && req.URL.Path != "" {
		path = req.URL.Path
	}
	pathL := strings.ToLower(path)
	hostL := strings.ToLower(host)
	needWhitelist := strings.Contains(hostL, "youtube") &&
		(strings.HasPrefix(pathL, "/watch") ||
			strings.Contains(pathL, "/youtubei/v1/player") ||
			strings.Contains(pathL, "/youtubei/v1/next"))
	needMeta := s.cfg.Logging.WatchMeta && s.audit != nil &&
		watchmeta.ShouldSniff("", host, path)
	needTitleWL := s.cfg.HasWatchRules() &&
		(needWhitelist || needMeta ||
			strings.Contains(hostL, "bilibili") ||
			strings.Contains(hostL, "douyin"))
	if !needWhitelist && !needMeta && !needTitleWL {
		return resp
	}
	// Only skip clearly non-text bodies (media/protobuf). Empty CT is allowed.
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct != "" &&
		!(strings.Contains(ct, "html") ||
			strings.Contains(ct, "json") ||
			strings.Contains(ct, "javascript") ||
			strings.Contains(ct, "text/") ||
			strings.Contains(ct, "xml")) {
		return resp
	}

	limited := io.LimitReader(resp.Body, sniffMax)
	buf, err := io.ReadAll(limited)
	rest := resp.Body
	resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf), rest))
	if err != nil && len(buf) == 0 {
		return resp
	}

	now := time.Now()
	if needWhitelist || needTitleWL {
		s.engine.ObserveResponse(host, path, buf, now)
	}
	if needMeta {
		siteName := ""
		if site, _ := s.engine.MatchSite(host, path); site != nil {
			siteName = site.Name
		}
		if meta := watchmeta.Extract(siteName, host, path, buf); meta != nil && meta.Useful() {
			fields := map[string]any{
				"site":       meta.Site,
				"video_id":   meta.VideoID,
				"title":      meta.Title,
				"author":     meta.Author,
				"tags":       meta.Tags,
				"url":        meta.URL,
				"source":     meta.Source,
				"dedupe_key": meta.DedupeKey(),
			}
			if ex, label := s.engine.NoteWatchMeta(meta.Site, meta.Title, meta.Author, meta.Tags, meta.VideoID, now); ex {
				fields["watch_exempt"] = true
				fields["exempt_label"] = label
			} else if st := s.engine.Status(now); st != nil {
				if sites, ok := st["sites"].(map[string]any); ok {
					if raw, ok := sites[meta.Site].(map[string]any); ok {
						if ex, _ := raw["watch_exempt"].(bool); ex {
							fields["watch_exempt"] = true
							fields["exempt_label"] = raw["exempt_label"]
						}
					}
				}
			}
			s.audit.Watch(fields)
		}
	}
	return resp
}

type captureResponse struct {
	header http.Header
	status int
	body   []byte
}

func (c *captureResponse) Header() http.Header { return c.header }
func (c *captureResponse) Write(b []byte) (int, error) {
	c.body = append(c.body, b...)
	return len(b), nil
}
func (c *captureResponse) WriteHeader(statusCode int) { c.status = statusCode }

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	log.Printf("TimeSavior proxy listening on %s", s.cfg.Listen)
	return http.Serve(ln, s.proxy)
}

func HostOnly(hostport string) string {
	h := hostport
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h
}
