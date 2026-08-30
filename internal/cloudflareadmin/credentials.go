package cloudflareadmin

import (
	"bytes"
	"fmt"
	"strings"
)

const maximumCredentialFileBytes = 64 << 10

type credentials struct {
	apiToken  string
	accountID string
}

func readCredentials(path string) (credentials, error) {
	content, err := readPrivateRegularFile(path, maximumCredentialFileBytes)
	if err != nil {
		return credentials{}, fmt.Errorf("read Cloudflare credentials: %w", err)
	}
	return parseCredentials(content)
}

func parseCredentials(content []byte) (credentials, error) {
	var result credentials
	var tokenSeen, accountSeen bool
	for _, raw := range bytes.Split(content, []byte{'\n'}) {
		line := string(raw)
		switch {
		case strings.HasPrefix(line, "CLOUDFLARE_API_TOKEN="):
			if tokenSeen {
				return credentials{}, fmt.Errorf("duplicate CLOUDFLARE_API_TOKEN")
			}
			tokenSeen = true
			result.apiToken = strings.TrimPrefix(line, "CLOUDFLARE_API_TOKEN=")
		case strings.HasPrefix(line, "CLOUDFLARE_ACCOUNT_ID="):
			if accountSeen {
				return credentials{}, fmt.Errorf("duplicate CLOUDFLARE_ACCOUNT_ID")
			}
			accountSeen = true
			result.accountID = strings.TrimPrefix(line, "CLOUDFLARE_ACCOUNT_ID=")
		}
	}
	if !tokenSeen || result.apiToken == "" {
		return credentials{}, fmt.Errorf("CLOUDFLARE_API_TOKEN is missing")
	}
	if !accountSeen || result.accountID == "" {
		return credentials{}, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID is missing")
	}
	if strings.ContainsAny(result.apiToken, "\x00\r") {
		return credentials{}, fmt.Errorf("CLOUDFLARE_API_TOKEN contains an invalid byte")
	}
	if len(result.accountID) != 32 {
		return credentials{}, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID must be 32 lowercase hexadecimal characters")
	}
	for _, character := range result.accountID {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return credentials{}, fmt.Errorf("CLOUDFLARE_ACCOUNT_ID must be 32 lowercase hexadecimal characters")
			}
		}
	}
	return result, nil
}
