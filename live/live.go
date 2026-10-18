// Package live manages the two WebSocket channels for real-time call analysis:
// binary audio uplink and typed JSON decision downlink.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// Frame is a parsed decision stream frame. Type is the discriminator;
// Raw holds the full decoded JSON for fields not broken out here.
type Frame struct {
	Type               string
	Raw                map[string]any
	SessionID          string
	RiskLevel          string
	RiskScore          *float64
	FraudCategory      string
	VerificationStatus string
	Transcript         string
	Explanation        []string
	DroppedChunks      int
	// Degraded frames: a stretch of the call was not transcribed, so no
	// decision covers it. Do not read a quiet stream as a safe call.
	DegradedReason      string
	UnheardSeconds      float64
	TotalUnheardSeconds float64
	ErrorCode           string
	ErrorMessage        string
}

func parseFrame(data map[string]any) Frame {
	f := Frame{Raw: data}
	if t, ok := data["type"].(string); ok {
		f.Type = t
	}
	if s, ok := data["sessionId"].(string); ok {
		f.SessionID = s
	}

	switch f.Type {
	case "decision", "decisionUpdate":
		if v, ok := data["riskLevel"].(string); ok {
			f.RiskLevel = v
		}
		if v, ok := data["riskScore"].(float64); ok {
			f.RiskScore = &v
		}
		if v, ok := data["fraudCategory"].(string); ok {
			f.FraudCategory = v
		}
		if v, ok := data["verificationStatus"].(string); ok {
			f.VerificationStatus = v
		}
		if v, ok := data["transcript"].(string); ok {
			f.Transcript = v
		}
		if arr, ok := data["explanation"].([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					f.Explanation = append(f.Explanation, s)
				}
			}
		}
	case "lagging":
		if v, ok := data["droppedChunks"].(float64); ok {
			f.DroppedChunks = int(v)
		}
	case "degraded":
		if v, ok := data["reason"].(string); ok {
			f.DegradedReason = v
		}
		if v, ok := data["unheardSeconds"].(float64); ok {
			f.UnheardSeconds = v
		}
		if v, ok := data["totalUnheardSeconds"].(float64); ok {
			f.TotalUnheardSeconds = v
		}
	case "error":
		if errObj, ok := data["error"].(map[string]any); ok {
			if c, ok := errObj["code"].(string); ok {
				f.ErrorCode = c
			}
			if m, ok := errObj["message"].(string); ok {
				f.ErrorMessage = m
			}
		}
	}
	return f
}

// Callbacks receives decision stream events. Implement only the ones you need.
type Callbacks struct {
	OnReady          func(Frame)
	OnDecision       func(Frame)
	OnDecisionUpdate func(Frame)
	OnLagging        func(Frame)
	OnDegraded       func(Frame)
	OnError          func(Frame)
	OnDisconnect     func(code int, reason string)
}

// Session manages the two WebSocket channels for a live call analysis session.
type Session struct {
	audioURL   string
	streamURL  string
	credential string
	callbacks  Callbacks

	mu       sync.Mutex
	audioWS  *websocket.Conn
	streamWS *websocket.Conn
	closed   bool
}

func httpToWS(url string) string {
	url = strings.Replace(url, "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1)
	return strings.TrimRight(url, "/")
}

// NewSession creates a live session helper. Call Connect to open the channels.
func NewSession(baseURL, credential, sessionID string, cb Callbacks) *Session {
	wsBase := httpToWS(baseURL)
	return &Session{
		audioURL:   wsBase + "/v1/sessions/" + sessionID + "/audio",
		streamURL:  wsBase + "/v1/sessions/" + sessionID + "/stream",
		credential: credential,
		callbacks:  cb,
	}
}

// Connect opens both WebSocket connections, sends the auth frame, and starts
// reading decision frames in a goroutine. The context controls the connection
// lifetime.
func (s *Session) Connect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("live.Session is closed")
	}

	authMsg, _ := json.Marshal(map[string]string{"token": s.credential})

	audioConn, _, err := websocket.Dial(ctx, s.audioURL, nil)
	if err != nil {
		return fmt.Errorf("audio dial: %w", err)
	}
	if err := audioConn.Write(ctx, websocket.MessageText, authMsg); err != nil {
		audioConn.CloseNow()
		return fmt.Errorf("audio auth: %w", err)
	}
	s.audioWS = audioConn

	streamConn, _, err := websocket.Dial(ctx, s.streamURL, nil)
	if err != nil {
		audioConn.CloseNow()
		return fmt.Errorf("stream dial: %w", err)
	}
	if err := streamConn.Write(ctx, websocket.MessageText, authMsg); err != nil {
		audioConn.CloseNow()
		streamConn.CloseNow()
		return fmt.Errorf("stream auth: %w", err)
	}
	s.streamWS = streamConn

	go s.readLoop(ctx, streamConn)
	return nil
}

// SendAudio sends raw PCM audio bytes (16-bit LE, 16kHz mono).
func (s *Session) SendAudio(ctx context.Context, pcm []byte) error {
	s.mu.Lock()
	ws := s.audioWS
	s.mu.Unlock()
	if ws == nil {
		return fmt.Errorf("not connected")
	}
	return ws.Write(ctx, websocket.MessageBinary, pcm)
}

// Close shuts down both channels.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.audioWS != nil {
		s.audioWS.Close(websocket.StatusNormalClosure, "")
		s.audioWS = nil
	}
	if s.streamWS != nil {
		s.streamWS.Close(websocket.StatusNormalClosure, "")
		s.streamWS = nil
	}
}

func (s *Session) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			if s.callbacks.OnDisconnect != nil && !s.closed {
				s.callbacks.OnDisconnect(1006, err.Error())
			}
			return
		}
		var data map[string]any
		if json.Unmarshal(msg, &data) != nil {
			continue
		}
		frame := parseFrame(data)
		switch frame.Type {
		case "ready":
			if s.callbacks.OnReady != nil {
				s.callbacks.OnReady(frame)
			}
		case "decision":
			if s.callbacks.OnDecision != nil {
				s.callbacks.OnDecision(frame)
			}
		case "decisionUpdate":
			if s.callbacks.OnDecisionUpdate != nil {
				s.callbacks.OnDecisionUpdate(frame)
			}
		case "lagging":
			if s.callbacks.OnLagging != nil {
				s.callbacks.OnLagging(frame)
			}
		case "degraded":
			if s.callbacks.OnDegraded != nil {
				s.callbacks.OnDegraded(frame)
			}
		case "error":
			if s.callbacks.OnError != nil {
				s.callbacks.OnError(frame)
			}
		}
	}
}
