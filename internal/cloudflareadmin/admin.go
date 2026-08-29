package cloudflareadmin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const usage = "usage: scripts/with-cloudflare-env.sh dns status\n       scripts/with-cloudflare-env.sh dns publish-app"

type operation int

const (
	operationDNSStatus operation = iota + 1
	operationDNSPublishApp
)

type configuration struct {
	credentialsPath string
	commonDirectory string
	apiBaseURL      string
	httpClient      *http.Client
}

func Run(ctx context.Context, repositoryRoot string, args []string, stdout io.Writer) error {
	op, err := parseOperation(args)
	if err != nil {
		return err
	}
	commonDirectory, err := resolveCommonDirectory(ctx, repositoryRoot)
	if err != nil {
		return err
	}
	config := configuration{
		credentialsPath: filepath.Join(filepath.Dir(commonDirectory), ".env.txt"),
		commonDirectory: commonDirectory,
		apiBaseURL:      cloudflareAPIBase,
		httpClient:      productionHTTPClient(),
	}
	return config.execute(ctx, op, stdout)
}

func productionHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func parseOperation(args []string) (operation, error) {
	if len(args) != 2 {
		return 0, fmt.Errorf("%s", usage)
	}
	switch strings.Join(args, " ") {
	case "dns status":
		return operationDNSStatus, nil
	case "dns publish-app":
		return operationDNSPublishApp, nil
	default:
		return 0, fmt.Errorf("%s", usage)
	}
}

func (config configuration) execute(ctx context.Context, op operation, stdout io.Writer) error {
	secrets, err := readCredentials(config.credentialsPath)
	if err != nil {
		return err
	}
	client := apiClient{
		httpClient: config.httpClient,
		baseURL:    config.apiBaseURL,
		token:      secrets.apiToken,
		accountID:  secrets.accountID,
	}
	if op == operationDNSStatus {
		return runDNSStatus(ctx, client, stdout)
	}
	lock, err := acquirePublishLock(config.commonDirectory)
	if err != nil {
		return fmt.Errorf("acquire app DNS publish lock: %w", err)
	}
	defer lock.Close()
	return runDNSPublish(ctx, client, stdout)
}

func runDNSStatus(ctx context.Context, client apiClient, stdout io.Writer) error {
	zone, err := client.lookupZone(ctx)
	if err != nil {
		return err
	}
	records, err := client.listRecords(ctx, zone.ID)
	if err != nil {
		return err
	}
	output := struct {
		Outcome string         `json:"outcome"`
		Zone    string         `json:"zone"`
		Records []recordOutput `json:"records"`
	}{Outcome: "observed", Zone: zoneName, Records: make([]recordOutput, len(records))}
	for index, record := range records {
		output.Records[index] = publicRecord(record)
	}
	return json.NewEncoder(stdout).Encode(output)
}

func runDNSPublish(ctx context.Context, client apiClient, stdout io.Writer) error {
	zone, err := client.lookupZone(ctx)
	if err != nil {
		return err
	}
	records, err := client.listRecords(ctx, zone.ID)
	if err != nil {
		return err
	}
	if exactRecordSet(records) {
		return writePublishOutcome(stdout, "unchanged", records[0])
	}
	if len(records) != 0 {
		return fmt.Errorf("%s has conflicting records; refusing overwrite", appRecordName)
	}
	created, createErr := client.createRecord(ctx, zone.ID)
	if createErr != nil {
		settled, lookupErr := client.listRecords(ctx, zone.ID)
		if lookupErr == nil && exactRecordSet(settled) {
			return writePublishOutcome(stdout, "settled_after_ambiguous_create", settled[0])
		}
		if lookupErr != nil {
			return fmt.Errorf("record creation outcome is indeterminate (%v); verification failed: %w", createErr, lookupErr)
		}
		return fmt.Errorf("record creation outcome is indeterminate: %w", createErr)
	}
	settled, err := client.listRecords(ctx, zone.ID)
	if err != nil {
		return fmt.Errorf("verify created record: %w", err)
	}
	if !exactRecordSet(settled) || settled[0].ID != created.ID {
		return fmt.Errorf("created record did not settle as one exact record")
	}
	return writePublishOutcome(stdout, "created", settled[0])
}

func writePublishOutcome(stdout io.Writer, outcome string, record dnsRecord) error {
	return json.NewEncoder(stdout).Encode(struct {
		Outcome string       `json:"outcome"`
		Record  recordOutput `json:"record"`
	}{Outcome: outcome, Record: publicRecord(record)})
}

func resolveCommonDirectory(ctx context.Context, repositoryRoot string) (string, error) {
	if !filepath.IsAbs(repositoryRoot) {
		return "", fmt.Errorf("repository root must be absolute")
	}
	command := exec.CommandContext(ctx, "/usr/bin/git", "-C", repositoryRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	command.Env = []string{
		"HOME=/var/empty",
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git common directory: %w", err)
	}
	common := strings.TrimSpace(string(output))
	if !filepath.IsAbs(common) || filepath.Base(common) != ".git" {
		return "", fmt.Errorf("git common directory is not an absolute .git directory")
	}
	info, err := os.Stat(common)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("git common directory is unavailable")
	}
	return filepath.Clean(common), nil
}
