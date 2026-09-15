package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	legacy "github.com/samuelncui/yatm/migrate/legacy"
	"gorm.io/gorm"
)

type upgradeStatus struct {
	RunningJobIDs []int64 `json:"running_job_ids"`
}

func preflight(ctx context.Context, db *gorm.DB, listen string, serviceStopped bool) error {
	// Identify the installed schema before selecting its running-state source.
	schema, err := installedSchema(db)
	if err != nil {
		return err
	}
	if serviceStopped || schema == legacy.SchemaEmpty {
		return nil
	}

	// legacy persists PROCESSING while a physical operation is active.
	if schema == legacy.SchemaLegacy {
		ids, err := legacy.RunningJobIDs(ctx, db)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			return fmt.Errorf("running legacy Jobs must finish before upgrade, ids=%v", ids)
		}
		return nil
	}

	// Observation must not close current admission before the operator consents.
	ids, err := requestUpgradeStatus(ctx, listen, false)
	if err != nil {
		return fmt.Errorf("inspect current service failed, %w", err)
	}
	if len(ids) > 0 {
		return fmt.Errorf("running current Jobs must finish before upgrade, ids=%v", ids)
	}
	return nil
}

func quiesceService(ctx context.Context, listen string) ([]int64, error) {
	return requestUpgradeStatus(ctx, listen, true)
}

func requestUpgradeStatus(ctx context.Context, listen string, quiesce bool) ([]int64, error) {
	endpoint, err := upgradeStatusURL(listen)
	if err != nil {
		return nil, err
	}
	method := http.MethodGet
	if quiesce {
		method = http.MethodPost
		endpoint = strings.TrimSuffix(endpoint, "status") + "quiesce"
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create upgrade status request failed, %w", err)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("request upgrade status failed, %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		return nil, fmt.Errorf("upgrade status returned HTTP %d", response.StatusCode)
	}

	status := new(upgradeStatus)
	if err := json.NewDecoder(response.Body).Decode(status); err != nil {
		return nil, fmt.Errorf("decode upgrade status failed, %w", err)
	}
	return status.RunningJobIDs, nil
}

func upgradeStatusURL(listen string) (string, error) {
	serverURL, err := localServerURL(listen)
	if err != nil {
		return "", err
	}
	return serverURL + "/files/_upgrade/status", nil
}

func localServerURL(listen string) (string, error) {
	if strings.Contains(listen, "://") {
		parsed, err := url.Parse(listen)
		if err != nil {
			return "", fmt.Errorf("parse service listen URL failed, %w", err)
		}
		if parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
			return "", fmt.Errorf("upgrade requires a direct local HTTP listen address")
		}
		listen = parsed.Host
	}

	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse service listen address failed, %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("upgrade requires a loopback or wildcard listen address")
		}
	}
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, port),
	}).String(), nil
}
