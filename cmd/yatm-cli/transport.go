package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const maxHTTPErrorSize = 64 << 10

type basicAuthTransport struct {
	base     http.RoundTripper
	user     string
	password string
}

type statusOutput struct {
	HTTP    bool `json:"http"`
	GRPCWeb bool `json:"grpc_web"`
}

type jobLogOutput struct {
	Logs   string `json:"logs"`
	Offset int64  `json:"offset,string"`
}

type fileOutput struct {
	Output string `json:"output"`
	Bytes  int64  `json:"bytes,string"`
}

func (t *basicAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Clone the request so authentication remains local to this transport adapter.
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.SetBasicAuth(t.user, t.password)
	return t.base.RoundTrip(cloned)
}

func (r *runtime) prepare() error {
	// Reuse the one transport prepared for this command invocation.
	if r.prepared {
		return nil
	}

	// Validate connection settings before reading credentials or creating a client.
	if r.options.Timeout < 0 {
		return usageError(fmt.Errorf("timeout must not be negative, timeout=%s", r.options.Timeout))
	}
	parsed, err := url.Parse(strings.TrimSpace(r.options.Server))
	if err != nil {
		return usageError(fmt.Errorf("parse server URL failed, %w", err))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return usageError(fmt.Errorf("server URL scheme must be http or https, scheme=%q", parsed.Scheme))
	}
	if parsed.Host == "" {
		return usageError(fmt.Errorf("server URL host is empty"))
	}
	if parsed.User != nil {
		return usageError(fmt.Errorf("server URL must not contain credentials"))
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return usageError(fmt.Errorf("server URL must not contain a query or fragment"))
	}
	r.baseURL = strings.TrimRight(parsed.String(), "/")

	// Resolve the optional Basic Auth pair exactly once for this command invocation.
	transport := http.RoundTripper(http.DefaultTransport)
	user := strings.TrimSpace(r.options.BasicUser)
	passwordFile := strings.TrimSpace(r.options.BasicPasswordFile)
	if (user == "") != (passwordFile == "") {
		return usageError(fmt.Errorf("basic-user and basic-password-file must be specified together"))
	}
	if user != "" {
		password, err := os.ReadFile(passwordFile)
		if err != nil {
			return runtimeError("io", fmt.Errorf("read Basic Auth password failed, path=%q, %w", passwordFile, err))
		}
		transport = &basicAuthTransport{
			base: transport, user: user, password: strings.TrimRight(string(password), "\r\n"),
		}
	}
	r.httpClient = &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	r.prepared = true
	return nil
}

func (r *runtime) context() (context.Context, context.CancelFunc) {
	if r.options.Timeout == 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), r.options.Timeout)
}

func (r *runtime) rpcURL(method string) string {
	return r.baseURL + "/services" + method
}

func (r *runtime) httpURL(path string) string {
	return r.baseURL + "/files" + path
}

func callRPC[Request, Reply any](
	ctx context.Context,
	commandRuntime *runtime,
	method string,
	request *Request,
) (*Reply, error) {
	// Use Connect's concrete gRPC-Web adapter for one existing unary procedure.
	client := connect.NewClient[Request, Reply](
		commandRuntime.httpClient,
		commandRuntime.rpcURL(method),
		connect.WithGRPCWeb(),
	)
	response, err := client.CallUnary(ctx, connect.NewRequest(request))
	if err != nil {
		return nil, runtimeError(
			connect.CodeOf(err).String(),
			fmt.Errorf("call RPC failed, method=%q, %w", method, err),
		)
	}
	return response.Msg, nil
}

func (r *runtime) request(
	ctx context.Context,
	method, path string,
	body io.Reader,
) (*http.Response, error) {
	// Build one authenticated HTTP transfer request against the configured server.
	request, err := http.NewRequestWithContext(ctx, method, r.httpURL(path), body)
	if err != nil {
		return nil, runtimeError("http", fmt.Errorf("create HTTP request failed, %w", err))
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/x-ndjson")
	}
	response, err := r.httpClient.Do(request)
	if err != nil {
		code := "http"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "deadline_exceeded"
		} else if errors.Is(err, context.Canceled) {
			code = "canceled"
		}
		return nil, runtimeError(code, fmt.Errorf("perform HTTP request failed, %w", err))
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}

	// Bound error decoding while retaining a useful reverse-proxy or YATM reason.
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxHTTPErrorSize))
	if readErr != nil {
		return nil, runtimeError(
			fmt.Sprintf("http_%d", response.StatusCode),
			fmt.Errorf("HTTP request returned %s and its error body could not be read, %w", response.Status, readErr),
		)
	}
	message := strings.TrimSpace(string(data))
	var reason struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(data, &reason) == nil && reason.Reason != "" {
		message = reason.Reason
	}
	if message == "" {
		message = response.Status
	}
	return nil, runtimeError(
		fmt.Sprintf("http_%d", response.StatusCode),
		fmt.Errorf("HTTP request returned %s, reason=%q", response.Status, message),
	)
}

func (r *runtime) checkStatus(ctx context.Context) error {
	// Verify the public HTTP path and validate its health payload.
	response, err := r.request(ctx, http.MethodGet, "/ping", nil)
	if err != nil {
		return err
	}
	var ping struct {
		Result string `json:"result"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&ping)
	closeErr := response.Body.Close()
	if decodeErr != nil || closeErr != nil {
		return runtimeError("http", fmt.Errorf("read HTTP health response failed, %w", errors.Join(decodeErr, closeErr)))
	}
	if ping.Result != "pong" {
		return runtimeError("http", fmt.Errorf("unexpected HTTP health result, result=%q", ping.Result))
	}

	// Verify the gRPC-Web path through a bounded, read-only catalog request.
	limit := int64(1)
	_, err = callRPC[entity.ListJobsRequest, entity.ListJobsReply](
		ctx,
		r,
		entity.JobService_List_FullMethodName,
		&entity.ListJobsRequest{Filter: &entity.JobFilter{Limit: &limit}},
	)
	return err
}

func writeProto(output io.Writer, message proto.Message) error {
	// Emit one compact ProtoJSON document with stable protobuf field names.
	data, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
	if err != nil {
		return runtimeError("internal", fmt.Errorf("encode protobuf output failed, %w", err))
	}
	data = append(data, '\n')
	if _, err := output.Write(data); err != nil {
		return runtimeError("io", fmt.Errorf("write command output failed, %w", err))
	}
	return nil
}

func writeJSON(output io.Writer, value any) error {
	if err := json.NewEncoder(output).Encode(value); err != nil {
		return runtimeError("io", fmt.Errorf("write command output failed, %w", err))
	}
	return nil
}

func (r *runtime) saveLibraryExport(ctx context.Context, output string) error {
	// Reserve the local target semantics before starting a potentially large transfer.
	if output == "-" {
		return usageError(fmt.Errorf("export output must be a file path"))
	}
	if _, err := os.Lstat(output); err == nil {
		return safetyError(fmt.Errorf("output already exists, path=%q", output))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return runtimeError("io", fmt.Errorf("inspect output path failed, path=%q, %w", output, err))
	}

	// Open the remote response only after the local destination is available.
	response, err := r.request(ctx, http.MethodGet, "/library/_export", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Stream into the destination directory and publish only the complete response.
	directory := filepath.Dir(output)
	file, err := os.CreateTemp(directory, ".yatm-export-*")
	if err != nil {
		return runtimeError("io", fmt.Errorf("create temporary output failed, path=%q, %w", output, err))
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	written, copyErr := io.Copy(file, response.Body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return runtimeError("io", fmt.Errorf("write Library export failed, path=%q, %w", output, err))
	}
	if err := os.Link(temporary, output); err != nil {
		return runtimeError("io", fmt.Errorf("publish Library export failed, path=%q, %w", output, err))
	}
	return writeJSON(r.stdout, fileOutput{Output: output, Bytes: written})
}
