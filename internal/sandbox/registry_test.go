package sandbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type registryTestProvider struct {
	name string
}

func (p *registryTestProvider) Name() string { return p.name }

func (*registryTestProvider) Create(
	context.Context,
	string,
	Spec,
) (Ref, Sandbox, error) {
	panic("not used")
}

func (*registryTestProvider) Attach(
	context.Context,
	string,
	Ref,
	Spec,
) (Sandbox, error) {
	panic("not used")
}

func registryFactory(name string) ProviderFactory {
	return func() (Provider, error) {
		return &registryTestProvider{name: name}, nil
	}
}

func TestProviderRegistry_OpensSelectedFactoryLazily(t *testing.T) {
	var localCalls, dockerCalls int
	registry, err := NewProviderRegistry(
		ProviderRegistration{
			Name: LocalProviderName,
			Factory: func() (Provider, error) {
				localCalls++
				return &registryTestProvider{name: LocalProviderName}, nil
			},
		},
		ProviderRegistration{
			Name: DockerProviderName,
			Factory: func() (Provider, error) {
				dockerCalls++
				return &registryTestProvider{name: DockerProviderName}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if localCalls != 0 || dockerCalls != 0 {
		t.Fatal("registry construction invoked an optional provider factory")
	}

	provider, err := registry.Open(LocalProviderName)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != LocalProviderName {
		t.Fatalf("provider name = %q, want %q", provider.Name(), LocalProviderName)
	}
	if localCalls != 1 || dockerCalls != 0 {
		t.Fatalf("factory calls local=%d docker=%d, want 1/0", localCalls, dockerCalls)
	}
	if got := registry.Names(); !reflect.DeepEqual(
		got,
		[]string{DockerProviderName, LocalProviderName},
	) {
		t.Fatalf("Names() = %v", got)
	}
}

func TestProviderRegistry_RejectsInvalidRegistrations(t *testing.T) {
	cases := []struct {
		name          string
		registrations []ProviderRegistration
		want          string
	}{
		{name: "empty", want: "at least one"},
		{
			name: "missing name",
			registrations: []ProviderRegistration{{
				Factory: registryFactory("local"),
			}},
			want: "name is required",
		},
		{
			name: "non-canonical name",
			registrations: []ProviderRegistration{{
				Name: "Open_Sandbox", Factory: registryFactory("Open_Sandbox"),
			}},
			want: "invalid provider name",
		},
		{
			name: "missing factory",
			registrations: []ProviderRegistration{{
				Name: "local",
			}},
			want: "has no factory",
		},
		{
			name: "duplicate",
			registrations: []ProviderRegistration{
				{Name: "local", Factory: registryFactory("local")},
				{Name: "local", Factory: registryFactory("local")},
			},
			want: "more than once",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewProviderRegistry(tc.registrations...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewProviderRegistry() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestProviderRegistry_RejectsUnknownAndBrokenFactories(t *testing.T) {
	sentinel := errors.New("bad credentials")
	registry, err := NewProviderRegistry(
		ProviderRegistration{Name: "local", Factory: registryFactory("local")},
		ProviderRegistration{Name: "broken", Factory: func() (Provider, error) {
			return nil, sentinel
		}},
		ProviderRegistration{Name: "nil", Factory: func() (Provider, error) {
			return nil, nil
		}},
		ProviderRegistration{Name: "alias", Factory: registryFactory("different")},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := registry.Open("missing"); err == nil ||
		!strings.Contains(err.Error(), "available: alias, broken, local, nil") {
		t.Fatalf("unknown provider error = %v", err)
	}
	if _, err := registry.Open("broken"); !errors.Is(err, sentinel) {
		t.Fatalf("factory error = %v, want wrapped sentinel", err)
	}
	if _, err := registry.Open("nil"); err == nil ||
		!strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("nil factory error = %v", err)
	}
	if _, err := registry.Open("alias"); err == nil ||
		!strings.Contains(err.Error(), `registered as "alias" reports name "different"`) {
		t.Fatalf("name mismatch error = %v", err)
	}
}
