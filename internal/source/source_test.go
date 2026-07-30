package source

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/EOEboh/prospects-cli/internal/model"
)

type stubSource struct {
	name    model.SignalSource
	enabled bool
	signals []model.Signal
	err     error
	called  bool
}

func (s *stubSource) Name() model.SignalSource { return s.name }
func (s *stubSource) Enabled() bool            { return s.enabled }

func (s *stubSource) Collect(context.Context, *model.Business) ([]model.Signal, error) {
	s.called = true
	return s.signals, s.err
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func signalFrom(source model.SignalSource) model.Signal {
	return model.Signal{Source: source, Type: model.TypeContactForm, Value: "true"}
}

// The central promise of the source interface: a missing API key degrades to a
// no-op with a warning, and the run continues. A disabled source must never be
// called and must never fail anything.
func TestRunSkipsDisabledSources(t *testing.T) {
	disabled := &stubSource{name: model.SourcePlaces, enabled: false}
	working := &stubSource{
		name: model.SourceWebsite, enabled: true,
		signals: []model.Signal{signalFrom(model.SourceWebsite)},
	}

	got := Run(context.Background(), discard(), []Source{disabled, working}, &model.Business{ID: 1})

	if disabled.called {
		t.Error("a disabled source must not be called")
	}
	if !working.called {
		t.Error("an enabled source should still run")
	}
	if len(got) != 1 {
		t.Fatalf("%d signals, want 1 from the enabled source", len(got))
	}
	if got[0].Source != model.SourceWebsite {
		t.Errorf("signal came from %q, want website", got[0].Source)
	}
}

// One source failing must not cost the others their results.
func TestRunContinuesPastAFailingSource(t *testing.T) {
	failing := &stubSource{name: model.SourceMetaAds, enabled: true, err: errors.New("token expired")}
	working := &stubSource{
		name: model.SourceWebsite, enabled: true,
		signals: []model.Signal{signalFrom(model.SourceWebsite)},
	}

	// Order matters: the failure comes first, so a naive implementation that
	// returned early would lose the working source entirely.
	got := Run(context.Background(), discard(), []Source{failing, working}, &model.Business{ID: 1})

	if !working.called {
		t.Error("a later source should still run after an earlier one failed")
	}
	if len(got) != 1 {
		t.Errorf("%d signals, want the 1 from the source that worked", len(got))
	}
}

func TestRunCombinesEverySource(t *testing.T) {
	a := &stubSource{name: model.SourceWebsite, enabled: true,
		signals: []model.Signal{signalFrom(model.SourceWebsite)}}
	b := &stubSource{name: model.SourceMetaAds, enabled: true,
		signals: []model.Signal{signalFrom(model.SourceMetaAds)}}

	got := Run(context.Background(), discard(), []Source{a, b}, &model.Business{ID: 1})
	if len(got) != 2 {
		t.Fatalf("%d signals, want 2", len(got))
	}
}

// No sources at all is a legitimate configuration, not a crash.
func TestRunWithNoSources(t *testing.T) {
	if got := Run(context.Background(), discard(), nil, &model.Business{ID: 1}); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// Every source that yields nothing is fine: absence of signals is a fact about
// the business, not an error.
func TestRunWithNoSignals(t *testing.T) {
	empty := &stubSource{name: model.SourceWebsite, enabled: true}
	if got := Run(context.Background(), discard(), []Source{empty}, &model.Business{ID: 1}); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
	if !empty.called {
		t.Error("the source should still have been consulted")
	}
}
