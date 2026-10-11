package sender

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Etr1x/kuma_log_sender/internal/config"
)

// UserAgent подставляется в HTTP-запросы; версию задаёт main.
var UserAgent = "kuma_log_sender"

// httpSender отправляет каждое событие отдельным POST-запросом, тело запроса —
// событие с разделителем.
type httpSender struct {
	url    string
	client *http.Client
}

func newHTTPSender(c config.Collector, addr string, tlsCfg *tls.Config, opt Options) *httpSender {
	scheme := "http"
	if c.TLS {
		scheme = "https"
	}
	dialer := &net.Dialer{Timeout: opt.Timeout}
	return &httpSender{
		url: scheme + "://" + addr + c.Path,
		client: &http.Client{
			Timeout: opt.Timeout,
			Transport: &http.Transport{
				Proxy:               nil, // к коллектору идём напрямую, системный прокси не нужен
				DialContext:         dialer.DialContext,
				TLSClientConfig:     tlsCfg,
				TLSHandshakeTimeout: opt.Timeout,
				DisableCompression:  true,
				MaxIdleConns:        2,
				IdleConnTimeout:     30 * time.Second,
			},
			// Редирект для POST превращается в GET и теряет тело — показываем его как ошибку.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (s *httpSender) Send(ctx context.Context, payload []byte) (Result, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return Result{Took: time.Since(start)}, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	took := time.Since(start)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{Took: took}, &HTTPStatusError{
			Code:     resp.StatusCode,
			Status:   resp.Status,
			Location: resp.Header.Get("Location"),
			Body:     oneLine(body, 200),
		}
	}
	return Result{Bytes: len(payload), Took: took, Status: "HTTP " + resp.Status}, nil
}

func (s *httpSender) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// HTTPStatusError — коллектор ответил, но не кодом 2xx.
type HTTPStatusError struct {
	Code     int
	Status   string
	Location string
	Body     string
}

func (e *HTTPStatusError) Error() string {
	s := "HTTP " + e.Status
	if e.Location != "" {
		s += " → " + e.Location
	}
	if e.Body != "" {
		s += ": " + e.Body
	}
	return s
}

func oneLine(b []byte, max int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}
