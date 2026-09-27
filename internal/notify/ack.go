package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/davidsugianto/upsera/internal/model"
)

// AckFunc processes an Acknowledge button press. source and byName record
// who pressed it.
type AckFunc func(alertID string, source model.AckSource, byName string) error

// errHTTPConflict marks a Telegram getUpdates 409, which means another
// poller is already using this bot token's long-poll offset.
var errHTTPConflict = errors.New("telegram: 409 conflict")

// errAckNotConfigured answers a button pressed in a chat or channel that no
// Upsera channel is configured for, so the press is not silently dropped.
const errAckNotConfigured = "This chat is not configured for Upsera acknowledgements."

// telegramUpdatesLimit caps one getUpdates batch so it always fits in
// maxResponseBytes.
const telegramUpdatesLimit = 20

// telegramListener polls getUpdates for one bot token. chatIDs is the set
// of chat_id strings whose callback presses are accepted; it is swapped
// on every Sync without restarting the poller.
type telegramListener struct {
	token    string
	mu       sync.Mutex
	chatIDs  map[string]struct{}
	cancel   context.CancelFunc
	warnOnce sync.Once
}

// slackListener runs one Socket Mode connection for one app-level token.
// channels is the set of Slack channel ids whose Acknowledge presses are
// accepted; it is swapped on every Sync without restarting the socket.
type slackListener struct {
	appToken string
	mu       sync.Mutex
	channels map[string]struct{}
	cancel   context.CancelFunc
}

// AckListeners runs one Telegram long-poller per distinct bot token and
// one Slack Socket Mode connection per distinct app-level token, routing
// Acknowledge button presses to onAck. Sync starts and stops listeners to
// match the configured channels; Close stops them all.
type AckListeners struct {
	opts  Options
	onAck AckFunc
	log   *slog.Logger

	telegramClient *http.Client
	slackClient    *http.Client

	// Backoffs, overridable in tests so they run in milliseconds.
	telegramPollBackoff     time.Duration
	telegramConflictBackoff time.Duration
	slackReconnectBackoff   time.Duration

	mu       sync.Mutex
	closed   bool
	telegram map[string]*telegramListener
	slack    map[string]*slackListener
	wg       sync.WaitGroup
}

// NewAckListeners returns an AckListeners with no listeners running; call
// Sync to start them.
func NewAckListeners(opts Options, onAck AckFunc, log *slog.Logger) *AckListeners {
	opts = opts.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	// Long polls hold the request for up to 50s: same client as the
	// notifiers, with a longer timeout.
	telegramClient := newHTTPClient(opts.Policy)
	telegramClient.Timeout = 60 * time.Second
	return &AckListeners{
		opts:                    opts,
		onAck:                   onAck,
		log:                     log,
		telegramClient:          telegramClient,
		slackClient:             newHTTPClient(opts.Policy),
		telegramPollBackoff:     5 * time.Second,
		telegramConflictBackoff: 30 * time.Second,
		slackReconnectBackoff:   5 * time.Second,
		telegram:                make(map[string]*telegramListener),
		slack:                   make(map[string]*slackListener),
	}
}

// Sync starts a poller/socket for every distinct telegram bot_token /
// slack_app app_token among chans, updates the accepted chat/channel ids
// of ones already running, and stops ones no longer configured. Channels
// with a DecryptErr or nil Config are skipped. Sync may be called
// repeatedly and concurrently with Close.
func (l *AckListeners) Sync(ctx context.Context, chans []model.Channel) {
	wantTelegram := make(map[string]map[string]struct{})
	wantSlack := make(map[string]map[string]struct{})
	for _, ch := range chans {
		if ch.DecryptErr != "" || ch.Config == nil {
			continue
		}
		switch ch.Type {
		case model.ChannelTelegram:
			var c telegramConfig
			if err := json.Unmarshal(ch.Config, &c); err != nil || c.BotToken == "" {
				continue
			}
			if wantTelegram[c.BotToken] == nil {
				wantTelegram[c.BotToken] = make(map[string]struct{})
			}
			wantTelegram[c.BotToken][c.ChatID] = struct{}{}
		case model.ChannelSlackApp:
			var c slackAppConfig
			if err := json.Unmarshal(ch.Config, &c); err != nil || c.AppToken == "" {
				continue
			}
			if wantSlack[c.AppToken] == nil {
				wantSlack[c.AppToken] = make(map[string]struct{})
			}
			wantSlack[c.AppToken][c.Channel] = struct{}{}
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}

	for token, ids := range wantTelegram {
		if tl, ok := l.telegram[token]; ok {
			tl.mu.Lock()
			tl.chatIDs = ids
			tl.mu.Unlock()
			continue
		}
		lctx, cancel := context.WithCancel(ctx)
		tl := &telegramListener{token: token, chatIDs: ids, cancel: cancel}
		l.telegram[token] = tl
		l.wg.Add(1)
		go l.runTelegram(lctx, tl)
	}
	for token, tl := range l.telegram {
		if _, ok := wantTelegram[token]; !ok {
			tl.cancel()
			delete(l.telegram, token)
		}
	}

	for token, ids := range wantSlack {
		if sl, ok := l.slack[token]; ok {
			sl.mu.Lock()
			sl.channels = ids
			sl.mu.Unlock()
			continue
		}
		lctx, cancel := context.WithCancel(ctx)
		sl := &slackListener{appToken: token, channels: ids, cancel: cancel}
		l.slack[token] = sl
		l.wg.Add(1)
		go l.runSlack(lctx, sl)
	}
	for token, sl := range l.slack {
		if _, ok := wantSlack[token]; !ok {
			sl.cancel()
			delete(l.slack, token)
		}
	}
}

// Close stops every listener and waits for their goroutines to exit.
func (l *AckListeners) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	for _, tl := range l.telegram {
		tl.cancel()
	}
	for _, sl := range l.slack {
		sl.cancel()
	}
	l.telegram = make(map[string]*telegramListener)
	l.slack = make(map[string]*slackListener)
	l.mu.Unlock()
	l.wg.Wait()
}

// --- Telegram long-polling ---

type telegramUpdate struct {
	UpdateID      int64 `json:"update_id"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		From struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"from"`
		Message struct {
			Chat struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"chat"`
		} `json:"message"`
		Data string `json:"data"`
	} `json:"callback_query"`
}

func (l *AckListeners) runTelegram(ctx context.Context, tl *telegramListener) {
	defer l.wg.Done()
	var offset int64
	for ctx.Err() == nil {
		updates, err := l.fetchTelegramUpdates(ctx, tl.token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := l.telegramPollBackoff
			if errors.Is(err, errHTTPConflict) {
				tl.warnOnce.Do(func() {
					l.log.Warn("notify: telegram getUpdates conflict, another poller may be using this bot token")
				})
				wait = l.telegramConflictBackoff
			} else {
				l.log.Warn("notify: telegram poll failed", "err", err)
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			l.handleTelegramUpdate(ctx, tl, u)
		}
	}
}

func (l *AckListeners) fetchTelegramUpdates(ctx context.Context, token string, offset int64) ([]telegramUpdate, error) {
	q := url.Values{}
	q.Set("timeout", "50")
	q.Set("offset", strconv.FormatInt(offset, 10))
	q.Set("allowed_updates", `["callback_query"]`)
	// A full batch (100 updates of ~1 KiB) would not fit the response cap
	// and the poller would retry the same offset forever.
	q.Set("limit", strconv.Itoa(telegramUpdatesLimit))
	u := l.opts.TelegramAPIURL + "/bot" + token + "/getUpdates?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.New("invalid request URL")
	}
	req.Header.Set("User-Agent", "Upsera")
	resp, err := l.telegramClient.Do(req)
	if err != nil {
		return nil, describe(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, describe(err)
	}
	if resp.StatusCode == http.StatusConflict {
		return nil, errHTTPConflict
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var out struct {
		Result []telegramUpdate `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Result, nil
}

func (l *AckListeners) handleTelegramUpdate(ctx context.Context, tl *telegramListener, u telegramUpdate) {
	cq := u.CallbackQuery
	if cq == nil || !strings.HasPrefix(cq.Data, "ack:") {
		return
	}
	// chat_id may be configured as the numeric id or as @username.
	chat := cq.Message.Chat
	tl.mu.Lock()
	_, accepted := tl.chatIDs[strconv.FormatInt(chat.ID, 10)]
	if !accepted && chat.Username != "" {
		_, accepted = tl.chatIDs["@"+chat.Username]
	}
	tl.mu.Unlock()
	text := errAckNotConfigured
	if accepted {
		alertID := strings.TrimPrefix(cq.Data, "ack:")
		name := cq.From.Username
		if name == "" {
			name = cq.From.FirstName
		}
		text = "Acknowledged"
		if err := l.onAck(alertID, model.AckTelegram, name); err != nil {
			text = err.Error()
		}
	}
	_, _ = postJSON(ctx, l.telegramClient, l.opts.TelegramAPIURL+"/bot"+tl.token+"/answerCallbackQuery", nil,
		map[string]string{"callback_query_id": cq.ID, "text": text})
}

// --- Slack Socket Mode ---

// slackFrame is the envelope of every Socket Mode message: interactive
// actions, events, slash commands and disconnect notices all share it.
type slackFrame struct {
	Type       string          `json:"type"`
	EnvelopeID string          `json:"envelope_id"`
	Payload    json.RawMessage `json:"payload"`
}

type slackInteractivePayload struct {
	Type    string `json:"type"`
	Channel struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
	User struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	ResponseURL string `json:"response_url"`
}

func (l *AckListeners) runSlack(ctx context.Context, sl *slackListener) {
	defer l.wg.Done()
	for ctx.Err() == nil {
		wsURL, err := l.openSlackConnection(ctx, sl.appToken)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			l.log.Warn("notify: slack apps.connections.open failed", "err", err)
			if !sleepOrDone(ctx, l.slackReconnectBackoff) {
				return
			}
			continue
		}
		err = l.slackReadLoop(ctx, sl, wsURL)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			l.log.Warn("notify: slack socket disconnected", "err", err)
		}
		if !sleepOrDone(ctx, l.slackReconnectBackoff) {
			return
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *AckListeners) openSlackConnection(ctx context.Context, appToken string) (string, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := slackCall(ctx, l.slackClient, l.opts.SlackAPIURL+"/apps.connections.open", appToken, map[string]any{}, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

func (l *AckListeners) slackReadLoop(ctx context.Context, sl *slackListener, wsURL string) error {
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: newHTTPClient(l.opts.Policy)})
	if err != nil {
		return describe(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	for {
		var frame slackFrame
		if err := wsjson.Read(ctx, conn, &frame); err != nil {
			return err
		}
		if frame.EnvelopeID != "" {
			if err := wsjson.Write(ctx, conn, map[string]string{"envelope_id": frame.EnvelopeID}); err != nil {
				return err
			}
		}
		switch frame.Type {
		case "disconnect":
			return nil
		case "interactive":
			l.handleSlackInteractive(ctx, sl, frame.Payload)
		}
	}
}

func (l *AckListeners) handleSlackInteractive(ctx context.Context, sl *slackListener, payload json.RawMessage) {
	var p slackInteractivePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return
	}
	if p.Type != "block_actions" {
		return
	}
	// channel may be configured as the id (C0123) or the name (#alerts).
	sl.mu.Lock()
	_, accepted := sl.channels[p.Channel.ID]
	if !accepted && p.Channel.Name != "" {
		_, byName := sl.channels[p.Channel.Name]
		_, byHash := sl.channels["#"+p.Channel.Name]
		accepted = byName || byHash
	}
	sl.mu.Unlock()
	var value string
	found := false
	for _, a := range p.Actions {
		if a.ActionID == slackAckActionID {
			value, found = a.Value, true
			break
		}
	}
	if !found {
		return
	}
	name := p.User.Username
	if name == "" {
		name = p.User.Name
	}
	text := errAckNotConfigured
	if accepted {
		if err := l.onAck(value, model.AckSlack, name); err != nil {
			text = err.Error()
		} else {
			text = "Acknowledged by " + name
		}
	}
	if p.ResponseURL == "" {
		return
	}
	_, _ = postJSON(ctx, l.slackClient, p.ResponseURL, nil, map[string]any{"replace_original": false, "text": text})
}
