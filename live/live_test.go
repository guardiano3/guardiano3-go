package live

import (
	"reflect"
	"testing"
)

func TestHTTPBaseURLsBecomeWebSocketURLs(t *testing.T) {
	cases := map[string]string{
		"https://api.guardiano3.com":  "wss://api.guardiano3.com",
		"https://api.guardiano3.com/": "wss://api.guardiano3.com",
		"http://localhost:8000":       "ws://localhost:8000",
	}
	for in, want := range cases {
		if got := httpToWS(in); got != want {
			t.Errorf("httpToWS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewSessionAddressesBothChannels(t *testing.T) {
	s := NewSession("https://api.guardiano3.com/", "go3_test_x", "sess_1", Callbacks{})
	if s.audioURL != "wss://api.guardiano3.com/v1/sessions/sess_1/audio" {
		t.Errorf("audio URL = %q", s.audioURL)
	}
	if s.streamURL != "wss://api.guardiano3.com/v1/sessions/sess_1/stream" {
		t.Errorf("stream URL = %q", s.streamURL)
	}
}

// Frames arrive as decoded JSON, so numbers are float64 -- the parser must
// read them as such, or every score silently comes back empty.
func TestDecisionFramesAreParsed(t *testing.T) {
	f := parseFrame(map[string]any{
		"type":               "decision",
		"sessionId":          "sess_1",
		"riskLevel":          "high",
		"riskScore":          78.5,
		"fraudCategory":      "otp_fraud",
		"verificationStatus": "pending",
		"transcript":         "share the OTP",
		"explanation":        []any{"asks for an OTP", 42},
	})
	if f.Type != "decision" || f.SessionID != "sess_1" || f.RiskLevel != "high" {
		t.Fatalf("unexpected frame: %+v", f)
	}
	if f.RiskScore == nil || *f.RiskScore != 78.5 {
		t.Errorf("RiskScore = %v, want 78.5", f.RiskScore)
	}
	if f.FraudCategory != "otp_fraud" || f.VerificationStatus != "pending" || f.Transcript != "share the OTP" {
		t.Errorf("unexpected frame: %+v", f)
	}
	// A non-string entry is skipped rather than failing the frame.
	if !reflect.DeepEqual(f.Explanation, []string{"asks for an OTP"}) {
		t.Errorf("Explanation = %v", f.Explanation)
	}
}

func TestADecisionWithoutAScoreHasANilScore(t *testing.T) {
	if f := parseFrame(map[string]any{"type": "decisionUpdate"}); f.RiskScore != nil {
		t.Errorf("RiskScore = %v, want nil", *f.RiskScore)
	}
}

func TestDegradedLaggingAndErrorFramesAreParsed(t *testing.T) {
	d := parseFrame(map[string]any{
		"type": "degraded", "reason": "transcription_timeout",
		"unheardSeconds": 4.0, "totalUnheardSeconds": 9.5,
	})
	if d.DegradedReason != "transcription_timeout" || d.UnheardSeconds != 4 || d.TotalUnheardSeconds != 9.5 {
		t.Errorf("degraded frame: %+v", d)
	}

	l := parseFrame(map[string]any{"type": "lagging", "droppedChunks": 3.0})
	if l.DroppedChunks != 3 {
		t.Errorf("DroppedChunks = %d, want 3", l.DroppedChunks)
	}

	e := parseFrame(map[string]any{
		"type":  "error",
		"error": map[string]any{"code": "SESSION_NOT_FOUND", "message": "no such session"},
	})
	if e.ErrorCode != "SESSION_NOT_FOUND" || e.ErrorMessage != "no such session" {
		t.Errorf("error frame: %+v", e)
	}
}

func TestTheRawFrameIsKept(t *testing.T) {
	raw := map[string]any{"type": "ping", "future": true}
	if f := parseFrame(raw); f.Raw["future"] != true || f.Type != "ping" {
		t.Errorf("frame lost data: %+v", f)
	}
}
