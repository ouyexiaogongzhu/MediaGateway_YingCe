package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// MediaGateway（本地生成引擎网关）的 REST 客户端：提交 job + 轮询到终态。
// 不做重试、不做连接池定制——网关在 127.0.0.1:8600，失败直接上抛。

const (
	defaultMediaGatewayBaseURL       = "http://127.0.0.1:8600"
	defaultMediaGatewayJobTimeout    = 30 * time.Minute
	mediaGatewayBaseURLEnv           = "CANVAS_MEDIA_GATEWAY_URL"
	mediaGatewayJobTimeoutSecondsEnv = "CANVAS_MEDIA_GATEWAY_JOB_TIMEOUT_SECONDS"
)

type gatewayClient struct {
	baseURL      string
	httpClient   *http.Client
	pollInterval time.Duration
	jobTimeout   time.Duration
}

func newGatewayClientFromEnv() *gatewayClient {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(mediaGatewayBaseURLEnv)), "/")
	if baseURL == "" {
		baseURL = defaultMediaGatewayBaseURL
	}
	timeout := defaultMediaGatewayJobTimeout
	if raw := strings.TrimSpace(os.Getenv(mediaGatewayJobTimeoutSecondsEnv)); raw != "" {
		if seconds, parseErr := strconv.Atoi(raw); parseErr == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	return &gatewayClient{baseURL: baseURL, httpClient: &http.Client{Timeout: 30 * time.Second}, pollInterval: 2 * time.Second, jobTimeout: timeout}
}

// gatewayJob 是 GET /v1/jobs/{id} 返回行的精简视图；视频/成片路径在顶层
// output_path，镜头尾帧等附加输出在 meta（meta.last_frame_path）。
type gatewayJob struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	Error      string         `json:"error"`
	Progress   float64        `json:"progress"`
	Phase      string         `json:"phase"`
	OutputPath string         `json:"output_path"`
	Meta       map[string]any `json:"meta"`
}

func (c *gatewayClient) metaString(job *gatewayJob, key string) string {
	if job == nil || job.Meta == nil {
		return ""
	}
	text, _ := job.Meta[key].(string)
	return strings.TrimSpace(text)
}

func (c *gatewayClient) createJob(ctx context.Context, jobType string, params map[string]any) (*gatewayJob, error) {
	body, err := json.Marshal(map[string]any{"type": jobType, "params": params})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/jobs", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	var job gatewayJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return nil, err
	}
	if strings.TrimSpace(job.ID) == "" {
		return nil, fmt.Errorf("gateway 未返回 job id")
	}
	return &job, nil
}

func (c *gatewayClient) getJob(ctx context.Context, jobID string) (*gatewayJob, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/jobs/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	var job gatewayJob
	if err := json.Unmarshal(payload, &job); err != nil {
		return nil, fmt.Errorf("解析 gateway 响应失败：%w：%s", err, strings.TrimSpace(string(payload)))
	}
	return &job, nil
}

// wait 按 pollInterval 轮询直到终态或超时；failed/cancelled 返回携带 job 的错误。
func (c *gatewayClient) wait(ctx context.Context, jobID string) (*gatewayJob, error) {
	deadline := time.Now().Add(c.jobTimeout)
	for {
		job, err := c.getJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		switch job.Status {
		case "completed":
			return job, nil
		case "failed", "cancelled":
			return job, fmt.Errorf("gateway job %s %s: %s", jobID, job.Status, firstNonEmpty(strings.TrimSpace(job.Error), "无错误信息"))
		}
		if time.Now().After(deadline) {
			return job, fmt.Errorf("gateway job %s 等待超时（>%s）", jobID, c.jobTimeout)
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(c.pollInterval):
		}
	}
}

func (s *Service) mediaGateway() *gatewayClient {
	if s.mediaGatewayClient != nil {
		return s.mediaGatewayClient
	}
	return newGatewayClientFromEnv()
}
