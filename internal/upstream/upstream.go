package upstream

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"timesavior/internal/config"
)

type Manager struct {
	cfg *config.ClashUpstream

	mu       sync.RWMutex
	alive    atomic.Bool
	port     atomic.Int32
	stopCh   chan struct{}
	onceStop sync.Once
}

func New(cfg config.ClashUpstream) *Manager {
	m := &Manager{
		cfg:    &cfg,
		stopCh: make(chan struct{}),
	}
	m.port.Store(int32(cfg.Port))
	return m
}

func (m *Manager) Start() {
	if m.cfg == nil || !m.cfg.Enabled {
		m.alive.Store(false)
		return
	}
	m.probeOnce()
	go m.loop()
}

func (m *Manager) Stop() {
	m.onceStop.Do(func() { close(m.stopCh) })
}

func (m *Manager) loop() {
	iv := time.Duration(m.cfg.ProbeSeconds) * time.Second
	if iv <= 0 {
		iv = 3 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			m.probeOnce()
		}
	}
}

func (m *Manager) probeOnce() {
	ports := m.cfg.ProbePorts
	if len(ports) == 0 {
		ports = []int{m.cfg.Port}
	}
	host := m.cfg.Host
	for _, p := range ports {
		addr := net.JoinHostPort(host, fmt.Sprintf("%d", p))
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			m.port.Store(int32(p))
			m.alive.Store(true)
			return
		}
	}
	m.alive.Store(false)
}

func (m *Manager) Alive() bool {
	return m.alive.Load()
}

func (m *Manager) Port() int {
	return int(m.port.Load())
}

func (m *Manager) Status() string {
	if !m.cfg.Enabled {
		return "disabled"
	}
	if m.Alive() {
		return fmt.Sprintf("clash:%d", m.Port())
	}
	return "direct"
}

func (m *Manager) ProxyURL() *url.URL {
	if !m.Alive() {
		return nil
	}
	return &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(m.cfg.Host, fmt.Sprintf("%d", m.Port())),
	}
}

func (m *Manager) Transport() *http.Transport {
	tr := &http.Transport{
		Proxy: m.proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return tr
}

func (m *Manager) proxyFunc(req *http.Request) (*url.URL, error) {
	return m.ProxyURL(), nil
}
