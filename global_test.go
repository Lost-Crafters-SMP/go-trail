package trail

import (
	"context"
	"sync"
	"testing"
)

// resetDefaultProvider restores the no-op default registration so tests that
// mutate the process-wide default do not affect each other.
func resetDefaultProvider(t *testing.T) {
	t.Helper()
	previous := defaultProvider.Load()
	SetDefaultProvider(nil)
	t.Cleanup(func() {
		defaultProvider.Store(previous)
	})
}

func TestGetTracerUnconfigured(t *testing.T) {
	resetDefaultProvider(t)
	tracer := GetTracer("myapp")
	if tracer.Enabled() {
		t.Fatal("GetTracer() before configuration Enabled() = true, want false")
	}
	if got, want := tracer.Scope(), "myapp"; got != want {
		t.Fatalf("Scope() = %q, want %q", got, want)
	}
	ctx, span := tracer.Start(context.Background(), "command.run")
	if ctx != context.Background() {
		t.Fatal("unconfigured GetTracer().Start returned a different context")
	}
	if span.IsRecording() {
		t.Fatal("unconfigured GetTracer().Start returned a recording span")
	}
}

func TestGetTracerExplicitScopeEquivalence(t *testing.T) {
	resetDefaultProvider(t)
	var provider Provider
	SetDefaultProvider(&provider)
	global := GetTracer("myapp")
	explicit := provider.Tracer("myapp")
	if global != explicit {
		t.Fatalf("GetTracer scope handle = %v, want equal to explicit tracer", global)
	}
	if global.Scope() != explicit.Scope() {
		t.Fatalf("scope mismatch: %q vs %q", global.Scope(), explicit.Scope())
	}
}

func TestSetDefaultProviderNilRestoresNoOp(t *testing.T) {
	resetDefaultProvider(t)
	var provider Provider
	SetDefaultProvider(&provider)
	SetDefaultProvider(nil)
	if GetTracer("myapp").Enabled() {
		t.Fatal("GetTracer() after SetDefaultProvider(nil) Enabled() = true, want false")
	}
}

func TestSetDefaultProviderAffectsFutureLookupsOnly(t *testing.T) {
	resetDefaultProvider(t)
	var first, second Provider
	SetDefaultProvider(&first)
	before := GetTracer("myapp")
	SetDefaultProvider(&second)
	after := GetTracer("myapp")
	if before == after {
		t.Fatal("GetTracer after replacement returned the same tracer handle")
	}
	// Tracer binding to the provider that created it becomes observable with
	// enabled providers in the lifecycle milestone; in this milestone every
	// provider is disabled, so only scope and disabled state are observable.
	if before.Scope() != after.Scope() {
		t.Fatalf("scope changed across replacement: %q vs %q", before.Scope(), after.Scope())
	}
}

func TestSetDefaultProviderConcurrent(t *testing.T) {
	resetDefaultProvider(t)
	providers := []*Provider{{}, {}}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 100 {
				SetDefaultProvider(providers[(i+j)%len(providers)])
				tracer := GetTracer("myapp")
				if tracer.Scope() != "myapp" {
					t.Errorf("scope = %q, want %q", tracer.Scope(), "myapp")
					return
				}
				_, span := tracer.Start(context.Background(), "op")
				span.End()
			}
		}(i)
	}
	wg.Wait()
}

func TestGlobalDisabledStartAllocs(t *testing.T) {
	resetDefaultProvider(t)
	tests := []struct {
		name string
		set  func()
	}{
		{name: "unconfigured", set: func() {}},
		{name: "registered zero provider", set: func() {
			var provider Provider
			SetDefaultProvider(&provider)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetDefaultProvider(nil)
			tt.set()
			allocs := testing.AllocsPerRun(100, func() {
				ctx, span := GetTracer("myapp").Start(context.Background(), "op")
				span.End()
				if ctx == nil {
					t.Fatal("nil context")
				}
			})
			if allocs > 0 {
				t.Fatalf("disabled global Start/End allocated %v times per run, want 0", allocs)
			}
		})
	}
}
