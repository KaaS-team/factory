package intake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// SignSlack returns the signature header value for body at time ts. Tests use it.
func SignSlack(secret string, ts time.Time, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(m, "v0:%d:%s", ts.Unix(), body)
	return "v0=" + hex.EncodeToString(m.Sum(nil))
}

// SignSentry returns the Sentry-Hook-Signature value for body. Tests use it.
func SignSentry(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}
