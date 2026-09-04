package cloudflareadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	cloudflareAPIBase    = "https://api.cloudflare.com/client/v4"
	zoneName             = "darkfactory.build"
	appRecordName        = "app.darkfactory.build"
	appRecordContent     = "76.76.21.21"
	maximumResponseBytes = 1 << 20
)

type apiClient struct {
	httpClient *http.Client
	baseURL    string
	token      string
	accountID  string
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type resultInfo struct {
	Count      int `json:"count"`
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalCount int `json:"total_count"`
	TotalPages int `json:"total_pages"`
}

type envelope[T any] struct {
	Success    bool        `json:"success"`
	Errors     []apiError  `json:"errors"`
	Result     T           `json:"result"`
	ResultInfo *resultInfo `json:"result_info"`
}

type zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account struct {
		ID string `json:"id"`
	} `json:"account"`
}

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied *bool  `json:"proxied"`
	TTL     int    `json:"ttl"`
}

type recordOutput struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

func (client apiClient) lookupZone(ctx context.Context) (zone, error) {
	query := url.Values{}
	query.Set("name", zoneName)
	query.Set("status", "active")
	query.Set("page", "1")
	query.Set("per_page", "2")
	var response envelope[[]zone]
	if err := client.request(ctx, http.MethodGet, "/zones?"+query.Encode(), nil, &response); err != nil {
		return zone{}, fmt.Errorf("zone lookup: %w", err)
	}
	if err := requireSinglePage(response.ResultInfo, len(response.Result)); err != nil {
		return zone{}, fmt.Errorf("zone lookup: %w", err)
	}
	if len(response.Result) != 1 {
		return zone{}, fmt.Errorf("zone lookup did not return exactly one active zone")
	}
	got := response.Result[0]
	if got.ID == "" || got.Name != zoneName || got.Status != "active" || got.Account.ID != client.accountID {
		return zone{}, fmt.Errorf("zone lookup did not bind the configured account and zone")
	}
	return got, nil
}

func (client apiClient) listRecords(ctx context.Context, zoneID string) ([]dnsRecord, error) {
	query := url.Values{}
	query.Set("name", appRecordName)
	query.Set("page", "1")
	query.Set("per_page", "100")
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records?" + query.Encode()
	var response envelope[[]dnsRecord]
	if err := client.request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, fmt.Errorf("record lookup: %w", err)
	}
	if err := requireSinglePage(response.ResultInfo, len(response.Result)); err != nil {
		return nil, fmt.Errorf("record lookup: %w", err)
	}
	return response.Result, nil
}

func (client apiClient) createRecord(ctx context.Context, zoneID string) (dnsRecord, error) {
	payload := struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		TTL     int    `json:"ttl"`
		Proxied bool   `json:"proxied"`
	}{
		Type:    "A",
		Name:    appRecordName,
		Content: appRecordContent,
		TTL:     1,
		Proxied: false,
	}
	var response envelope[dnsRecord]
	path := "/zones/" + url.PathEscape(zoneID) + "/dns_records"
	if err := client.request(ctx, http.MethodPost, path, payload, &response); err != nil {
		return dnsRecord{}, err
	}
	if !expectedAppRecord(response.Result) {
		return dnsRecord{}, fmt.Errorf("created record did not match the exact requested state")
	}
	return response.Result, nil
}

func (client apiClient) request(ctx context.Context, method, path string, payload any, output any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.baseURL, "/")+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return err
	}
	if len(content) > maximumResponseBytes {
		return fmt.Errorf("Cloudflare response exceeded %d bytes", maximumResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Cloudflare returned HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(content, output); err != nil {
		return fmt.Errorf("decode Cloudflare response: %w", err)
	}
	var status struct {
		Success bool       `json:"success"`
		Errors  []apiError `json:"errors"`
	}
	if err := json.Unmarshal(content, &status); err != nil {
		return fmt.Errorf("decode Cloudflare status: %w", err)
	}
	if !status.Success || len(status.Errors) != 0 {
		return fmt.Errorf("Cloudflare rejected the request")
	}
	return nil
}

func requireSinglePage(info *resultInfo, results int) error {
	if info == nil {
		return fmt.Errorf("pagination metadata is missing")
	}
	if info.Page != 1 || info.TotalPages != 1 || info.Count != results || info.TotalCount != results || info.PerPage < results {
		return fmt.Errorf("pagination metadata is incomplete or inconsistent")
	}
	return nil
}

func expectedAppRecord(record dnsRecord) bool {
	return record.ID != "" && record.Type == "A" && record.Name == appRecordName && record.Content == appRecordContent && record.Proxied != nil && !*record.Proxied && record.TTL == 1
}

func exactRecordSet(records []dnsRecord) bool {
	return len(records) == 1 && expectedAppRecord(records[0])
}

func publicRecord(record dnsRecord) recordOutput {
	proxied := false
	if record.Proxied != nil {
		proxied = *record.Proxied
	}
	return recordOutput{
		ID:      record.ID,
		Type:    record.Type,
		Name:    record.Name,
		Content: record.Content,
		Proxied: proxied,
		TTL:     record.TTL,
	}
}
