package helps

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// DeleteJSONField removes a top-level or nested JSON field from a payload.
func DeleteJSONField(body []byte, key string) []byte {
	if key == "" || len(body) == 0 {
		return body
	}
	updated, err := sjson.DeleteBytes(body, key)
	if err != nil {
		return body
	}
	return updated
}

// ParseRetryDelay extracts the retry delay from a Google API 429 error response.
func ParseRetryDelay(errorBody []byte) (*time.Duration, error) {
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.RetryInfo" {
				continue
			}
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(retryDelay)
			if err != nil {
				return nil, fmt.Errorf("failed to parse duration")
			}
			return &duration, nil
		}

		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
				continue
			}
			if quotaResetDelay := detail.Get("metadata.quotaResetDelay").String(); quotaResetDelay != "" {
				if duration, err := time.ParseDuration(quotaResetDelay); err == nil {
					return &duration, nil
				}
			}
			// Absolute reset time, e.g. "2026-10-12T05:00:00Z".
			if stamp := detail.Get("metadata.quotaResetTimeStamp").String(); stamp != "" {
				if reset, err := time.Parse(time.RFC3339, stamp); err == nil {
					if duration := time.Until(reset); duration > 0 {
						return &duration, nil
					}
				}
			}
		}
	}

	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		re := regexp.MustCompile(`(?:after|resets in)\s+(\d+)s\.?`)
		if matches := re.FindStringSubmatch(message); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil {
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
		// "Your quota will reset after 4h59m32s." / "Individual quota reached ... Resets in 126h19m36s."
		reHuman := regexp.MustCompile(`(?:after|resets in)\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)\.?`)
		if matches := reHuman.FindStringSubmatch(strings.ToLower(message)); len(matches) > 1 {
			duration, err := time.ParseDuration(matches[1])
			if err == nil && duration > 0 {
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}

// ParseRetryAfterHeader parses an HTTP Retry-After header (delta-seconds or HTTP-date).
func ParseRetryAfterHeader(value string) (*time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		duration := time.Duration(seconds) * time.Second
		return &duration, true
	}
	if at, err := http.ParseTime(value); err == nil {
		if duration := time.Until(at); duration > 0 {
			return &duration, true
		}
	}
	return nil, false
}
