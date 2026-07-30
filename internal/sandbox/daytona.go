package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/daytona/clients/sdk-go/pkg/daytona"
	daytonaerrors "github.com/daytona/clients/sdk-go/pkg/errors"
	daytonaoptions "github.com/daytona/clients/sdk-go/pkg/options"
	daytonatypes "github.com/daytona/clients/sdk-go/pkg/types"
)

const (
	DaytonaProviderName = "daytona"
	defaultDaytonaImage = "python:3.12-slim"
	defaultDaytonaRoot  = "/home/daytona"
)

type DaytonaConfig struct {
	APIURL           string
	APIKey           string
	Target           string
	Snapshot         string
	Image            string
	AutoPauseMinutes int
	HTTPClient       *http.Client
}

type daytonaResource struct {
	id     string
	labels map[string]string
	remote daytonaRemote
}

type daytonaRemote interface {
	ID() string
	Exec(context.Context, string, string, time.Duration) (string, string, int, error)
	ReadFile(context.Context, string) ([]byte, error)
	WriteFile(context.Context, string, []byte) error
	Start(context.Context) error
	Destroy(context.Context) error
}

type daytonaService interface {
	Get(context.Context, string) (daytonaResource, error)
	Create(context.Context, string, string, Spec) (daytonaResource, error)
}

type daytonaProvider struct {
	service daytonaService
	root    string
}

func NewDaytonaProvider(cfg DaytonaConfig) (Provider, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New(
			"sandbox: DAYTONA_API_KEY is required for the daytona provider",
		)
	}
	autoPause := cfg.AutoPauseMinutes
	if autoPause <= 0 {
		autoPause = 15
	}
	image := strings.TrimSpace(cfg.Image)
	if image == "" {
		image = defaultDaytonaImage
	}
	client, err := daytona.NewClientWithConfig(&daytonatypes.DaytonaConfig{
		APIKey:      apiKey,
		APIUrl:      strings.TrimRight(strings.TrimSpace(cfg.APIURL), "/"),
		Target:      strings.TrimSpace(cfg.Target),
		OtelEnabled: false,
		HTTPClient:  cfg.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox: initialize Daytona client: %w", err)
	}
	return &daytonaProvider{
		service: &daytonaSDKService{
			client:           client,
			snapshot:         strings.TrimSpace(cfg.Snapshot),
			image:            image,
			autoPauseMinutes: autoPause,
		},
		root: defaultDaytonaRoot,
	}, nil
}

func newDaytonaProvider(service daytonaService, root string) Provider {
	if root == "" {
		root = defaultDaytonaRoot
	}
	return &daytonaProvider{service: service, root: root}
}

func (p *daytonaProvider) Name() string { return DaytonaProviderName }

func (p *daytonaProvider) Create(
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
	name := deterministicRemoteName(p.Name(), sessionKey)
	resource, err := p.service.Get(ctx, name)
	if err == nil {
		return p.attachResource(ctx, sessionKey, resource, spec)
	}
	if !isDaytonaNotFound(err) {
		return Ref{}, nil, fmt.Errorf("sandbox: daytona get by name: %w", err)
	}
	resource, err = p.service.Create(ctx, name, sessionKey, spec)
	if err != nil {
		// Daytona names are unique. A conflict or lost response means the
		// deterministic name is the recovery key.
		resource, getErr := p.service.Get(ctx, name)
		if getErr == nil {
			return p.attachResource(ctx, sessionKey, resource, spec)
		}
		return Ref{}, nil, fmt.Errorf("sandbox: daytona create: %w", err)
	}
	return p.attachResource(ctx, sessionKey, resource, spec)
}

func (p *daytonaProvider) Attach(
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
		if isDaytonaNotFound(err) {
			return nil, fmt.Errorf("%w: daytona sandbox %q", ErrNotFound, ref.ID)
		}
		return nil, fmt.Errorf("sandbox: daytona get: %w", err)
	}
	if err := validateRemoteOwnership(
		p.Name(),
		resource.id,
		sessionKey,
		resource.labels,
	); err != nil {
		return nil, err
	}
	if err := resource.remote.Start(ctx); err != nil {
		if isDaytonaNotFound(err) {
			return nil, fmt.Errorf("%w: daytona sandbox %q", ErrNotFound, ref.ID)
		}
		return nil, fmt.Errorf("sandbox: daytona start: %w", err)
	}
	box := &daytonaBox{
		remote:  resource.remote,
		root:    p.root,
		timeout: spec.Timeout,
	}
	if err := box.ensureRoot(ctx); err != nil {
		return nil, err
	}
	return box, nil
}

func (p *daytonaProvider) attachResource(
	ctx context.Context,
	sessionKey string,
	resource daytonaResource,
	spec Spec,
) (Ref, Sandbox, error) {
	ref := Ref{Provider: p.Name(), ID: resource.id}
	box, err := p.Attach(ctx, sessionKey, ref, spec)
	return ref, box, err
}

type daytonaBox struct {
	remote  daytonaRemote
	root    string
	timeout time.Duration
}

func (s *daytonaBox) Root() string { return s.root }

func (s *daytonaBox) ensureRoot(ctx context.Context) error {
	_, stderr, code, err := s.remote.Exec(
		ctx,
		"mkdir -p "+shellQuote(s.root),
		"/",
		s.timeout,
	)
	if err != nil {
		return fmt.Errorf("sandbox: daytona create workspace: %w", err)
	}
	if code != 0 {
		return fmt.Errorf(
			"sandbox: daytona create workspace failed (exit %d): %s",
			code,
			strings.TrimSpace(stderr),
		)
	}
	return nil
}

func (s *daytonaBox) Exec(
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
		return nil, fmt.Errorf("sandbox: daytona exec: %w", err)
	}
	return remoteResult(runCtx, s.timeout, stdout, stderr, code)
}

func (s *daytonaBox) ReadFile(
	ctx context.Context,
	value string,
) ([]byte, error) {
	full, err := containedRemotePath(s.root, value)
	if err != nil {
		return nil, err
	}
	return s.remote.ReadFile(ctx, full)
}

func (s *daytonaBox) WriteFile(
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
		return fmt.Errorf("sandbox: daytona create parent: %w", err)
	}
	if code != 0 {
		return fmt.Errorf(
			"sandbox: daytona create parent failed (exit %d): %s",
			code,
			strings.TrimSpace(stderr),
		)
	}
	return s.remote.WriteFile(ctx, full, data)
}

func (s *daytonaBox) Destroy(ctx context.Context) error {
	err := s.remote.Destroy(ctx)
	if isDaytonaNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sandbox: daytona destroy: %w", err)
	}
	return nil
}

type daytonaSDKService struct {
	client           *daytona.Client
	snapshot         string
	image            string
	autoPauseMinutes int
}

func (s *daytonaSDKService) Get(
	ctx context.Context,
	idOrName string,
) (daytonaResource, error) {
	box, err := s.client.Get(ctx, idOrName)
	if err != nil {
		return daytonaResource{}, err
	}
	return daytonaResource{
		id:     box.ID,
		labels: box.Labels,
		remote: &daytonaSDKRemote{sandbox: box},
	}, nil
}

func (s *daytonaSDKService) Create(
	ctx context.Context,
	name string,
	sessionKey string,
	spec Spec,
) (daytonaResource, error) {
	autoPause := s.autoPauseMinutes
	neverDelete := -1
	noTTL := 0
	base := daytonatypes.SandboxBaseParams{
		Name:               name,
		Labels:             remoteMetadata(sessionKey),
		AutoPauseInterval:  &autoPause,
		AutoDeleteInterval: &neverDelete,
		TtlMinutes:         &noTTL,
		NetworkBlockAll:    spec.Network == "" || spec.Network == "none",
	}
	var params any
	if s.snapshot != "" && spec.Image == "" {
		params = daytonatypes.SnapshotParams{
			SandboxBaseParams: base,
			Snapshot:          s.snapshot,
		}
	} else {
		image := s.image
		if spec.Image != "" {
			image = spec.Image
		}
		params = daytonatypes.ImageParams{
			SandboxBaseParams: base,
			Image:             image,
			Resources:         daytonaResources(spec),
		}
	}
	box, err := s.client.Create(ctx, params)
	if err != nil {
		return daytonaResource{}, err
	}
	return daytonaResource{
		id:     box.ID,
		labels: box.Labels,
		remote: &daytonaSDKRemote{sandbox: box},
	}, nil
}

type daytonaSDKRemote struct {
	sandbox *daytona.Sandbox
}

func (s *daytonaSDKRemote) ID() string { return s.sandbox.ID }

func (s *daytonaSDKRemote) Exec(
	ctx context.Context,
	command string,
	cwd string,
	timeout time.Duration,
) (string, string, int, error) {
	options := []func(*daytonaoptions.ExecuteCommand){
		daytonaoptions.WithCwd(cwd),
	}
	if timeout > 0 {
		options = append(options, daytonaoptions.WithExecuteTimeout(timeout))
	}
	result, err := s.sandbox.Process.ExecuteCommand(ctx, command, options...)
	if err != nil {
		return "", "", -1, err
	}
	return result.Result, "", result.ExitCode, nil
}

func (s *daytonaSDKRemote) ReadFile(
	ctx context.Context,
	value string,
) (data []byte, err error) {
	reader, err := s.sandbox.FileSystem.DownloadFileStream(ctx, value)
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

func (s *daytonaSDKRemote) WriteFile(
	ctx context.Context,
	value string,
	data []byte,
) error {
	return s.sandbox.FileSystem.UploadFileStream(
		ctx,
		bytes.NewReader(data),
		value,
	)
}

func (s *daytonaSDKRemote) Start(ctx context.Context) error {
	return s.sandbox.Start(ctx)
}

func (s *daytonaSDKRemote) Destroy(ctx context.Context) error {
	return s.sandbox.DeleteAndWait(ctx, time.Minute)
}

func daytonaResources(spec Spec) *daytonatypes.Resources {
	resources := &daytonatypes.Resources{}
	hasResource := false
	if spec.CPUs != "" {
		if cpu := parseWholeCPU(spec.CPUs); cpu > 0 {
			resources.CPU = cpu
			hasResource = true
		}
	}
	if memory := parseMemoryMB(spec.Memory); memory > 0 {
		resources.Memory = memory
		hasResource = true
	}
	if !hasResource {
		return nil
	}
	return resources
}

func parseWholeCPU(value string) int {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 || parsed != math.Trunc(parsed) {
		return 0
	}
	return int(parsed)
}

func parseMemoryMB(value string) int {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, suffix := range []string{"mib", "mb", "mi", "m"} {
		if strings.HasSuffix(normalized, suffix) {
			normalized = strings.TrimSpace(strings.TrimSuffix(normalized, suffix))
			break
		}
	}
	parsed, err := strconv.Atoi(normalized)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

func isDaytonaNotFound(err error) bool {
	return errors.Is(err, daytonaerrors.ErrNotFound)
}
