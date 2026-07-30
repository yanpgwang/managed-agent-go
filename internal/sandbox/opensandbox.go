package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

const (
	OpenSandboxProviderName = "opensandbox"
	defaultOpenSandboxImage = "python:3.12-slim"
)

type OpenSandboxConfig struct {
	BaseURL    string
	APIKey     string
	Image      string
	UseProxy   bool
	HTTPClient *http.Client
}

type openSandboxResource struct {
	id       string
	metadata map[string]string
}

type openSandboxRemote interface {
	ID() string
	Exec(context.Context, string, string, time.Duration) (string, string, int, error)
	ReadFile(context.Context, string) ([]byte, error)
	WriteFile(context.Context, string, []byte) error
	Destroy(context.Context) error
}

type openSandboxService interface {
	List(context.Context, map[string]string) ([]openSandboxResource, error)
	Get(context.Context, string) (openSandboxResource, error)
	Create(context.Context, string, Spec) (openSandboxRemote, error)
	Connect(context.Context, string) (openSandboxRemote, error)
}

type openSandboxProvider struct {
	service openSandboxService
	root    string
}

func NewOpenSandboxProvider(cfg OpenSandboxConfig) (Provider, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		return nil, errors.New(
			"sandbox: OPEN_SANDBOX_DOMAIN is required for the opensandbox provider",
		)
	}
	image := strings.TrimSpace(cfg.Image)
	if image == "" {
		image = defaultOpenSandboxImage
	}
	retry := opensandbox.DefaultRetryConfig()
	connection := opensandbox.ConnectionConfig{
		Domain:         baseURL,
		APIKey:         strings.TrimSpace(cfg.APIKey),
		UseServerProxy: cfg.UseProxy,
		RequestTimeout: remoteDefaultPeriod,
		HTTPClient:     cfg.HTTPClient,
		Retry:          &retry,
		DisableMetrics: true,
	}
	return &openSandboxProvider{
		service: &openSandboxSDKService{
			config:  connection,
			manager: opensandbox.NewSandboxManager(connection),
			image:   image,
		},
		root: remoteDefaultRoot,
	}, nil
}

func newOpenSandboxProvider(service openSandboxService, root string) Provider {
	if root == "" {
		root = remoteDefaultRoot
	}
	return &openSandboxProvider{service: service, root: root}
}

func (p *openSandboxProvider) Name() string { return OpenSandboxProviderName }

func (p *openSandboxProvider) Create(
	ctx context.Context,
	sessionKey string,
	spec Spec,
) (Ref, Sandbox, error) {
	if sessionKey == "" {
		return Ref{}, nil, errors.New("sandbox: session key is required")
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, nil, err
	}
	existing, err := p.service.List(ctx, remoteMetadata(sessionKey))
	if err != nil {
		return Ref{}, nil, fmt.Errorf("sandbox: opensandbox list: %w", err)
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].id < existing[j].id })
	if len(existing) > 0 {
		return p.attachResource(ctx, sessionKey, existing[0], spec)
	}
	remote, err := p.service.Create(ctx, sessionKey, spec)
	if err != nil {
		existing, findErr := p.service.List(ctx, remoteMetadata(sessionKey))
		if findErr == nil && len(existing) > 0 {
			sort.Slice(existing, func(i, j int) bool {
				return existing[i].id < existing[j].id
			})
			return p.attachResource(ctx, sessionKey, existing[0], spec)
		}
		return Ref{}, nil, fmt.Errorf("sandbox: opensandbox create: %w", err)
	}
	box := &openSandboxBox{
		remote:  remote,
		root:    p.root,
		timeout: spec.Timeout,
	}
	if err := box.ensureRoot(ctx); err != nil {
		_ = remote.Destroy(context.Background())
		return Ref{}, nil, err
	}
	return Ref{Provider: p.Name(), ID: remote.ID()}, box, nil
}

func (p *openSandboxProvider) Attach(
	ctx context.Context,
	sessionKey string,
	ref Ref,
	spec Spec,
) (Sandbox, error) {
	if err := validateRemoteReference(p.Name(), sessionKey, ref); err != nil {
		return nil, err
	}
	resource, err := p.service.Get(ctx, ref.ID)
	if err != nil {
		if isOpenSandboxNotFound(err) {
			return nil, fmt.Errorf("%w: opensandbox sandbox %q", ErrNotFound, ref.ID)
		}
		return nil, fmt.Errorf("sandbox: opensandbox get: %w", err)
	}
	if err := validateRemoteOwnership(
		p.Name(),
		resource.id,
		sessionKey,
		resource.metadata,
	); err != nil {
		return nil, err
	}
	remote, err := p.service.Connect(ctx, ref.ID)
	if err != nil {
		if isOpenSandboxNotFound(err) {
			return nil, fmt.Errorf("%w: opensandbox sandbox %q", ErrNotFound, ref.ID)
		}
		return nil, fmt.Errorf("sandbox: opensandbox connect: %w", err)
	}
	box := &openSandboxBox{
		remote:  remote,
		root:    p.root,
		timeout: spec.Timeout,
	}
	if err := box.ensureRoot(ctx); err != nil {
		return nil, err
	}
	return box, nil
}

func (p *openSandboxProvider) attachResource(
	ctx context.Context,
	sessionKey string,
	resource openSandboxResource,
	spec Spec,
) (Ref, Sandbox, error) {
	ref := Ref{Provider: p.Name(), ID: resource.id}
	box, err := p.Attach(ctx, sessionKey, ref, spec)
	return ref, box, err
}

type openSandboxBox struct {
	remote  openSandboxRemote
	root    string
	timeout time.Duration
}

func (s *openSandboxBox) Root() string { return s.root }

func (s *openSandboxBox) ensureRoot(ctx context.Context) error {
	_, stderr, code, err := s.remote.Exec(
		ctx,
		"mkdir -p "+shellQuote(s.root),
		"/",
		s.timeout,
	)
	if err != nil {
		return fmt.Errorf("sandbox: opensandbox create workspace: %w", err)
	}
	if code != 0 {
		return fmt.Errorf(
			"sandbox: opensandbox create workspace failed (exit %d): %s",
			code,
			strings.TrimSpace(stderr),
		)
	}
	return nil
}

func (s *openSandboxBox) Exec(
	ctx context.Context,
	cmd Command,
) (*Result, error) {
	if cmd.Path == "" {
		return nil, errors.New("sandbox: command path is required")
	}
	runCtx, cancel := commandTimeout(ctx, s.timeout)
	defer cancel()
	stdout, stderr, code, err := s.remote.Exec(
		runCtx,
		remoteCommandLine(cmd),
		s.root,
		s.timeout,
	)
	if err != nil {
		if runCtx.Err() != nil {
			return remoteResult(runCtx, s.timeout, stdout, stderr, code)
		}
		return nil, fmt.Errorf("sandbox: opensandbox exec: %w", err)
	}
	return remoteResult(runCtx, s.timeout, stdout, stderr, code)
}

func (s *openSandboxBox) ReadFile(
	ctx context.Context,
	value string,
) ([]byte, error) {
	full, err := containedRemotePath(s.root, value)
	if err != nil {
		return nil, err
	}
	return s.remote.ReadFile(ctx, full)
}

func (s *openSandboxBox) WriteFile(
	ctx context.Context,
	value string,
	data []byte,
) error {
	full, err := containedRemotePath(s.root, value)
	if err != nil {
		return err
	}
	_, stderr, code, err := s.remote.Exec(
		ctx,
		"mkdir -p "+shellQuote(path.Dir(full)),
		s.root,
		s.timeout,
	)
	if err != nil {
		return fmt.Errorf("sandbox: opensandbox create parent: %w", err)
	}
	if code != 0 {
		return fmt.Errorf(
			"sandbox: opensandbox create parent failed (exit %d): %s",
			code,
			strings.TrimSpace(stderr),
		)
	}
	return s.remote.WriteFile(ctx, full, data)
}

func (s *openSandboxBox) Destroy(ctx context.Context) error {
	err := s.remote.Destroy(ctx)
	if isOpenSandboxNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sandbox: opensandbox destroy: %w", err)
	}
	return nil
}

type openSandboxSDKService struct {
	config  opensandbox.ConnectionConfig
	manager *opensandbox.SandboxManager
	image   string
}

func (s *openSandboxSDKService) List(
	ctx context.Context,
	metadata map[string]string,
) ([]openSandboxResource, error) {
	const pageSize = 100
	var resources []openSandboxResource
	for page := 1; ; page++ {
		response, err := s.manager.ListSandboxInfos(ctx, opensandbox.ListOptions{
			Metadata: metadata,
			Page:     page,
			PageSize: pageSize,
		})
		if err != nil {
			return nil, err
		}
		for _, item := range response.Items {
			resources = append(resources, openSandboxResource{
				id:       item.ID,
				metadata: item.Metadata,
			})
		}
		if !response.Pagination.HasNextPage {
			return resources, nil
		}
		if len(response.Items) == 0 ||
			(response.Pagination.Page > 0 &&
				response.Pagination.Page != page) ||
			(response.Pagination.TotalPages > 0 &&
				page >= response.Pagination.TotalPages) {
			return nil, errors.New(
				"sandbox: opensandbox returned non-advancing pagination",
			)
		}
	}
}

func (s *openSandboxSDKService) Get(
	ctx context.Context,
	id string,
) (openSandboxResource, error) {
	info, err := s.manager.GetSandboxInfo(ctx, id)
	if err != nil {
		return openSandboxResource{}, err
	}
	return openSandboxResource{id: info.ID, metadata: info.Metadata}, nil
}

func (s *openSandboxSDKService) Create(
	ctx context.Context,
	sessionKey string,
	spec Spec,
) (openSandboxRemote, error) {
	image := s.image
	if spec.Image != "" {
		image = spec.Image
	}
	var limits opensandbox.ResourceLimits
	if spec.CPUs != "" {
		limits = opensandbox.ResourceLimits{}
		limits["cpu"] = spec.CPUs
	}
	if spec.Memory != "" {
		if limits == nil {
			limits = opensandbox.ResourceLimits{}
		}
		limits["memory"] = normalizeKubernetesMemory(spec.Memory)
	}
	var policy *opensandbox.NetworkPolicy
	if spec.Network == "" || spec.Network == "none" {
		policy = &opensandbox.NetworkPolicy{DefaultAction: "deny"}
	}
	created, err := opensandbox.CreateSandbox(
		ctx,
		s.config,
		opensandbox.SandboxCreateOptions{
			Image:          image,
			ResourceLimits: limits,
			Metadata:       remoteMetadata(sessionKey),
			ManualCleanup:  true,
			NetworkPolicy:  policy,
		},
	)
	if err != nil {
		return nil, err
	}
	return &openSandboxSDKRemote{sandbox: created}, nil
}

func (s *openSandboxSDKService) Connect(
	ctx context.Context,
	id string,
) (openSandboxRemote, error) {
	connected, err := opensandbox.ConnectSandbox(ctx, s.config, id)
	if err != nil {
		return nil, err
	}
	return &openSandboxSDKRemote{sandbox: connected}, nil
}

type openSandboxSDKRemote struct {
	sandbox *opensandbox.Sandbox
}

func (s *openSandboxSDKRemote) ID() string { return s.sandbox.ID() }

func (s *openSandboxSDKRemote) Exec(
	ctx context.Context,
	command string,
	cwd string,
	timeout time.Duration,
) (string, string, int, error) {
	request := opensandbox.RunCommandRequest{
		Command: command,
		Cwd:     cwd,
	}
	if timeout > 0 {
		request.Timeout = timeout.Milliseconds()
	}
	execution, err := s.sandbox.RunCommandWithOpts(
		ctx,
		request,
		nil,
	)
	if err != nil {
		return "", "", -1, err
	}
	exitCode := 0
	if execution.ExitCode != nil {
		exitCode = *execution.ExitCode
	}
	stdout := joinOpenSandboxOutput(execution.Stdout)
	stderr := joinOpenSandboxOutput(execution.Stderr)
	return stdout, stderr, exitCode, nil
}

func (s *openSandboxSDKRemote) ReadFile(
	ctx context.Context,
	value string,
) (data []byte, err error) {
	reader, err := s.sandbox.DownloadFile(ctx, value, "")
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil {
			err = closeErr
		}
	}()
	return io.ReadAll(reader)
}

func (s *openSandboxSDKRemote) WriteFile(
	ctx context.Context,
	value string,
	data []byte,
) error {
	return s.sandbox.UploadFile(
		ctx,
		bytes.NewReader(data),
		opensandbox.UploadFileOptions{
			FileName: path.Base(value),
			Metadata: opensandbox.FileMetadata{
				Path: value,
				// OpenSandbox's wire API uses chmod-style digits (600), not
				// Go's os.FileMode integer representation (0o600 == 384).
				Mode: 600,
			},
		},
	)
}

func (s *openSandboxSDKRemote) Destroy(ctx context.Context) error {
	return s.sandbox.Kill(ctx)
}

func joinOpenSandboxOutput(messages []opensandbox.OutputMessage) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(message.Text)
	}
	return builder.String()
}

func normalizeKubernetesMemory(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	if strings.HasSuffix(lower, "m") &&
		!strings.HasSuffix(lower, "mi") {
		return strings.TrimSuffix(lower, "m") + "Mi"
	}
	return value
}

func isOpenSandboxNotFound(err error) bool {
	var apiErr *opensandbox.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}
