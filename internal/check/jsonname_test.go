package check

import "testing"

// TestJSONName pins the automatic json key rule of ADR 0036.
func TestJSONName(t *testing.T) {
	tests := []struct {
		name, key string
		split     bool // W0008: an exception that needs a tag
	}{
		{"Name", "name", false},
		{"ID", "id", false},
		{"URL", "url", false},
		{"UserID", "userID", false},
		{"UserIDs", "userIDs", false},
		{"IDs", "ids", false},
		{"URLs", "urls", false},
		{"SKUs", "skus", false},
		{"UIDsByHost", "uidsByHost", false},
		{"HTTPServer", "httpServer", false},
		{"HTTPSPort", "httpsPort", false},
		{"APIKey", "apiKey", false},
		{"TTLSeconds", "ttlSeconds", false},
		{"IPv4", "ipv4", false},
		{"IPv4Addr", "ipv4Addr", false},
		{"UTF8Name", "utf8Name", false},
		{"HTTP2", "http2", false},
		{"DBName", "dbName", false},
		{"XMLHTTPRequest", "xmlhttpRequest", false},
		{"ETag", "eTag", true},
		{"OAuthToken", "oAuthToken", true},
		{"XCoord", "xCoord", true},
		{"A", "a", false},
		{"ÄName", "äName", true},
		{"ÄBCd", "äbCd", false},
		{"ÉTag", "éTag", true},
		{"ÄÖ", "äö", false},
	}
	for _, tt := range tests {
		if got := JSONName(tt.name); got != tt.key {
			t.Errorf("JSONName(%s) = %s, want %s", tt.name, got, tt.key)
		}
		if got := JSONKeySplitsRun(tt.name); got != tt.split {
			t.Errorf("JSONKeySplitsRun(%s) = %v, want %v", tt.name, got, tt.split)
		}
	}
}

// TestJSONNameBefore0036 pins the rule W0009 compares against: the json
// key rule before ADR 0036.
func TestJSONNameBefore0036(t *testing.T) {
	for name, key := range map[string]string{
		"ID": "id", "UserID": "userID", "IDs": "iDs", "URLs": "urLs", "IPv4": "iPv4",
		"HTTPServer": "httpServer", "UTF8Name": "utf8Name", "ETag": "eTag", "ÄBCd": "äbCd", "Name": "name",
	} {
		if got := jsonNameBefore0036(name); got != key {
			t.Errorf("jsonNameBefore0036(%s) = %s, want %s", name, got, key)
		}
	}
}
