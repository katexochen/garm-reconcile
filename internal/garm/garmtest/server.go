// Package garmtest provides an in-memory fake of the garm API. It implements
// the endpoints garm-reconcile uses and mirrors the rules garm enforces on
// them, like refusing to delete a pool that has runners.
package garmtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	commonparams "github.com/cloudbase/garm-provider-common/params"
	apiparams "github.com/cloudbase/garm/apiserver/params"
	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/util/appdefaults"

	"github.com/katexochen/garm-reconcile/internal/garm"
)

const token = "garmtest-token"

// State is everything the fake controller stores.
type State struct {
	// Admin is nil until the controller is initialized.
	Admin       *params.NewUserParams
	Controller  params.ControllerInfo
	Endpoints   []*params.ForgeEndpoint
	Credentials []*Credentials
	Templates   []*params.Template
	Entities    map[garm.EntityKind][]*Entity
	Pools       []*params.Pool
	ScaleSets   []*params.ScaleSet
	Instances   []*params.Instance
	// KeepDeletedScaleSets makes the controller answer requests to delete
	// a scale set with success without deleting it, which is what garm
	// does when GitHub fails to delete its side.
	KeepDeletedScaleSets bool
	// Mutations lists the requests that changed the state, as
	// "METHOD /path".
	Mutations []string

	nextID uint
}

// Credentials are GitHub credentials including the secret garm never
// returns.
type Credentials struct {
	params.ForgeCredentials
	PAT params.GithubPAT
	App params.GithubApp
}

// Entity is an entity including the webhook secret garm never returns.
type Entity struct {
	garm.Entity
	WebhookSecret string
}

// Server is a fake garm controller.
type Server struct {
	URL string

	mu    sync.Mutex
	state State
}

// NewServer starts a fake controller that is shut down when the test ends.
func NewServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{state: State{Entities: map[garm.EntityKind][]*Entity{}}}
	// Garm ships templates.
	s.state.Templates = []*params.Template{{
		ID: s.state.newID(), Name: "github_linux", OSType: commonparams.Linux,
		ForgeType: params.GithubEndpointType, Owner: params.SystemUser, Data: []byte("#!/bin/sh"),
	}, {
		ID: s.state.newID(), Name: "github_windows", OSType: commonparams.Windows,
		ForgeType: params.GithubEndpointType, Owner: params.SystemUser, Data: []byte("#ps1"),
	}, {
		ID: s.state.newID(), Name: "gitea_linux", OSType: commonparams.Linux,
		ForgeType: params.GiteaEndpointType, Owner: params.SystemUser, Data: []byte("#!/bin/sh"),
	}}
	// It also has an endpoint for github.com.
	s.state.Endpoints = []*params.ForgeEndpoint{{
		Name: "github.com", EndpointType: params.GithubEndpointType,
		APIBaseURL: "https://api.github.com", BaseURL: "https://github.com", UploadBaseURL: "https://uploads.github.com",
	}}

	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/first-run", s.route(public, (*State).firstRun))
	mux.Handle("POST /api/v1/auth/login", s.route(initialized, (*State).login))
	mux.Handle("GET /api/v1/controller", s.route(authenticated, (*State).controllerInfo))
	mux.Handle("PUT /api/v1/controller", s.route(authenticated, (*State).updateController))
	mux.Handle("GET /api/v1/controller-info", s.route(configured, (*State).controllerInfo))
	for _, forge := range []params.EndpointType{params.GithubEndpointType, params.GiteaEndpointType} {
		prefix := "/api/v1/" + string(forge)
		mux.Handle("GET "+prefix+"/endpoints", s.route(configured, listEndpoints(forge)))
		mux.Handle("POST "+prefix+"/endpoints", s.route(configured, createEndpoint(forge)))
		mux.Handle("PUT "+prefix+"/endpoints/{name}", s.route(configured, updateEndpoint(forge)))
		mux.Handle("DELETE "+prefix+"/endpoints/{name}", s.route(configured, deleteEndpoint(forge)))
		mux.Handle("GET "+prefix+"/credentials", s.route(configured, listCredentials(forge)))
		mux.Handle("POST "+prefix+"/credentials", s.route(configured, createCredentials(forge)))
		mux.Handle("PUT "+prefix+"/credentials/{id}", s.route(configured, updateCredentials(forge)))
		mux.Handle("DELETE "+prefix+"/credentials/{id}", s.route(configured, deleteCredentials(forge)))
	}
	mux.Handle("GET /api/v1/templates", s.route(configured, (*State).listTemplates))
	mux.Handle("POST /api/v1/templates", s.route(configured, (*State).createTemplate))
	mux.Handle("GET /api/v1/templates/{id}", s.route(configured, (*State).getTemplate))
	mux.Handle("PUT /api/v1/templates/{id}", s.route(configured, (*State).updateTemplate))
	mux.Handle("DELETE /api/v1/templates/{id}", s.route(configured, (*State).deleteTemplate))
	for kind, collection := range map[garm.EntityKind]string{
		garm.Repository:   "repositories",
		garm.Organization: "organizations",
		garm.Enterprise:   "enterprises",
	} {
		prefix := "/api/v1/" + collection
		mux.Handle("GET "+prefix, s.route(configured, listEntities(kind)))
		mux.Handle("POST "+prefix, s.route(configured, createEntity(kind)))
		mux.Handle("PUT "+prefix+"/{id}", s.route(configured, updateEntity(kind)))
		mux.Handle("DELETE "+prefix+"/{id}", s.route(configured, deleteEntity(kind)))
		mux.Handle("GET "+prefix+"/{id}/pools", s.route(configured, listEntityPools(kind)))
		mux.Handle("POST "+prefix+"/{id}/pools", s.route(configured, createEntityPool(kind)))
		mux.Handle("GET "+prefix+"/{id}/scalesets", s.route(configured, listEntityScaleSets(kind)))
		mux.Handle("POST "+prefix+"/{id}/scalesets", s.route(configured, createEntityScaleSet(kind)))
	}
	mux.Handle("GET /api/v1/pools/{id}", s.route(configured, (*State).getPool))
	mux.Handle("PUT /api/v1/pools/{id}", s.route(configured, (*State).updatePool))
	mux.Handle("DELETE /api/v1/pools/{id}", s.route(configured, (*State).deletePool))
	mux.Handle("GET /api/v1/pools/{id}/instances", s.route(configured, (*State).listPoolInstances))
	mux.Handle("GET /api/v1/scalesets/{id}", s.route(configured, (*State).getScaleSet))
	mux.Handle("PUT /api/v1/scalesets/{id}", s.route(configured, (*State).updateScaleSet))
	mux.Handle("DELETE /api/v1/scalesets/{id}", s.route(configured, (*State).deleteScaleSet))
	mux.Handle("GET /api/v1/scalesets/{id}/instances", s.route(configured, (*State).listScaleSetInstances))
	mux.Handle("DELETE /api/v1/instances/{name}", s.route(configured, (*State).deleteInstance))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// State calls fn with the state of the controller, which fn may inspect and
// modify. Requests are blocked while fn runs.
func (s *Server) State(fn func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.state)
}

// access is what garm requires of the controller and the caller before it
// serves an endpoint.
type access int

const (
	public access = iota
	// initialized endpoints need an admin user to exist.
	initialized
	// authenticated endpoints need the caller to be logged in.
	authenticated
	// configured endpoints need the controller URLs to be set as well.
	configured
)

type apiError struct {
	status  int
	code    string
	details string
}

func (e *apiError) Error() string { return e.details }

func badRequest(format string, args ...any) error {
	return &apiError{http.StatusBadRequest, "Bad Request", fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &apiError{http.StatusConflict, "Conflict", fmt.Sprintf(format, args...)}
}

func notFound() error {
	return &apiError{http.StatusNotFound, apiparams.NotFoundResponse.Error, apiparams.NotFoundResponse.Details}
}

// handler serves a request on the locked state and returns the response
// body.
type handler func(st *State, r *http.Request) (any, error)

func (s *Server) route(level access, handle handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		resp, err := s.serve(level, handle, r)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			apiErr, ok := err.(*apiError)
			if !ok {
				apiErr = &apiError{http.StatusBadRequest, "Bad Request", err.Error()}
			}
			w.WriteHeader(apiErr.status)
			resp = apiparams.APIErrorResponse{Error: apiErr.code, Details: apiErr.details}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func (s *Server) serve(level access, handle handler, r *http.Request) (any, error) {
	st := &s.state
	if level >= initialized && st.Admin == nil {
		e := apiparams.InitializationRequired
		return nil, &apiError{http.StatusConflict, e.Error, e.Details}
	}
	ctrl := st.Controller
	if level >= configured && (ctrl.MetadataURL == "" || ctrl.CallbackURL == "" || ctrl.AgentURL == "") {
		e := apiparams.URLsRequired
		return nil, &apiError{http.StatusConflict, e.Error, e.Details}
	}
	if level >= authenticated && r.Header.Get("Authorization") != "Bearer "+token {
		e := apiparams.UnauthorizedResponse
		return nil, &apiError{http.StatusUnauthorized, e.Error, e.Details}
	}

	resp, err := handle(st, r)
	if err == nil && r.Method != http.MethodGet && r.URL.Path != "/api/v1/auth/login" {
		st.Mutations = append(st.Mutations, r.Method+" "+r.URL.Path)
	}
	return resp, err
}

func decode[T any](r *http.Request) (T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return v, badRequest("invalid request body: %v", err)
	}
	return v, nil
}

func (st *State) newID() uint {
	st.nextID++
	return st.nextID
}

func (st *State) firstRun(r *http.Request) (any, error) {
	if st.Admin != nil {
		return nil, conflict("already initialized")
	}
	admin, err := decode[params.NewUserParams](r)
	if err != nil {
		return nil, err
	}
	if admin.Username == "" || admin.Email == "" || admin.Password == "" {
		return nil, badRequest("missing username, password or email")
	}
	st.Admin = &admin
	return params.User{Username: admin.Username, Email: admin.Email, FullName: admin.FullName}, nil
}

func (st *State) login(r *http.Request) (any, error) {
	login, err := decode[params.PasswordLoginParams](r)
	if err != nil {
		return nil, err
	}
	if login.Username != st.Admin.Username || login.Password != st.Admin.Password {
		e := apiparams.UnauthorizedResponse
		return nil, &apiError{http.StatusUnauthorized, e.Error, e.Details}
	}
	return params.JWTResponse{Token: token}, nil
}

func (st *State) controllerInfo(_ *http.Request) (any, error) {
	return st.Controller, nil
}

func (st *State) updateController(r *http.Request) (any, error) {
	update, err := decode[params.UpdateControllerParams](r)
	if err != nil {
		return nil, err
	}
	if err := update.Validate(); err != nil {
		return nil, badRequest("%v", err)
	}
	for _, field := range []struct{ dst, src *string }{
		{&st.Controller.MetadataURL, update.MetadataURL},
		{&st.Controller.CallbackURL, update.CallbackURL},
		{&st.Controller.WebhookURL, update.WebhookURL},
		{&st.Controller.AgentURL, update.AgentURL},
	} {
		if field.src != nil {
			*field.dst = *field.src
		}
	}
	return st.Controller, nil
}

func (st *State) findEndpoint(forge params.EndpointType, name string) *params.ForgeEndpoint {
	for _, e := range st.Endpoints {
		if e.EndpointType == forge && strings.EqualFold(e.Name, name) {
			return e
		}
	}
	return nil
}

func listEndpoints(forge params.EndpointType) handler {
	return func(st *State, _ *http.Request) (any, error) {
		endpoints := []params.ForgeEndpoint{}
		for _, e := range st.Endpoints {
			if e.EndpointType == forge {
				endpoints = append(endpoints, *e)
			}
		}
		return endpoints, nil
	}
}

func createEndpoint(forge params.EndpointType) handler {
	return func(st *State, r *http.Request) (any, error) {
		create, err := decode[garm.CreateEndpointParams](r)
		if err != nil {
			return nil, err
		}
		if create.Name == "" || create.APIBaseURL == "" || create.BaseURL == "" {
			return nil, badRequest("missing name or URL")
		}
		if forge == params.GithubEndpointType && create.UploadBaseURL == "" {
			return nil, badRequest("missing upload_base_url")
		}
		if st.findEndpoint(forge, create.Name) != nil {
			return nil, conflict("%s endpoint already exists", forge)
		}
		endpoint := &params.ForgeEndpoint{
			Name: create.Name, Description: create.Description, EndpointType: forge,
			APIBaseURL: create.APIBaseURL, BaseURL: create.BaseURL, UploadBaseURL: create.UploadBaseURL,
			CACertBundle: create.CACertBundle, ToolsMetadataURL: create.ToolsMetadataURL,
		}
		st.Endpoints = append(st.Endpoints, endpoint)
		return *endpoint, nil
	}
}

func (st *State) endpointInUse(endpoint *params.ForgeEndpoint) bool {
	return slices.ContainsFunc(st.Credentials, func(c *Credentials) bool {
		return c.ForgeType == endpoint.EndpointType && c.Endpoint.Name == endpoint.Name
	})
}

func updateEndpoint(forge params.EndpointType) handler {
	return func(st *State, r *http.Request) (any, error) {
		endpoint := st.findEndpoint(forge, r.PathValue("name"))
		if endpoint == nil {
			return nil, notFound()
		}
		update, err := decode[garm.UpdateEndpointParams](r)
		if err != nil {
			return nil, err
		}
		changesURL := (update.APIBaseURL != nil && *update.APIBaseURL != endpoint.APIBaseURL) ||
			(update.BaseURL != nil && *update.BaseURL != endpoint.BaseURL)
		if changesURL && st.endpointInUse(endpoint) {
			return nil, badRequest("cannot update endpoint URLs with existing credentials")
		}
		setPointer(&endpoint.Description, update.Description)
		setPointer(&endpoint.APIBaseURL, update.APIBaseURL)
		setPointer(&endpoint.BaseURL, update.BaseURL)
		setPointer(&endpoint.UploadBaseURL, update.UploadBaseURL)
		setString(&endpoint.ToolsMetadataURL, update.ToolsMetadataURL)
		if update.CACertBundle != nil {
			endpoint.CACertBundle = update.CACertBundle
		}
		return *endpoint, nil
	}
}

func deleteEndpoint(forge params.EndpointType) handler {
	return func(st *State, r *http.Request) (any, error) {
		endpoint := st.findEndpoint(forge, r.PathValue("name"))
		if endpoint == nil {
			return nil, nil
		}
		if st.endpointInUse(endpoint) {
			return nil, badRequest("cannot delete endpoint with associated entities")
		}
		st.Endpoints = slices.DeleteFunc(st.Endpoints, func(e *params.ForgeEndpoint) bool { return e == endpoint })
		return nil, nil
	}
}

func listCredentials(forge params.EndpointType) handler {
	return func(st *State, _ *http.Request) (any, error) {
		creds := []params.ForgeCredentials{}
		for _, c := range st.Credentials {
			if c.ForgeType == forge {
				creds = append(creds, c.ForgeCredentials)
			}
		}
		return creds, nil
	}
}

func (st *State) findCredentials(forge params.EndpointType, name string) *Credentials {
	for _, c := range st.Credentials {
		if c.ForgeType == forge && strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return nil
}

func createCredentials(forge params.EndpointType) handler {
	return func(st *State, r *http.Request) (any, error) {
		create, err := decode[params.CreateGithubCredentialsParams](r)
		if err != nil {
			return nil, err
		}
		if err := create.Validate(); err != nil {
			return nil, badRequest("%v", err)
		}
		if forge == params.GiteaEndpointType && create.AuthType != params.ForgeAuthTypePAT {
			return nil, badRequest("invalid auth_type: %s", create.AuthType)
		}
		endpoint := st.findEndpoint(forge, create.Endpoint)
		if endpoint == nil {
			return nil, notFound()
		}
		if st.findCredentials(forge, create.Name) != nil {
			return nil, conflict("%s credentials already exists", forge)
		}
		creds := &Credentials{
			ForgeCredentials: params.ForgeCredentials{
				ID:          st.newID(),
				Name:        create.Name,
				Description: create.Description,
				AuthType:    create.AuthType,
				ForgeType:   forge,
				Endpoint:    *endpoint,
			},
			PAT: create.PAT,
			App: create.App,
		}
		st.Credentials = append(st.Credentials, creds)
		return creds.ForgeCredentials, nil
	}
}

func (st *State) credentialsByID(forge params.EndpointType, r *http.Request) (*Credentials, error) {
	for _, c := range st.Credentials {
		if c.ForgeType == forge && formatID(c.ID) == r.PathValue("id") {
			return c, nil
		}
	}
	return nil, notFound()
}

func updateCredentials(forge params.EndpointType) handler {
	return func(st *State, r *http.Request) (any, error) {
		creds, err := st.credentialsByID(forge, r)
		if err != nil {
			return nil, err
		}
		update, err := decode[params.UpdateGithubCredentialsParams](r)
		if err != nil {
			return nil, err
		}
		if err := update.Validate(); err != nil {
			return nil, badRequest("%v", err)
		}
		if update.PAT != nil && creds.AuthType != params.ForgeAuthTypePAT {
			return nil, badRequest("cannot update PAT credentials for app")
		}
		if update.App != nil && creds.AuthType != params.ForgeAuthTypeApp {
			return nil, badRequest("cannot update app credentials for PAT")
		}
		setPointer(&creds.Name, update.Name)
		setPointer(&creds.Description, update.Description)
		setPointer(&creds.PAT, update.PAT)
		setPointer(&creds.App, update.App)
		return creds.ForgeCredentials, nil
	}
}

func deleteCredentials(forge params.EndpointType) handler {
	return func(st *State, r *http.Request) (any, error) {
		creds, err := st.credentialsByID(forge, r)
		if err != nil {
			return nil, err
		}
		for _, entities := range st.Entities {
			for _, e := range entities {
				if e.Endpoint.EndpointType == forge && strings.EqualFold(e.CredentialsName, creds.Name) {
					return nil, badRequest("cannot delete credentials with entities")
				}
			}
		}
		st.Credentials = slices.DeleteFunc(st.Credentials, func(c *Credentials) bool { return c == creds })
		return nil, nil
	}
}

// listTemplates leaves out the data of the templates, like garm.
func (st *State) listTemplates(_ *http.Request) (any, error) {
	templates := []params.Template{}
	for _, t := range st.Templates {
		listed := *t
		listed.Data = nil
		templates = append(templates, listed)
	}
	return templates, nil
}

func (st *State) templateByID(id string) *params.Template {
	for _, t := range st.Templates {
		if formatID(t.ID) == id {
			return t
		}
	}
	return nil
}

func formatID(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}

func (st *State) getTemplate(r *http.Request) (any, error) {
	template := st.templateByID(r.PathValue("id"))
	if template == nil {
		return nil, notFound()
	}
	return *template, nil
}

func (st *State) createTemplate(r *http.Request) (any, error) {
	create, err := decode[params.CreateTemplateParams](r)
	if err != nil {
		return nil, err
	}
	if err := create.Validate(); err != nil {
		return nil, badRequest("invalid create params %q", err)
	}
	for _, t := range st.Templates {
		if t.Name == create.Name && t.Owner != params.SystemUser {
			return nil, conflict("a template name already exists with the specified name")
		}
	}
	template := &params.Template{
		ID: st.newID(), Name: create.Name, Description: create.Description,
		OSType: create.OSType, ForgeType: create.ForgeType, Data: create.Data, Owner: st.Admin.Username,
	}
	st.Templates = append(st.Templates, template)
	return *template, nil
}

func (st *State) updateTemplate(r *http.Request) (any, error) {
	template := st.templateByID(r.PathValue("id"))
	if template == nil {
		return nil, notFound()
	}
	update, err := decode[params.UpdateTemplateParams](r)
	if err != nil {
		return nil, err
	}
	setPointer(&template.Name, update.Name)
	setPointer(&template.Description, update.Description)
	if len(update.Data) > 0 {
		template.Data = update.Data
	}
	return *template, nil
}

func (st *State) deleteTemplate(r *http.Request) (any, error) {
	template := st.templateByID(r.PathValue("id"))
	if template == nil {
		return nil, nil
	}
	for _, p := range st.Pools {
		if p.TemplateID == template.ID {
			return nil, badRequest("cannot delete template while in use by pools or scale sets")
		}
	}
	for _, set := range st.ScaleSets {
		if set.TemplateID == template.ID {
			return nil, badRequest("cannot delete template while in use by pools or scale sets")
		}
	}
	st.Templates = slices.DeleteFunc(st.Templates, func(t *params.Template) bool { return t == template })
	return nil, nil
}

// wireEntity converts an entity to the type garm serializes for its kind.
func wireEntity(kind garm.EntityKind, e *Entity) any {
	switch kind {
	case garm.Repository:
		return params.Repository{
			ID: e.ID, Owner: e.Owner, Name: e.Name, CredentialsName: e.CredentialsName,
			PoolBalancerType: e.PoolBalancerType, AgentMode: e.AgentMode, Endpoint: e.Endpoint,
		}
	case garm.Organization:
		return params.Organization{
			ID: e.ID, Name: e.Name, CredentialsName: e.CredentialsName,
			PoolBalancerType: e.PoolBalancerType, AgentMode: e.AgentMode, Endpoint: e.Endpoint,
		}
	default:
		return params.Enterprise{
			ID: e.ID, Name: e.Name, CredentialsName: e.CredentialsName,
			PoolBalancerType: e.PoolBalancerType, AgentMode: e.AgentMode, Endpoint: e.Endpoint,
		}
	}
}

func listEntities(kind garm.EntityKind) handler {
	return func(st *State, _ *http.Request) (any, error) {
		entities := []any{}
		for _, e := range st.Entities[kind] {
			entities = append(entities, wireEntity(kind, e))
		}
		return entities, nil
	}
}

func createEntity(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		create, err := decode[garm.CreateEntityParams](r)
		if err != nil {
			return nil, err
		}
		if create.Name == "" || (kind == garm.Repository && create.Owner == "") {
			return nil, badRequest("missing name")
		}
		if create.WebhookSecret == "" {
			return nil, badRequest("missing secret")
		}
		forge := create.ForgeType
		if forge == params.AutoEndpointType {
			forge = params.GithubEndpointType
		}
		if forge == params.GiteaEndpointType && kind == garm.Enterprise {
			return nil, badRequest("invalid forge type")
		}
		creds := st.findCredentials(forge, create.CredentialsName)
		if creds == nil {
			return nil, badRequest("credentials %s not defined", create.CredentialsName)
		}
		entity := &Entity{
			Entity: garm.Entity{
				Owner:            create.Owner,
				Name:             create.Name,
				CredentialsName:  creds.Name,
				PoolBalancerType: create.PoolBalancerType,
				AgentMode:        create.AgentMode,
				Endpoint:         creds.Endpoint,
			},
			WebhookSecret: create.WebhookSecret,
		}
		for _, e := range st.Entities[kind] {
			if strings.EqualFold(e.FullName(), entity.FullName()) && e.Endpoint.Name == entity.Endpoint.Name {
				return nil, conflict("%s %s already exists", kind, entity.FullName())
			}
		}
		entity.ID = fmt.Sprintf("%s-%d", kind, st.newID())
		st.Entities[kind] = append(st.Entities[kind], entity)
		return wireEntity(kind, entity), nil
	}
}

func (st *State) entityByID(kind garm.EntityKind, r *http.Request) (*Entity, error) {
	for _, e := range st.Entities[kind] {
		if e.ID == r.PathValue("id") {
			return e, nil
		}
	}
	return nil, notFound()
}

func updateEntity(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		entity, err := st.entityByID(kind, r)
		if err != nil {
			return nil, err
		}
		update, err := decode[params.UpdateEntityParams](r)
		if err != nil {
			return nil, err
		}
		if update.CredentialsName != "" {
			creds := st.findCredentials(entity.Endpoint.EndpointType, update.CredentialsName)
			if creds == nil {
				return nil, badRequest("credentials %s not defined", update.CredentialsName)
			}
			if creds.Endpoint.Name != entity.Endpoint.Name {
				return nil, badRequest("endpoint mismatch")
			}
			entity.CredentialsName = creds.Name
		}
		if update.WebhookSecret != "" {
			entity.WebhookSecret = update.WebhookSecret
		}
		if update.PoolBalancerType != "" {
			entity.PoolBalancerType = update.PoolBalancerType
		}
		if update.AgentMode != nil {
			entity.AgentMode = *update.AgentMode
		}
		return wireEntity(kind, entity), nil
	}
}

func (st *State) entityPools(entity *Entity) []*params.Pool {
	var pools []*params.Pool
	for _, p := range st.Pools {
		if entity.ID == p.RepoID || entity.ID == p.OrgID || entity.ID == p.EnterpriseID {
			pools = append(pools, p)
		}
	}
	return pools
}

func deleteEntity(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		entity, err := st.entityByID(kind, r)
		if err != nil {
			return nil, err
		}
		if len(st.entityPools(entity)) > 0 {
			return nil, badRequest("%s has pools defined", kind)
		}
		if len(st.entityScaleSets(entity)) > 0 {
			return nil, badRequest("%s has scale sets defined; delete them first", kind)
		}
		st.Entities[kind] = slices.DeleteFunc(st.Entities[kind], func(e *Entity) bool { return e == entity })
		return nil, nil
	}
}

func listEntityPools(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		entity, err := st.entityByID(kind, r)
		if err != nil {
			return nil, err
		}
		// Like garm, the listing leaves out some settings of the pools.
		pools := []params.Pool{}
		for _, p := range st.entityPools(entity) {
			listed := *p
			listed.ExtraSpecs = nil
			listed.RunnerBootstrapTimeout = 0
			listed.TemplateName = ""
			pools = append(pools, listed)
		}
		return pools, nil
	}
}

func createEntityPool(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		entity, err := st.entityByID(kind, r)
		if err != nil {
			return nil, err
		}
		create, err := decode[params.CreatePoolParams](r)
		if err != nil {
			return nil, err
		}
		if err := create.Validate(); err != nil {
			return nil, badRequest("failed to validate params: %v", err)
		}
		if create.OSType != commonparams.Linux && create.OSType != commonparams.Windows {
			return nil, badRequest("invalid OS type %s", create.OSType)
		}
		pool := &params.Pool{
			RunnerPrefix:           params.RunnerPrefix{Prefix: create.GetRunnerPrefix()},
			ID:                     fmt.Sprintf("pool-%d", st.newID()),
			ProviderName:           create.ProviderName,
			MaxRunners:             create.MaxRunners,
			MinIdleRunners:         create.MinIdleRunners,
			Image:                  create.Image,
			Flavor:                 create.Flavor,
			OSType:                 create.OSType,
			OSArch:                 create.OSArch,
			Enabled:                create.Enabled,
			EnableShell:            create.EnableShell,
			RunnerBootstrapTimeout: create.RunnerBootstrapTimeout,
			ExtraSpecs:             create.ExtraSpecs,
			GitHubRunnerGroup:      create.GitHubRunnerGroup,
			Priority:               create.Priority,
		}
		for _, tag := range create.Tags {
			pool.Tags = append(pool.Tags, params.Tag{ID: tag, Name: tag})
		}
		if pool.RunnerBootstrapTimeout == 0 {
			pool.RunnerBootstrapTimeout = appdefaults.DefaultRunnerBootstrapTimeout
		}
		// Without a template, garm picks the one it ships for the OS.
		for _, t := range st.Templates {
			if create.TemplateID == nil && t.Owner == params.SystemUser && t.OSType == create.OSType && t.ForgeType == entity.Endpoint.EndpointType {
				create.TemplateID = &t.ID
			}
		}
		if create.TemplateID == nil {
			return nil, badRequest("no template ID supplied and no default template can be found")
		}
		template := st.templateByID(formatID(*create.TemplateID))
		if template == nil {
			return nil, badRequest("failed to find suitable template")
		}
		if template.OSType != create.OSType {
			return nil, badRequest("selected template OS type (%s) and OS type (%s) do not match", template.OSType, create.OSType)
		}
		pool.TemplateID, pool.TemplateName = template.ID, template.Name
		switch kind {
		case garm.Repository:
			pool.RepoID = entity.ID
		case garm.Organization:
			pool.OrgID = entity.ID
		case garm.Enterprise:
			pool.EnterpriseID = entity.ID
		}
		st.Pools = append(st.Pools, pool)
		return *pool, nil
	}
}

func (st *State) poolByID(r *http.Request) *params.Pool {
	for _, p := range st.Pools {
		if p.ID == r.PathValue("id") {
			return p
		}
	}
	return nil
}

func (st *State) getPool(r *http.Request) (any, error) {
	pool := st.poolByID(r)
	if pool == nil {
		return nil, notFound()
	}
	return *pool, nil
}

// updatePool applies an update the way garm does: zero values of fields
// that are not pointers leave the pool unchanged.
func (st *State) updatePool(r *http.Request) (any, error) {
	pool := st.poolByID(r)
	if pool == nil {
		return nil, notFound()
	}
	update, err := decode[params.UpdatePoolParams](r)
	if err != nil {
		return nil, err
	}
	if update.RunnerBootstrapTimeout != nil && *update.RunnerBootstrapTimeout == 0 {
		return nil, badRequest("runner_bootstrap_timeout cannot be 0")
	}

	setString(&pool.Prefix, update.Prefix)
	setString(&pool.Image, update.Image)
	setString(&pool.Flavor, update.Flavor)
	setString((*string)(&pool.OSType), string(update.OSType))
	setString((*string)(&pool.OSArch), string(update.OSArch))
	setPointer(&pool.Enabled, update.Enabled)
	setPointer(&pool.MaxRunners, update.MaxRunners)
	setPointer(&pool.MinIdleRunners, update.MinIdleRunners)
	setPointer(&pool.RunnerBootstrapTimeout, update.RunnerBootstrapTimeout)
	setPointer(&pool.EnableShell, update.EnableShell)
	setPointer(&pool.GitHubRunnerGroup, update.GitHubRunnerGroup)
	setPointer(&pool.Priority, update.Priority)
	if update.TemplateID != nil {
		template := st.templateByID(formatID(*update.TemplateID))
		if template == nil {
			return nil, badRequest("failed to find template")
		}
		pool.TemplateID, pool.TemplateName = template.ID, template.Name
	}
	if update.ExtraSpecs != nil {
		pool.ExtraSpecs = update.ExtraSpecs
	}
	if len(update.Tags) > 0 {
		pool.Tags = nil
		for _, tag := range update.Tags {
			pool.Tags = append(pool.Tags, params.Tag{ID: tag, Name: tag})
		}
	}
	if pool.MinIdleRunners > pool.MaxRunners {
		return nil, badRequest("min_idle_runners cannot be larger than max_runners")
	}
	return *pool, nil
}

func setString(dst *string, src string) {
	if src != "" {
		*dst = src
	}
}

func setPointer[T any](dst, src *T) {
	if src != nil {
		*dst = *src
	}
}

func (st *State) poolInstances(poolID string) []params.Instance {
	instances := []params.Instance{}
	for _, i := range st.Instances {
		if i.PoolID == poolID {
			instances = append(instances, *i)
		}
	}
	return instances
}

func (st *State) deletePool(r *http.Request) (any, error) {
	pool := st.poolByID(r)
	if pool == nil {
		return nil, nil
	}
	if len(st.poolInstances(pool.ID)) > 0 {
		return nil, badRequest("pool has runners")
	}
	st.Pools = slices.DeleteFunc(st.Pools, func(p *params.Pool) bool { return p == pool })
	return nil, nil
}

func (st *State) listPoolInstances(r *http.Request) (any, error) {
	return st.poolInstances(r.PathValue("id")), nil
}

// deleteInstance removes a runner immediately. Like garm, it refuses for
// instances that are still being set up, and like GitHub for runners that
// execute a job.
func (st *State) deleteInstance(r *http.Request) (any, error) {
	idx := slices.IndexFunc(st.Instances, func(i *params.Instance) bool { return i.Name == r.PathValue("name") })
	if idx < 0 {
		return nil, notFound()
	}
	instance := st.Instances[idx]
	switch instance.Status {
	case commonparams.InstanceRunning, commonparams.InstanceError,
		commonparams.InstancePendingDelete, commonparams.InstancePendingForceDelete:
	default:
		return nil, badRequest("runner must be in one of the following states: running, error, pending_delete, pending_force_delete")
	}
	if instance.RunnerStatus == params.RunnerActive {
		return nil, badRequest("error removing runner from github: runner is executing a job")
	}
	st.Instances = slices.Delete(st.Instances, idx, idx+1)
	return nil, nil
}

func (st *State) entityScaleSets(entity *Entity) []*params.ScaleSet {
	var sets []*params.ScaleSet
	for _, set := range st.ScaleSets {
		if entity.ID == set.RepoID || entity.ID == set.OrgID || entity.ID == set.EnterpriseID {
			sets = append(sets, set)
		}
	}
	return sets
}

// listEntityScaleSets leaves out the extra specs, like garm.
func listEntityScaleSets(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		entity, err := st.entityByID(kind, r)
		if err != nil {
			return nil, err
		}
		sets := []params.ScaleSet{}
		for _, set := range st.entityScaleSets(entity) {
			listed := *set
			listed.ExtraSpecs = nil
			sets = append(sets, listed)
		}
		return sets, nil
	}
}

func createEntityScaleSet(kind garm.EntityKind) handler {
	return func(st *State, r *http.Request) (any, error) {
		entity, err := st.entityByID(kind, r)
		if err != nil {
			return nil, err
		}
		create, err := decode[params.CreateScaleSetParams](r)
		if err != nil {
			return nil, err
		}
		if entity.Endpoint.EndpointType != params.GithubEndpointType {
			return nil, badRequest("scale sets are only supported for github entities")
		}
		if err := create.Validate(); err != nil {
			return nil, badRequest("%v", err)
		}
		for _, set := range st.entityScaleSets(entity) {
			if set.Name == create.Name {
				return nil, conflict("scale set %s already exists", create.Name)
			}
		}
		set := &params.ScaleSet{
			RunnerPrefix:           params.RunnerPrefix{Prefix: create.GetRunnerPrefix()},
			ID:                     st.newID(),
			Name:                   create.Name,
			DisableUpdate:          create.DisableUpdate,
			State:                  params.ScaleSetCreated,
			ProviderName:           create.ProviderName,
			MaxRunners:             create.MaxRunners,
			MinIdleRunners:         create.MinIdleRunners,
			Image:                  create.Image,
			Flavor:                 create.Flavor,
			OSType:                 create.OSType,
			OSArch:                 create.OSArch,
			Enabled:                create.Enabled,
			EnableShell:            create.EnableShell,
			RunnerBootstrapTimeout: create.RunnerBootstrapTimeout,
			ExtraSpecs:             create.ExtraSpecs,
			GitHubRunnerGroup:      create.GitHubRunnerGroup,
		}
		for _, label := range create.Labels {
			set.Tags = append(set.Tags, params.Tag{ID: label, Name: label})
		}
		if set.RunnerBootstrapTimeout == 0 {
			set.RunnerBootstrapTimeout = appdefaults.DefaultRunnerBootstrapTimeout
		}
		if set.GitHubRunnerGroup == "" {
			set.GitHubRunnerGroup = "Default"
		}
		for _, t := range st.Templates {
			if create.TemplateID == nil && t.Owner == params.SystemUser && t.OSType == create.OSType && t.ForgeType == entity.Endpoint.EndpointType {
				create.TemplateID = &t.ID
			}
		}
		if create.TemplateID == nil {
			return nil, badRequest("no template ID supplied and no default template can be found")
		}
		template := st.templateByID(formatID(*create.TemplateID))
		if template == nil {
			return nil, badRequest("failed to find suitable template")
		}
		if template.OSType != create.OSType {
			return nil, badRequest("selected template OS type (%s) and OS type (%s) do not match", template.OSType, create.OSType)
		}
		set.TemplateID, set.TemplateName = template.ID, template.Name
		switch kind {
		case garm.Repository:
			set.RepoID = entity.ID
		case garm.Organization:
			set.OrgID = entity.ID
		case garm.Enterprise:
			set.EnterpriseID = entity.ID
		}
		st.ScaleSets = append(st.ScaleSets, set)
		return *set, nil
	}
}

func (st *State) scaleSetByID(r *http.Request) *params.ScaleSet {
	for _, set := range st.ScaleSets {
		if formatID(set.ID) == r.PathValue("id") {
			return set
		}
	}
	return nil
}

func (st *State) getScaleSet(r *http.Request) (any, error) {
	set := st.scaleSetByID(r)
	if set == nil {
		return nil, notFound()
	}
	return *set, nil
}

// updateScaleSet applies an update the way garm does, see updatePool.
func (st *State) updateScaleSet(r *http.Request) (any, error) {
	set := st.scaleSetByID(r)
	if set == nil {
		return nil, notFound()
	}
	update, err := decode[params.UpdateScaleSetParams](r)
	if err != nil {
		return nil, err
	}
	if update.RunnerBootstrapTimeout != nil && *update.RunnerBootstrapTimeout == 0 {
		return nil, badRequest("runner_bootstrap_timeout cannot be 0")
	}

	setString(&set.Prefix, update.Prefix)
	setString(&set.Name, update.Name)
	setString(&set.Image, update.Image)
	setString(&set.Flavor, update.Flavor)
	setString((*string)(&set.OSType), string(update.OSType))
	setString((*string)(&set.OSArch), string(update.OSArch))
	setPointer(&set.Enabled, update.Enabled)
	setPointer(&set.MaxRunners, update.MaxRunners)
	setPointer(&set.MinIdleRunners, update.MinIdleRunners)
	setPointer(&set.RunnerBootstrapTimeout, update.RunnerBootstrapTimeout)
	setPointer(&set.EnableShell, update.EnableShell)
	setPointer(&set.GitHubRunnerGroup, update.GitHubRunnerGroup)
	if update.ExtraSpecs != nil {
		set.ExtraSpecs = update.ExtraSpecs
	}
	if update.TemplateID != nil {
		template := st.templateByID(formatID(*update.TemplateID))
		if template == nil {
			return nil, badRequest("failed to find template")
		}
		set.TemplateID, set.TemplateName = template.ID, template.Name
	}
	if set.MinIdleRunners > set.MaxRunners {
		return nil, badRequest("min_idle_runners cannot be larger than max_runners")
	}
	return *set, nil
}

func (st *State) scaleSetInstances(id uint) []params.Instance {
	instances := []params.Instance{}
	for _, i := range st.Instances {
		if i.ScaleSetID == id && id != 0 {
			instances = append(instances, *i)
		}
	}
	return instances
}

func (st *State) deleteScaleSet(r *http.Request) (any, error) {
	set := st.scaleSetByID(r)
	if set == nil {
		return nil, nil
	}
	if len(st.scaleSetInstances(set.ID)) > 0 {
		return nil, badRequest("scale set has runners")
	}
	if set.Enabled {
		return nil, badRequest("scale set is enabled; disable it first")
	}
	if st.KeepDeletedScaleSets {
		return nil, nil
	}
	st.ScaleSets = slices.DeleteFunc(st.ScaleSets, func(s *params.ScaleSet) bool { return s == set })
	return nil, nil
}

func (st *State) listScaleSetInstances(r *http.Request) (any, error) {
	set := st.scaleSetByID(r)
	if set == nil {
		return []params.Instance{}, nil
	}
	return st.scaleSetInstances(set.ID), nil
}
