package main

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	maxLogEntries = 200
	maxLogBody    = 64 << 10
)

type callEntry struct {
	ID            int       `json:"id"`
	Time          time.Time `json:"time"`
	Method        string    `json:"method"`
	URL           string    `json:"url"`
	Status        int       `json:"status"`
	DurationMs    int64     `json:"durationMs"`
	InteractionID string    `json:"interactionId,omitempty"`
	Signed        bool      `json:"signed"`
	RequestBody   string    `json:"requestBody,omitempty"`
	ResponseBody  string    `json:"responseBody,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// callRecorder wraps the mTLS transport and keeps the last requests to S-Pankki for the dashboard.
type callRecorder struct {
	next    http.RoundTripper
	mu      sync.Mutex
	seq     int
	entries []callEntry
}

func (c *callRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil && req.Body != http.NoBody {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body.Close()
		clone := req.Clone(req.Context())
		clone.Body = io.NopCloser(bytes.NewReader(reqBody))
		clone.ContentLength = int64(len(reqBody))
		req = clone
	}
	entry := callEntry{
		Time:          time.Now(),
		Method:        req.Method,
		URL:           req.URL.String(),
		InteractionID: req.Header.Get("x-fapi-interaction-id"),
		Signed:        req.Header.Get("x-jws-signature") != "",
		RequestBody:   redact(reqBody),
	}
	resp, err := c.next.RoundTrip(req)
	entry.DurationMs = time.Since(entry.Time).Milliseconds()
	if err != nil {
		entry.Error = err.Error()
	} else {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		entry.Status = resp.StatusCode
		entry.ResponseBody = redact(respBody)
	}
	c.add(entry)
	return resp, err
}

func (c *callRecorder) add(e callEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	e.ID = c.seq
	c.entries = append(c.entries, e)
	if len(c.entries) > maxLogEntries {
		c.entries = c.entries[len(c.entries)-maxLogEntries:]
	}
}

// list returns entries newer than afterID, newest first.
func (c *callRecorder) list(afterID int) []callEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []callEntry{}
	for i := len(c.entries) - 1; i >= 0 && c.entries[i].ID > afterID; i-- {
		out = append(out, c.entries[i])
	}
	return out
}

func (c *callRecorder) clear() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

var (
	jsonSecret = regexp.MustCompile(`"(access_token|refresh_token|id_token)"\s*:\s*"[^"]*"`)
	formSecret = regexp.MustCompile(`\b(code|refresh_token)=[^&\s]+`)
)

func redact(body []byte) string {
	if len(body) > maxLogBody {
		body = append(body[:maxLogBody:maxLogBody], "…(truncated)"...)
	}
	s := jsonSecret.ReplaceAllString(string(body), `"$1":"«redacted»"`)
	return formSecret.ReplaceAllString(s, `$1=«redacted»`)
}
