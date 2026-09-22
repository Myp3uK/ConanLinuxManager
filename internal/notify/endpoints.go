package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/example/conan-manager/internal/events"
)

// notice is an event as a channel tells it, before any service's format.
type notice struct {
	Title    string
	Server   string
	ServerID string
	Text     string
	Fields   []noticeField
	Event    string
	Kind     string
	Severity string
	Time     time.Time
}

type noticeField struct{ Name, Key, Value string }

// summarize picks an event's title, text and fields in the channel's
// language.
func summarize(e events.Event, language string) notice {
	title := e.Message
	t, known := titles[e.Type]
	if known {
		title = pick(t, language)
	}
	n := notice{Title: title, Server: e.ServerName, ServerID: e.ServerID, Event: string(e.Type), Kind: e.Details["kind"],
		Severity: string(e.Severity), Time: e.Time.UTC()}
	if n.Server == "" {
		n.Server = e.ServerID
	}
	var lines []string
	if known && e.Message != "" && (e.Type == events.ServerRestartAbandoned || e.Type == events.ServerKilled || e.Type == events.ServerRestarting) {
		lines = append(lines, e.Message)
	}
	// A server's own text for the event is the message itself, not a field.
	if text := e.Details["announce"]; text != "" {
		lines = append(lines, text)
	}
	n.Text = strings.Join(lines, "\n")
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		// announce is the message; discord and kind steer delivery only.
		if k != "announce" && k != "discord" && k != "kind" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		if dn, ok := detailNames[k]; ok {
			name = pick(dn, language)
		}
		n.Fields = append(n.Fields, noticeField{Name: name, Key: k, Value: e.Details[k]})
		if len(n.Fields) == 10 {
			break
		}
	}
	return n
}

// ----- Telegram -----

const telegramAPI = "https://api.telegram.org"

// telegramText is a notice as Telegram HTML: the title in bold, the server,
// the message, and the fields, long ones as preformatted text.
func telegramText(n notice) string {
	var b strings.Builder
	b.WriteString("<b>" + html.EscapeString(n.Title) + "</b>")
	if n.Server != "" {
		b.WriteString("\n<i>" + html.EscapeString(n.Server) + "</i>")
	}
	if n.Text != "" {
		b.WriteString("\n" + html.EscapeString(limit(n.Text, 1500)))
	}
	for _, f := range n.Fields {
		if len(f.Value) < 40 {
			b.WriteString("\n<b>" + html.EscapeString(f.Name) + ":</b> " + html.EscapeString(f.Value))
		} else {
			b.WriteString("\n<b>" + html.EscapeString(f.Name) + ":</b>\n<pre>" + html.EscapeString(limit(f.Value, 900)) + "</pre>")
		}
	}
	return b.String()
}

func (s sender) sendTelegram(ctx context.Context, c Channel, n notice) error {
	message := map[string]any{"chat_id": c.Settings.ChatID, "text": telegramText(n), "parse_mode": "HTML",
		"disable_web_page_preview": true}
	if c.Settings.ThreadID > 0 {
		message["message_thread_id"] = c.Settings.ThreadID
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	endpoint := telegramAPI + "/bot" + c.secret.Token + "/sendMessage"
	return s.deliver(ctx, s.pick(c.Settings.Proxy, true), "Telegram", c.secret.Token, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
}

// ----- any HTTP endpoint -----

// DefaultBody is the JSON an HTTP channel sends unless told otherwise.
const DefaultBody = `{
  "event": "{event}",
  "kind": "{kind}",
  "severity": "{severity}",
  "server": "{server}",
  "server_id": "{server_id}",
  "title": "{title}",
  "text": "{text}",
  "time": "{time}",
  "details": {details}
}`

var bodyPlaceholder = regexp.MustCompile(`\{(event|kind|severity|server|server_id|title|text|time|details)\}`)

// values are what the placeholders of an HTTP channel stand for. text is
// the server's message where there is one, else the title.
func (n notice) values() map[string]string {
	text := n.Text
	if text == "" {
		text = n.Title
	}
	details := map[string]string{}
	for _, f := range n.Fields {
		details[f.Key] = f.Value
	}
	raw, _ := json.Marshal(details)
	return map[string]string{"event": n.Event, "kind": n.Kind, "severity": n.Severity, "server": n.Server,
		"server_id": n.ServerID, "title": n.Title, "text": text, "time": n.Time.Format(time.RFC3339), "details": string(raw)}
}

// fill puts the values into a template, each escaped as where it goes: a
// JSON string's content, a form or URL query value, or plain text. {details}
// in JSON is an object and goes in as it is.
func fill(template string, values map[string]string, escape string) string {
	return bodyPlaceholder.ReplaceAllStringFunc(template, func(p string) string {
		key := p[1 : len(p)-1]
		value := values[key]
		switch escape {
		case "application/json":
			if key == "details" {
				return value
			}
			quoted, _ := json.Marshal(value)
			return string(quoted[1 : len(quoted)-1])
		case "application/x-www-form-urlencoded", "url":
			return url.QueryEscape(value)
		}
		return value
	})
}

// sampleNotice fills templates when a channel is checked and tested.
func sampleNotice() notice {
	return notice{Title: "Тест", Server: "PvE \"1\"", ServerID: "pve", Text: "Строка\nс \"кавычками\"", Event: "manager.test",
		Severity: "info", Time: time.Unix(0, 0).UTC(), Fields: []noticeField{{Name: "Причина", Key: "reason", Value: "тест"}}}
}

func (c *Channel) validateHTTP() error {
	s := &c.Settings
	s.Method = strings.ToUpper(strings.TrimSpace(s.Method))
	if s.Method == "" {
		s.Method = "POST"
	}
	if s.ContentType == "" {
		s.ContentType = "application/json"
	}
	switch {
	case !methods[s.Method]:
		return fmt.Errorf("method must be GET, POST, PUT, PATCH or DELETE")
	case !contentTypes[s.ContentType]:
		return fmt.Errorf("content_type must be application/json, application/x-www-form-urlencoded or text/plain")
	case len(s.Body) > 8192:
		return fmt.Errorf("the body template is at most 8192 bytes")
	case c.secret.URL == "":
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(fill(c.secret.URL, sampleNotice().values(), "url"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Fragment != "" {
		return fmt.Errorf("url must be an http:// or https:// address")
	}
	if u.User != nil {
		return fmt.Errorf("put credentials in a header, not in the url")
	}
	if s.ContentType == "application/json" && strings.TrimSpace(s.Body) != "" && s.Method != "GET" {
		if sample := fill(s.Body, sampleNotice().values(), s.ContentType); !json.Valid([]byte(sample)) {
			return fmt.Errorf("the body template does not make valid JSON; write placeholders inside quotes, as \"{text}\", except {details}")
		}
	}
	return nil
}

func (s sender) sendHTTP(ctx context.Context, c Channel, n notice) error {
	values := n.values()
	endpoint := fill(c.secret.URL, values, "url")
	var body []byte
	if c.Settings.Method != "GET" && strings.TrimSpace(c.Settings.Body) != "" {
		body = []byte(fill(c.Settings.Body, values, c.Settings.ContentType))
	}
	client := s.pick(c.Settings.Proxy, c.Settings.AllowPrivate)
	if c.Settings.Proxy && !c.Settings.AllowPrivate {
		// Through a proxy the address is only known to the proxy; the name is
		// checked here instead.
		if err := checkPublicHost(ctx, endpoint); err != nil {
			return err
		}
	}
	return s.deliver(ctx, client, "the endpoint", c.secret.URL, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, c.Settings.Method, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", c.Settings.ContentType+"; charset=utf-8")
		}
		req.Header.Set("User-Agent", "conan-manager")
		for _, h := range c.secret.Headers {
			req.Header.Set(h.Name, h.Value)
		}
		return req, nil
	})
}

// ----- where requests may go -----

var errPrivateAddress = errors.New("the address is local or private; allow it in the channel's settings if that is intended")

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// publicIP says whether an address is on the internet, rather than the host
// itself, its networks, or a cloud's metadata service.
func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip))
}

// guardedClient connects only to public addresses. The check is made on the
// address actually dialled, so a name that resolves elsewhere later, or a
// redirect, cannot get around it.
func guardedClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || !publicIP(ip) {
			return errPrivateAddress
		}
		return nil
	}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: noRedirects}
}

// noRedirects keeps headers from following a redirect to another host: the
// endpoint is expected to answer itself.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func checkPublicHost(ctx context.Context, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		if !publicIP(ip) {
			return errPrivateAddress
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		// The proxy may resolve names the host cannot; let it try.
		return nil
	}
	for _, a := range addrs {
		if !publicIP(a.IP) {
			return errPrivateAddress
		}
	}
	return nil
}
