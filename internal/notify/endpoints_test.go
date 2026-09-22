package notify

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/conan-manager/internal/events"
)

const botTokenValue = "123456789:AAHsecretsecretsecretsecretsecret_xyz"

// recorder is a transport that keeps every request it is given.
type recorder struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	status   int
	answer   string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	body := ""
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	r.requests = append(r.requests, req)
	r.bodies = append(r.bodies, body)
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(r.answer))}, nil
}

func recordingDispatcher(t *testing.T) (*Dispatcher, *Repository, *recorder) {
	t.Helper()
	repo, _ := testRepo(t)
	d := NewDispatcher(repo, slogDiscard())
	rec := &recorder{}
	client := &http.Client{Transport: rec}
	d.sender.client, d.sender.direct, d.sender.guarded = client, client, client
	return d, repo, rec
}

var crash = events.Event{Type: events.ServerCrashed, Severity: events.Error, ServerID: "pve", ServerName: "PvE <1>",
	Time:    time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC),
	Details: map[string]string{"announce": "Сервер PvE упал.", "exit": "signal SIGSEGV", "kind": "crash"}}

func TestTelegramChannel(t *testing.T) {
	d, repo, rec := recordingDispatcher(t)
	ctx := context.Background()
	for _, bad := range []Input{
		{Name: str("TG"), Kind: str(KindTelegram), Token: str("not-a-token"), Settings: &Settings{ChatID: "-100123"}},
		{Name: str("TG"), Kind: str(KindTelegram), Token: str(botTokenValue), Settings: &Settings{ChatID: "chat"}},
	} {
		if _, err := repo.Create(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	c, err := repo.Create(ctx, Input{Name: str("TG"), Kind: str(KindTelegram), Token: str(botTokenValue),
		Settings: &Settings{ChatID: "-1001234567890", ThreadID: 7, Proxy: true}, Mention: str("@here")})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := json.Marshal(c)
	if strings.Contains(string(view), "AAHsecret") || c.Mention != "" || !strings.Contains(c.Target, "123456789") {
		t.Fatalf("view %s", view)
	}
	d.Handle(crash)
	if len(rec.requests) != 1 {
		t.Fatalf("%d requests", len(rec.requests))
	}
	if got := rec.requests[0].URL.String(); got != "https://api.telegram.org/bot"+botTokenValue+"/sendMessage" {
		t.Fatalf("url %s", got)
	}
	var sent struct {
		ChatID   string `json:"chat_id"`
		Text     string `json:"text"`
		Mode     string `json:"parse_mode"`
		ThreadID int64  `json:"message_thread_id"`
	}
	if err := json.Unmarshal([]byte(rec.bodies[0]), &sent); err != nil {
		t.Fatal(err)
	}
	want := "<b>🔴 Сервер упал</b>\n<i>PvE &lt;1&gt;</i>\nСервер PvE упал.\n<b>Выход:</b> signal SIGSEGV"
	if sent.ChatID != "-1001234567890" || sent.Mode != "HTML" || sent.ThreadID != 7 || sent.Text != want {
		t.Fatalf("sent %+v", sent)
	}
}

func TestTelegramErrorsDoNotLeakTheToken(t *testing.T) {
	var waits []time.Duration
	s := testSender(failingTransport{}, &waits)
	c := Channel{Kind: KindTelegram, secret: secret{Token: botTokenValue}, Settings: Settings{ChatID: "1"}}
	err := s.sendTelegram(context.Background(), c, summarize(crash, "ru"))
	if err == nil || strings.Contains(err.Error(), "AAHsecret") {
		t.Fatalf("error leaks the token or is missing: %v", err)
	}
}

func TestHTTPChannelFillsTheTemplate(t *testing.T) {
	d, repo, rec := recordingDispatcher(t)
	ctx := context.Background()
	c, err := repo.Create(ctx, Input{Name: str("Сайт"), Kind: str(KindHTTP), URL: str("https://example.com/hook?key=s3cr3t"),
		Headers: str("Authorization: Bearer s3cr3t-header\nX-Source: conan")})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := json.Marshal(c)
	if strings.Contains(string(view), "s3cr3t") || c.Target != "POST https://example.com/…" || len(c.Headers) != 2 {
		t.Fatalf("view %s", view)
	}
	d.Handle(crash)
	if len(rec.requests) != 1 {
		t.Fatalf("%d requests", len(rec.requests))
	}
	req := rec.requests[0]
	if req.Method != "POST" || req.Header.Get("Authorization") != "Bearer s3cr3t-header" || !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("request %s %v", req.Method, req.Header)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.bodies[0]), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.bodies[0])
	}
	details, _ := body["details"].(map[string]any)
	if body["text"] != "Сервер PvE упал." || body["server"] != "PvE <1>" || body["event"] != "server.crashed" || body["kind"] != "crash" || details["exit"] != "signal SIGSEGV" {
		t.Fatalf("body %v", body)
	}

	// Headers left out on an update stay; an empty value removes them.
	c, err = repo.Update(ctx, c.ID, Input{Name: str("Сайт 2")})
	if err != nil || len(c.Headers) != 2 {
		t.Fatalf("headers lost: %v %v", c.Headers, err)
	}
	c, err = repo.Update(ctx, c.ID, Input{Headers: str(""), Settings: &Settings{Method: "get"}, URL: str("https://example.com/notify?text={text}&server={server}")})
	if err != nil || len(c.Headers) != 0 || c.Settings.Method != "GET" {
		t.Fatalf("after update: %+v %v", c, err)
	}
	d.Handle(crash)
	if got := rec.requests[1].URL.RawQuery; got != "text=%D0%A1%D0%B5%D1%80%D0%B2%D0%B5%D1%80+PvE+%D1%83%D0%BF%D0%B0%D0%BB.&server=PvE+%3C1%3E" || rec.bodies[1] != "" {
		t.Fatalf("GET query %q body %q", got, rec.bodies[1])
	}
}

func TestHTTPChannelValidation(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	for name, in := range map[string]Input{
		"no url":         {Name: str("x"), Kind: str(KindHTTP)},
		"ftp":            {Name: str("x"), Kind: str(KindHTTP), URL: str("ftp://example.com/")},
		"credentials":    {Name: str("x"), Kind: str(KindHTTP), URL: str("https://user:pw@example.com/")},
		"method":         {Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/"), Settings: &Settings{Method: "TRACE"}},
		"broken json":    {Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/"), Settings: &Settings{Body: `{"text": {text}}`}},
		"reserved":       {Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/"), Headers: str("Host: evil")},
		"header form":    {Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/"), Headers: str("no colon")},
		"unknown kind":   {Name: str("x"), Kind: str("sms"), URL: str("https://example.com/")},
		"discord on tg":  {Name: str("x"), Kind: str(KindTelegram), WebhookURL: str(webhook)},
		"content type":   {Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/"), Settings: &Settings{ContentType: "text/html"}},
		"header newline": {Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/"), Headers: str("X-A: a\x00b")},
	} {
		if _, err := repo.Create(ctx, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c, err := repo.Create(ctx, Input{Name: str("x"), Kind: str(KindHTTP), URL: str("https://example.com/")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(ctx, c.ID, Input{Kind: str(KindDiscord)}); err == nil {
		t.Error("kind changed")
	}
}

// An HTTP channel reaches the manager's own host and networks only when
// allowed to.
func TestHTTPChannelKeepsOffPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer local.Close()
	repo, _ := testRepo(t)
	d := NewDispatcher(repo, slogDiscard())
	d.sender.sleep = func(context.Context, time.Duration) error { return nil }
	ctx := context.Background()
	c, err := repo.Create(ctx, Input{Name: str("local"), Kind: str(KindHTTP), URL: str(local.URL + "/hook")})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Test(ctx, c.ID); err == nil || !strings.Contains(err.Error(), "private") || hits.Load() != 0 {
		t.Fatalf("reached a local address: %v, %d hits", err, hits.Load())
	}
	if _, err := repo.Update(ctx, c.ID, Input{Settings: &Settings{AllowPrivate: true}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Test(ctx, c.ID); err != nil || hits.Load() != 1 {
		t.Fatalf("allowed local address: %v, %d hits", err, hits.Load())
	}
}

func TestPublicIP(t *testing.T) {
	for addr, public := range map[string]bool{"8.8.8.8": true, "127.0.0.1": false, "10.1.2.3": false, "192.168.0.1": false,
		"169.254.169.254": false, "100.64.1.1": false, "::1": false, "fd00::1": false, "2a00:1450::1": true, "0.0.0.0": false} {
		if got := publicIP(net.ParseIP(addr)); got != public {
			t.Errorf("%s: public %v", addr, got)
		}
	}
}
