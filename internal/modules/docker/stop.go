package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const (
	connectDeadline = 3 * time.Second
	exitDeadline    = 10 * time.Second
)

// Stop asks for a graceful stop. t=-1 keeps the engine from killing by itself.
func (c *Client) Stop(ctx context.Context, approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Graceful)
	if err != nil || target.Kind != domain.KindContainer {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	return c.act(ctx, target, "/stop?t=-1", exitDeadline)
}

// ForceStop kills an approved container.
func (c *Client) ForceStop(ctx context.Context, approval *domain.Approval) domain.Delivery {
	target, err := approval.Take(domain.Forced)
	if err != nil || target.Kind != domain.KindContainer {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	return c.act(ctx, target, "/kill?signal=SIGKILL", connectDeadline)
}

// act inspects again, then sends exactly one request. Never retried.
func (c *Client) act(ctx context.Context, target domain.StopTarget, action string, limit time.Duration) domain.Delivery {
	end, answer := openEndpoint(target.Container.Engine, c.caller)
	if answer != domain.AnswerOK {
		return domain.Delivery{Code: domain.CodeEngineUnresponsive}
	}
	api := newAPI(end)
	defer api.close()
	checkCtx, cancel := context.WithTimeout(ctx, connectDeadline)
	defer cancel()
	// Other engines may turn t=-1 into a kill
	if kind, err := api.kind(checkCtx); err != nil || kind != kindDocker {
		return domain.Delivery{Code: domain.CodeEngineRejected}
	}
	current, err := api.inspect(checkCtx, target.Container.ID)
	if isStatus(err, http.StatusNotFound) {
		return domain.Delivery{Code: domain.CodeChanged}
	}
	if err != nil {
		return domain.Delivery{Code: domain.CodeEngineUnresponsive}
	}
	gainedPause := current.State.Paused && !target.Paused
	if current.State.StartedAt != target.Container.StartedAt || !current.State.Running || gainedPause {
		return domain.Delivery{Code: domain.CodeChanged}
	}
	return api.post(ctx, "/containers/"+target.Container.ID+action, limit)
}

// post sends one request. A written request that times out was accepted.
func (a *api) post(ctx context.Context, path string, limit time.Duration) domain.Delivery {
	var isWritten atomic.Bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		isWritten.Store(info.Err == nil)
	}}
	waitCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	request, err := http.NewRequestWithContext(httptrace.WithClientTrace(waitCtx, trace), http.MethodPost, "http://engine"+path, nil)
	if err != nil {
		return domain.Delivery{Code: domain.CodeInternal}
	}
	response, err := a.http.Do(request)
	if err == nil {
		_ = response.Body.Close()
		return deliveryFor(response.StatusCode)
	}
	isOurTimeout := errors.Is(waitCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	switch {
	case !isWritten.Load():
		return domain.Delivery{Code: domain.CodeEngineUnresponsive}
	case isOurTimeout:
		return domain.Delivery{Accepted: true}
	}
	return domain.Delivery{Code: domain.CodeUnknownOutcome}
}

// deliveryFor maps the engine's answer. Only success counts as accepted.
func deliveryFor(status int) domain.Delivery {
	switch {
	case status == http.StatusNoContent || status == http.StatusOK:
		return domain.Delivery{Accepted: true}
	case status == http.StatusNotModified || status == http.StatusNotFound || status == http.StatusConflict:
		return domain.Delivery{Code: domain.CodeChanged}
	case status >= 400 && status < 500:
		return domain.Delivery{Code: domain.CodeEngineRejected}
	}
	return domain.Delivery{Code: domain.CodeUnknownOutcome}
}

// State reads a container again. Not found means it is gone.
func (c *Client) State(ctx context.Context, container domain.ContainerRef) domain.ContainerState {
	end, answer := openEndpoint(container.Engine, c.caller)
	if answer != domain.AnswerOK {
		return domain.ContainerState{}
	}
	api := newAPI(end)
	defer api.close()
	ctx, cancel := context.WithTimeout(ctx, connectDeadline)
	defer cancel()
	found, err := api.inspect(ctx, container.ID)
	if isStatus(err, http.StatusNotFound) {
		return domain.ContainerState{Known: true}
	}
	if err != nil {
		return domain.ContainerState{}
	}
	return domain.ContainerState{Known: true, Running: found.State.Running, StartedAt: found.State.StartedAt}
}

// isStatus matches one HTTP status.
func isStatus(err error, status int) bool {
	var found statusError
	return errors.As(err, &found) && int(found) == status
}
