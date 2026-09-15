//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var binaries struct {
	sync.Once
	directory string
	err       error
}

func TestMain(m *testing.M) {
	// Remove only the build directory allocated by this test process.
	code := m.Run()
	if binaries.directory != "" {
		if err := os.RemoveAll(binaries.directory); err != nil {
			fmt.Fprintln(os.Stderr, err)
			code = 1
		}
	}
	os.Exit(code)
}

func testBinary(t *testing.T, name string) string {
	t.Helper()
	// Acceptance can exercise extracted release programs without rebuilding or owning their directory.
	if directory := os.Getenv("YATM_E2E_BIN_DIR"); directory != "" {
		path, err := candidateBinary(directory, name)
		require.NoError(t, err)
		return path
	}

	// Build the actual shipped entry points once for this acceptance process.
	binaries.Do(func() {
		binaries.directory, binaries.err = os.MkdirTemp("", "yatm-e2e-binaries-")
		if binaries.err != nil {
			return
		}
		for _, item := range []struct{ name, target string }{{"yatm-cli", "./cmd/yatm-cli"}, {"yatm-httpd", "./cmd/httpd"}} {
			command := exec.Command("go", "build", "-o", filepath.Join(binaries.directory, item.name), item.target)
			command.Dir = ".."
			output, err := command.CombinedOutput()
			if err != nil {
				binaries.err = fmt.Errorf("build %s: %w\n%s", item.name, err, output)
				return
			}
		}
	})
	require.NoError(t, binaries.err)
	return filepath.Join(binaries.directory, name)
}

func candidateBinary(directory, name string) (string, error) {
	// Resolve before child processes change their working directory to an isolated installation.
	path, err := filepath.Abs(filepath.Join(directory, name))
	if err != nil {
		return "", fmt.Errorf("resolve candidate %s: %w", name, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect candidate %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("candidate %s must be a regular executable: %s", name, path)
	}
	return path, nil
}

func TestCandidateBinarySelection(t *testing.T) {
	// A supplied candidate is used in place and never registered for TestMain cleanup.
	directory := t.TempDir()
	path := filepath.Join(directory, "yatm-cli")
	require.NoError(t, os.WriteFile(path, []byte("candidate fixture"), 0o755))
	t.Setenv("YATM_E2E_BIN_DIR", directory)
	owned := binaries.directory
	require.Equal(t, path, testBinary(t, "yatm-cli"))
	require.Equal(t, owned, binaries.directory)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "candidate fixture", string(data))

	// Broken candidates fail explicitly instead of silently testing a checkout build.
	_, err = candidateBinary(directory, "yatm-httpd")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = candidateBinary(directory, "yatm-cli")
	require.ErrorContains(t, err, "regular executable")
	require.NoError(t, os.Mkdir(filepath.Join(directory, "yatm-httpd"), 0o755))
	_, err = candidateBinary(directory, "yatm-httpd")
	require.ErrorContains(t, err, "regular executable")
}

// cliConnection preserves typed test assertions while every business request runs a real CLI process.
// Unsupported requests fail; this adapter never falls back to direct RPC or private service calls.
type cliConnection struct {
	binary    string
	url       string
	directory string
}

func serveCLI(t *testing.T, api *apis.API, exe *executor.Executor) *cliConnection {
	t.Helper()
	// Fault-injection fixtures retain server ownership, but use the production HTTP transport.
	server := grpc.NewServer()
	entity.RegisterServiceServer(server, api)
	entity.RegisterJobServiceServer(server, api)
	api.RegisterLocations(server)
	exe.RegisterJobServices(server)
	mux := http.NewServeMux()
	mux.Handle("/services/", http.StripPrefix("/services", grpcweb.WrapServer(server)))
	mux.Handle("/files/", http.StripPrefix("/files", api.Uploader()))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(func() { httpServer.Close(); server.Stop() })
	connection := &cliConnection{binary: testBinary(t, "yatm-cli"), url: httpServer.URL, directory: t.TempDir()}
	setFixtureAutoCollect(t, context.Background(), connection, false)
	return connection
}

func (c *cliConnection) run(ctx context.Context, args ...string) ([]byte, error) {
	// Pass a deadline to the process and the network client; capture stderr independently of JSON.
	command := exec.CommandContext(ctx, c.binary, append([]string{"--server", c.url, "--timeout", "2m"}, args...)...)
	command.Dir = c.directory
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return output, fmt.Errorf("yatm-cli %q: %w\n%s", args, err, stderr.String())
	}
	return output, nil
}

func (c *cliConnection) Invoke(ctx context.Context, method string, request, reply any, _ ...grpc.CallOption) error {
	// Translate the typed fixture request to a documented CLI command.
	args, err := c.arguments(method, request)
	if err != nil {
		return err
	}
	output, err := c.run(ctx, args...)
	if err != nil {
		return err
	}
	message, ok := reply.(proto.Message)
	if !ok {
		return fmt.Errorf("CLI reply is not protobuf: %T", reply)
	}
	if err := decodeCLIOutput(output, message); err != nil {
		return fmt.Errorf("decode CLI result for %s: %w\n%s", method, err, output)
	}
	return nil
}

func decodeCLIOutput(output []byte, message proto.Message) error {
	// Job logs intentionally use readable UTF-8 text instead of protobuf's base64 bytes representation.
	if reply, ok := message.(*entity.GetJobLogReply); ok {
		var value struct {
			Logs   string `json:"logs"`
			Offset int64  `json:"offset,string"`
		}
		if err := json.Unmarshal(output, &value); err != nil {
			return err
		}
		reply.Logs, reply.Offset = []byte(value.Logs), value.Offset
		return nil
	}
	return protojson.Unmarshal(output, message)
}

func (*cliConnection) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, fmt.Errorf("streaming RPC has no CLI acceptance adapter")
}

func decimal(value int64) string { return strconv.FormatInt(value, 10) }

func selectionArguments(selections []*entity.FileSelection, ids []int64) ([]string, error) {
	// CLI selections preserve identity and scope; the server freezes and expands them.
	var result []string
	for _, id := range ids {
		result = append(result, "--file-id", decimal(id))
	}
	for _, selected := range selections {
		switch value := selected.Target.(type) {
		case *entity.FileSelection_Library:
			result = append(result, "--file-id", decimal(value.Library.FileId))
			if selected.Scope == entity.FileScope_FILE_SCOPE_SAVED {
				result = append(result, "--scope", "saved")
			}
		case *entity.FileSelection_Location:
			result = append(result, "--location", decimal(value.Location.LocationId)+":"+value.Location.Path)
		default:
			return nil, fmt.Errorf("unsupported selection %T", selected.Target)
		}
	}
	return result, nil
}

func indexedSelections(t *testing.T, ctx context.Context, connection *cliConnection, root string, paths ...string) []*entity.FileSelection {
	t.Helper()
	selections := liveSelections(t, ctx, connection, root, paths...)
	// Tests for content matching explicitly request analysis; ordinary content workflows do not require it.
	job, err := entity.NewScanJobServiceClient(connection).Create(ctx, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{LocationId: selections[0].GetLocation().LocationId, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS}})
	require.NoError(t, err)
	_, err = connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "2m", "--poll-interval", "100ms")
	require.NoError(t, err)
	return selections
}

func liveSelections(t *testing.T, ctx context.Context, connection *cliConnection, root string, paths ...string) []*entity.FileSelection {
	t.Helper()
	// Find or register one source using only user-visible Location commands.
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	locations := entity.NewLocationServiceClient(connection)
	var source *entity.Location
	for after := int64(0); ; {
		page, err := locations.List(ctx, &entity.ListLocationsRequest{AfterId: after, Limit: 100})
		require.NoError(t, err)
		for _, value := range page.Locations {
			if value.RootPath == root {
				source = value
			}
		}
		if !page.HasMore {
			break
		}
		after = page.Locations[len(page.Locations)-1].Id
	}
	if source == nil {
		registered, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Archive", RootPath: root}})
		require.NoError(t, err)
		source = registered.Location
	}
	if source.Binding == entity.OnlineBinding_UNCONFIRMED {
		confirmed, err := locations.Confirm(ctx, &entity.LocationRef{Id: source.Id, Revision: source.Revision})
		require.NoError(t, err)
		source = confirmed.Location
	}

	// Select real paths without collection or content analysis.
	result := make([]*entity.FileSelection, 0, len(paths))
	for _, path := range paths {
		result = append(result, &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: source.Id, Path: path}}})
	}
	return result
}

func librarySelections(ids ...int64) []*entity.FileSelection {
	result := make([]*entity.FileSelection, 0, len(ids))
	for _, id := range ids {
		result = append(result, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	return result
}

func restoreDestination(t *testing.T, ctx context.Context, connection *cliConnection, root string) *entity.RestoreDestination {
	t.Helper()
	// Restore-only directories do not need a source scan.
	require.NoError(t, os.MkdirAll(root, 0o755))
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	locations := entity.NewLocationServiceClient(connection)
	for after := int64(0); ; {
		page, err := locations.List(ctx, &entity.ListLocationsRequest{AfterId: after, Limit: 100})
		require.NoError(t, err)
		for _, value := range page.Locations {
			if value.RootPath == root && value.RestoreTarget {
				return &entity.RestoreDestination{LocationId: value.Id}
			}
		}
		if !page.HasMore {
			break
		}
		after = page.Locations[len(page.Locations)-1].Id
	}
	created, err := locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Restore", RootPath: root, RestoreTarget: true}})
	require.NoError(t, err)
	return &entity.RestoreDestination{LocationId: created.Location.Id}
}
