// Package garm is a client for the parts of the garm REST API that
// garm-reconcile uses.
package garm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudbase/garm/params"
)

// Client talks to the API of one garm controller. It is not safe for
// concurrent use.
type Client struct {
	base          *url.URL
	http          *http.Client
	token         string
	readOnly      bool
	retryInterval time.Duration
}

// NewClient returns a client for the controller at baseURL, for example
// http://127.0.0.1:9997.
func NewClient(baseURL string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%q is not an absolute URL", baseURL)
	}
	return &Client{
		base:          u.JoinPath("api", "v1"),
		http:          &http.Client{Timeout: 30 * time.Second},
		retryInterval: time.Second,
	}, nil
}

// ReadOnly makes the client refuse all requests that change the controller.
func (c *Client) ReadOnly() {
	c.readOnly = true
}

// WaitReady blocks until the API answers requests or ctx is done.
func (c *Client) WaitReady(ctx context.Context) error {
	for {
		// Garm answers this request with an error until it is set up,
		// while a server error comes from a proxy that waits for garm.
		err := c.do(ctx, http.MethodGet, nil, nil, "controller-info")
		var apiErr *Error
		if err == nil || (errors.As(err, &apiErr) && apiErr.StatusCode < http.StatusInternalServerError) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for garm: %w", err)
		case <-time.After(c.retryInterval):
		}
	}
}

// FirstRun creates the admin user of a controller that has none.
func (c *Client) FirstRun(ctx context.Context, admin params.NewUserParams) error {
	return c.do(ctx, http.MethodPost, admin, nil, "first-run")
}

// Login authenticates all further calls as the given user.
func (c *Client) Login(ctx context.Context, username, password string) error {
	login := params.PasswordLoginParams{Username: username, Password: password}
	var resp params.JWTResponse
	if err := c.request(ctx, http.MethodPost, login, &resp, "auth", "login"); err != nil {
		return err
	}
	c.token = resp.Token
	return nil
}

func (c *Client) ControllerInfo(ctx context.Context) (params.ControllerInfo, error) {
	var info params.ControllerInfo
	err := c.do(ctx, http.MethodGet, nil, &info, "controller")
	return info, err
}

func (c *Client) UpdateController(ctx context.Context, update params.UpdateControllerParams) error {
	return c.do(ctx, http.MethodPut, update, nil, "controller")
}

func (c *Client) ListGithubCredentials(ctx context.Context) ([]params.ForgeCredentials, error) {
	var creds []params.ForgeCredentials
	err := c.do(ctx, http.MethodGet, nil, &creds, "github", "credentials")
	return creds, err
}

func (c *Client) CreateGithubCredentials(ctx context.Context, create params.CreateGithubCredentialsParams) error {
	return c.do(ctx, http.MethodPost, create, nil, "github", "credentials")
}

func (c *Client) UpdateGithubCredentials(ctx context.Context, id uint, update params.UpdateGithubCredentialsParams) error {
	return c.do(ctx, http.MethodPut, update, nil, "github", "credentials", formatID(id))
}

func (c *Client) DeleteGithubCredentials(ctx context.Context, id uint) error {
	return c.do(ctx, http.MethodDelete, nil, nil, "github", "credentials", formatID(id))
}

func (c *Client) ListEntities(ctx context.Context, kind EntityKind) ([]Entity, error) {
	var entities []Entity
	err := c.do(ctx, http.MethodGet, nil, &entities, kind.collection())
	return entities, err
}

func (c *Client) CreateEntity(ctx context.Context, kind EntityKind, create CreateEntityParams) (Entity, error) {
	var entity Entity
	err := c.do(ctx, http.MethodPost, create, &entity, kind.collection())
	return entity, err
}

func (c *Client) UpdateEntity(ctx context.Context, kind EntityKind, id string, update params.UpdateEntityParams) error {
	return c.do(ctx, http.MethodPut, update, nil, kind.collection(), id)
}

func (c *Client) DeleteEntity(ctx context.Context, kind EntityKind, id string) error {
	return c.do(ctx, http.MethodDelete, nil, nil, kind.collection(), id)
}

// ListEntityPools lists the pools of an entity. Listed pools are incomplete,
// garm leaves out their extra specs for one. GetPool returns a complete
// pool.
func (c *Client) ListEntityPools(ctx context.Context, kind EntityKind, entityID string) ([]params.Pool, error) {
	var pools []params.Pool
	err := c.do(ctx, http.MethodGet, nil, &pools, kind.collection(), entityID, "pools")
	return pools, err
}

func (c *Client) GetPool(ctx context.Context, id string) (params.Pool, error) {
	var pool params.Pool
	err := c.do(ctx, http.MethodGet, nil, &pool, "pools", id)
	return pool, err
}

func (c *Client) CreateEntityPool(ctx context.Context, kind EntityKind, entityID string, create params.CreatePoolParams) error {
	return c.do(ctx, http.MethodPost, create, nil, kind.collection(), entityID, "pools")
}

func (c *Client) UpdatePool(ctx context.Context, id string, update params.UpdatePoolParams) error {
	return c.do(ctx, http.MethodPut, update, nil, "pools", id)
}

// DeletePool deletes a pool. Garm rejects the request with a bad request
// error while the pool has runners.
func (c *Client) DeletePool(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, nil, nil, "pools", id)
}

func (c *Client) ListPoolInstances(ctx context.Context, poolID string) ([]params.Instance, error) {
	var instances []params.Instance
	err := c.do(ctx, http.MethodGet, nil, &instances, "pools", poolID, "instances")
	return instances, err
}

// DeleteInstance removes a runner from its forge and deletes its instance.
// Garm rejects the request with a bad request error when the runner is
// executing a job or is not fully set up yet.
func (c *Client) DeleteInstance(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, nil, nil, "instances", name)
}

// do sends a request to the API path made of elem. body is sent as JSON
// unless it is nil, and the response is decoded into result unless that is
// nil.
func (c *Client) do(ctx context.Context, method string, body, result any, elem ...string) error {
	if c.readOnly && method != http.MethodGet {
		return fmt.Errorf("refusing %s %s with a read-only client", method, strings.Join(elem, "/"))
	}
	return c.request(ctx, method, body, result, elem...)
}

func (c *Client) request(ctx context.Context, method string, body, result any, elem ...string) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.JoinPath(elem...).String(), reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return newError(resp)
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decoding response of %s %s: %w", method, req.URL.Path, err)
	}
	return nil
}

func formatID(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
