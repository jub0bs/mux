// Copyright 2012 The Gorilla Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mux

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
)

var (
	// ErrMethodMismatch is returned when the method in the request does not match
	// the method defined against the route.
	ErrMethodMismatch = errors.New("method is not allowed")
	// ErrNotFound is returned when no route match is found.
	ErrNotFound = errors.New("no matching route was found")
	// RegexpCompileFunc aliases regexp.Compile and enables overriding it.
	// Do not run this function from `init()` in importable packages.
	// Changing this value is not safe for concurrent use.
	RegexpCompileFunc = regexp.Compile
	// ErrMetadataKeyNotFound is returned when the specified metadata key is not present in the map
	ErrMetadataKeyNotFound = errors.New("key not found in metadata")
)

// NewRouter returns a new router instance.
func NewRouter() *Router {
	return &Router{namedRoutes: make(map[string]*Route)}
}

// Router registers routes to be matched and dispatches a handler.
//
// It implements the http.Handler interface, so it can be registered to serve
// requests:
//
//	var router = mux.NewRouter()
//
//	func main() {
//	    http.Handle("/", router)
//	}
//
// Or, for Google App Engine, register it in a init() function:
//
//	func init() {
//	    http.Handle("/", router)
//	}
//
// This will send all incoming requests to the router.
type Router struct {
	// Configurable Handler to be used when no route matches.
	// This can be used to render your own 404 Not Found errors.
	NotFoundHandler http.Handler

	// Configurable Handler to be used when the request method does not match the route.
	// This can be used to render your own 405 Method Not Allowed errors.
	MethodNotAllowedHandler http.Handler

	// Routes to be matched, in order.
	routes []*Route

	// Routes by name for URL building.
	namedRoutes map[string]*Route

	// If true, do not clear the request context after handling the request.
	//
	// Deprecated: No effect, since the context is stored on the request itself.
	KeepContext bool

	// Slice of middlewares to be called after a match is found
	middlewares []middleware

	// configuration shared with `Route`
	routeConf
}

// common route configuration shared between `Router` and `Route`
type routeConf struct {
	// If true, "/path/foo%2Fbar/to" will match the path "/path/{var}/to"
	useEncodedPath bool

	// If true, when the path pattern is "/path/", accessing "/path" will
	// redirect to the former and vice versa.
	strictSlash bool

	// If true, when the path pattern is "/path//to", accessing "/path//to"
	// will not redirect
	skipClean bool

	// If true, the http.Request context will not contain the Route.
	omitRouteFromContext bool

	// if true, the the http.Request context will not contain the router
	omitRouterFromContext bool

	// If true, only ampersands (not semicolons) act as separators for
	// query-parameter pairs.
	strictQueryParamSep bool

	// Manager for the variables from host and path.
	regexp routeRegexpGroup

	// List of matchers.
	matchers []matcher

	// The scheme used when building URLs.
	buildScheme string

	buildVarsFunc BuildVarsFunc
}

// returns an effective deep copy of `routeConf`
func copyRouteConf(r routeConf) routeConf {
	c := r

	if r.regexp.path != nil {
		c.regexp.path = copyRouteRegexp(r.regexp.path)
	}

	if r.regexp.host != nil {
		c.regexp.host = copyRouteRegexp(r.regexp.host)
	}

	c.regexp.queries = make([]*routeRegexp, 0, len(r.regexp.queries))
	for _, q := range r.regexp.queries {
		c.regexp.queries = append(c.regexp.queries, copyRouteRegexp(q))
	}

	c.matchers = make([]matcher, len(r.matchers))
	copy(c.matchers, r.matchers)

	return c
}

func copyRouteRegexp(r *routeRegexp) *routeRegexp {
	c := *r
	return &c
}

// Match attempts to match the given request against the router's registered routes.
//
// If the request matches a route of this router or one of its subrouters the Route,
// Handler, and Vars fields of the the match argument are filled and this function
// returns true.
//
// If the request does not match any of this router's or its subrouters' routes
// then this function returns false. If available, a reason for the match failure
// will be filled in the match argument's MatchErr field. If the match failure type
// (eg: not found) has a registered handler, the handler is assigned to the Handler
// field of the match argument.
func (r *Router) Match(req *http.Request, match *RouteMatch) bool {
	for _, route := range r.routes {
		if route.Match(req, match) {
			// Build middleware chain if no error was found
			if match.MatchErr == nil {
				for i := len(r.middlewares) - 1; i >= 0; i-- {
					match.Handler = r.middlewares[i].Middleware(match.Handler)
				}
			}
			return true
		}
	}

	if match.MatchErr == ErrMethodMismatch {
		if r.MethodNotAllowedHandler != nil {
			match.Handler = r.MethodNotAllowedHandler
			return true
		}

		return false
	}

	// Closest match for a router (includes sub-routers)
	if r.NotFoundHandler != nil {
		match.Handler = r.NotFoundHandler
		match.MatchErr = ErrNotFound
		return true
	}

	match.MatchErr = ErrNotFound
	return false
}

// ServeHTTP dispatches the handler registered in the matched route.
//
// When there is a match, the route variables can be retrieved calling
// mux.Vars(request).
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.skipClean {
		path := req.URL.Path
		if r.useEncodedPath {
			path = req.URL.EscapedPath()
		}
		// Clean path to canonical form and redirect.
		if p := cleanPath(path); p != path {
			w.Header().Set("Location", replaceURLPath(req.URL, p))
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
	}
	var match RouteMatch
	var handler http.Handler
	if r.Match(req, &match) {
		handler = match.Handler
		if handler != nil {
			// Populate context for custom handlers
			if r.omitRouteFromContext {
				// Only populate the match vars (if any) into the context.
				req = requestWithVars(req, match.Vars)
			} else {
				req = requestWithRouteAndVars(req, match.Route, match.Vars)
			}

			if !r.omitRouterFromContext {
				req = requestWithRouter(req, r)
			}
		}
	}

	if handler == nil && match.MatchErr == ErrMethodMismatch {
		handler = methodNotAllowedHandler()
	}

	if handler == nil {
		handler = http.NotFoundHandler()
	}

	handler.ServeHTTP(w, req)
}

// Get returns a route registered with the given name.
func (r *Router) Get(name string) *Route {
	return r.namedRoutes[name]
}

// GetRoute returns a route registered with the given name. This method
// was renamed to Get() and remains here for backwards compatibility.
func (r *Router) GetRoute(name string) *Route {
	return r.namedRoutes[name]
}

// StrictSlash defines the trailing slash behavior for new routes. The initial
// value is false.
//
// When true, if the route path is "/path/", accessing "/path" will perform a redirect
// to the former and vice versa. In other words, your application will always
// see the path as specified in the route.
//
// When false, if the route path is "/path", accessing "/path/" will not match
// this route and vice versa.
//
// The redirect is a HTTP 301 (Moved Permanently). Note that when this is set for
// routes with a non-idempotent method (e.g. POST, PUT), the subsequent redirected
// request will be made as a GET by most clients. Use middleware or client settings
// to modify this behaviour as needed.
//
// Special case: when a route sets a path prefix using the PathPrefix() method,
// strict slash is ignored for that route because the redirect behavior can't
// be determined from a prefix alone. However, any subrouters created from that
// route inherit the original StrictSlash setting.
func (r *Router) StrictSlash(value bool) *Router {
	r.strictSlash = value
	return r
}

// SkipClean defines the path cleaning behaviour for new routes. The initial
// value is false. Users should be careful about which routes are not cleaned
//
// When true, if the route path is "/path//to", it will remain with the double
// slash. This is helpful if you have a route like: /fetch/http://xkcd.com/534/
//
// When false, the path will be cleaned, so /fetch/http://xkcd.com/534/ will
// become /fetch/http/xkcd.com/534
func (r *Router) SkipClean(value bool) *Router {
	r.skipClean = value
	return r
}

// OmitRouteFromContext defines the behavior of omitting the Route from the
//
//	http.Request context.
//
// CurrentRoute will yield nil with this option.
func (r *Router) OmitRouteFromContext(value bool) *Router {
	r.omitRouteFromContext = value
	return r
}

// OmitRouterFromContext defines the behavior of omitting the Router from the
// http.Request context.
//
// RouterFromRequest will yield nil with this option.
func (r *Router) OmitRouterFromContext(value bool) *Router {
	r.omitRouterFromContext = value
	return r
}

// StrictQueryParamSep defines which characters act as separators for
// query-parameter pairs. The initial value is false, but beware: a future
// version of this library will adopt true for the initial value.
//
// When true, only ampersands act as separators for query parameters.
// This behavior complies with [the URL Living Standard].
//
// When false, both ampersands and semicolons act as separators for
// query-parameter pairs. This contravenes the URL Living Standard and causes
// interoperability problems that can lead to [security vulnerabilities].
//
// [security vulnerabilities]: https://github.com/gorilla/mux/issues/781
// [the URL Living Standard]: https://url.spec.whatwg.org/#urlencoded-parsing
func (r *Router) StrictQueryParamSep(value bool) *Router {
	r.strictQueryParamSep = value
	return r
}

// UseEncodedPath tells the router to match the encoded original path
// to the routes.
// For eg. "/path/foo%2Fbar/to" will match the path "/path/{var}/to".
//
// If not called, the router will match the unencoded path to the routes.
// For eg. "/path/foo%2Fbar/to" will match the path "/path/foo/bar/to"
func (r *Router) UseEncodedPath() *Router {
	r.useEncodedPath = true
	return r
}

// ----------------------------------------------------------------------------
// Route factories
// ----------------------------------------------------------------------------

// NewRoute registers an empty route.
func (r *Router) NewRoute() *Route {
	// initialize a route with a copy of the parent router's configuration
	route := &Route{routeConf: copyRouteConf(r.routeConf), namedRoutes: r.namedRoutes}
	r.routes = append(r.routes, route)
	return route
}

// Name registers a new route with a name.
// See Route.Name().
func (r *Router) Name(name string) *Route {
	return r.NewRoute().Name(name)
}

// Handle registers a new route with a matcher for the URL path.
// See Route.Path() and Route.Handler().
func (r *Router) Handle(path string, handler http.Handler) *Route {
	return r.NewRoute().Path(path).Handler(handler)
}

// HandleFunc registers a new route with a matcher for the URL path.
// See Route.Path() and Route.HandlerFunc().
func (r *Router) HandleFunc(path string, f func(http.ResponseWriter,
	*http.Request)) *Route {
	return r.NewRoute().Path(path).HandlerFunc(f)
}

// Headers registers a new route with a matcher for request header values.
// See Route.Headers().
func (r *Router) Headers(pairs ...string) *Route {
	return r.NewRoute().Headers(pairs...)
}

// Host registers a new route with a matcher for the URL host.
// See Route.Host().
func (r *Router) Host(tpl string) *Route {
	return r.NewRoute().Host(tpl)
}

// MatcherFunc registers a new route with a custom matcher function.
// See Route.MatcherFunc().
func (r *Router) MatcherFunc(f MatcherFunc) *Route {
	return r.NewRoute().MatcherFunc(f)
}

// Methods registers a new route with a matcher for HTTP methods.
// See Route.Methods().
func (r *Router) Methods(methods ...string) *Route {
	return r.NewRoute().Methods(methods...)
}

// Path registers a new route with a matcher for the URL path.
// See Route.Path().
func (r *Router) Path(tpl string) *Route {
	return r.NewRoute().Path(tpl)
}

// PathPrefix registers a new route with a matcher for the URL path prefix.
// See Route.PathPrefix().
func (r *Router) PathPrefix(tpl string) *Route {
	return r.NewRoute().PathPrefix(tpl)
}

// Queries registers a new route with a matcher for URL query values.
// See Route.Queries().
func (r *Router) Queries(pairs ...string) *Route {
	return r.NewRoute().Queries(pairs...)
}

// Schemes registers a new route with a matcher for URL schemes.
// See Route.Schemes().
func (r *Router) Schemes(schemes ...string) *Route {
	return r.NewRoute().Schemes(schemes...)
}

// BuildVarsFunc registers a new route with a custom function for modifying
// route variables before building a URL.
func (r *Router) BuildVarsFunc(f BuildVarsFunc) *Route {
	return r.NewRoute().BuildVarsFunc(f)
}

// Walk walks the router and all its sub-routers, calling walkFn for each route
// in the tree. The routes are walked in the order they were added. Sub-routers
// are explored depth-first.
func (r *Router) Walk(walkFn WalkFunc) error {
	return r.walk(walkFn, []*Route{})
}

// SkipRouter is used as a return value from WalkFuncs to indicate that the
// router that walk is about to descend down to should be skipped.
var SkipRouter = errors.New("skip this router")

// WalkFunc is the type of the function called for each route visited by Walk.
// At every invocation, it is given the current route, and the current router,
// and a list of ancestor routes that lead to the current route.
type WalkFunc func(route *Route, router *Router, ancestors []*Route) error

func (r *Router) walk(walkFn WalkFunc, ancestors []*Route) error {
	for _, t := range r.routes {
		err := walkFn(t, r, ancestors)
		if err == SkipRouter {
			continue
		}
		if err != nil {
			return err
		}
		for _, sr := range t.matchers {
			if h, ok := sr.(*Router); ok {
				ancestors = append(ancestors, t)
				err := h.walk(walkFn, ancestors)
				if err != nil {
					return err
				}
				ancestors = ancestors[:len(ancestors)-1]
			}
		}
		if h, ok := t.handler.(*Router); ok {
			ancestors = append(ancestors, t)
			err := h.walk(walkFn, ancestors)
			if err != nil {
				return err
			}
			ancestors = ancestors[:len(ancestors)-1]
		}
	}
	return nil
}

// ----------------------------------------------------------------------------
// Context
// ----------------------------------------------------------------------------

// RouteMatch stores information about a matched route.
type RouteMatch struct {
	Route   *Route
	Handler http.Handler
	Vars    map[string]string

	// MatchErr is set to appropriate matching error
	// It is set to ErrMethodMismatch if there is a mismatch in
	// the request method and route method
	MatchErr error
}

type contextKey int

const (
	varsKey contextKey = iota
	routeKey
	routerKey
)

// Vars returns the route variables for the current request, if any.
func Vars(r *http.Request) map[string]string {
	if rv := r.Context().Value(varsKey); rv != nil {
		return rv.(map[string]string)
	}
	return nil
}

// CurrentRoute returns the matched route for the current request, if any.
// This only works when called inside the handler of the matched route
// because the matched route is stored in the request context which is cleared
// after the handler returns.
func CurrentRoute(r *http.Request) *Route {
	if rv := r.Context().Value(routeKey); rv != nil {
		return rv.(*Route)
	}
	return nil
}

func CurrentRouter(r *http.Request) *Router {
	if rv := r.Context().Value(routerKey); rv != nil {
		return rv.(*Router)
	}
	return nil
}

// requestWithVars adds the matched vars to the request ctx.
// It shortcuts the operation when the vars are empty.
func requestWithVars(r *http.Request, vars map[string]string) *http.Request {
	if len(vars) == 0 {
		return r
	}
	ctx := context.WithValue(r.Context(), varsKey, vars)
	return r.WithContext(ctx)
}

// requestWithRouteAndVars adds the matched route and vars to the request ctx.
// It saves extra allocations in cloning the request once and skipping the
//
//	population of empty vars, which in turn mux.Vars can handle gracefully.
func requestWithRouteAndVars(r *http.Request, route *Route, vars map[string]string) *http.Request {
	ctx := context.WithValue(r.Context(), routeKey, route)
	if len(vars) > 0 {
		ctx = context.WithValue(ctx, varsKey, vars)
	}
	return r.WithContext(ctx)
}

func requestWithRouter(r *http.Request, router *Router) *http.Request {
	ctx := context.WithValue(r.Context(), routerKey, router)
	return r.WithContext(ctx)
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// cleanPath returns the canonical path for p, eliminating . and .. elements.
// Borrowed from the net/http package.
func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if p[0] != '/' {
		p = "/" + p
	}
	np := path.Clean(p)
	// path.Clean removes trailing slash except for root;
	// put the trailing slash back if necessary.
	if p[len(p)-1] == '/' && np != "/" {
		np += "/"
	}

	return np
}

// replaceURLPath prints an url.URL with a different path.
func replaceURLPath(u *url.URL, p string) string {
	// Operate on a copy of the request url.
	u2 := *u
	u2.Path = p
	return u2.String()
}

// uniqueVars returns an error if two slices contain duplicated strings.
func uniqueVars(s1, s2 []string) error {
	for _, v1 := range s1 {
		for _, v2 := range s2 {
			if v1 == v2 {
				return fmt.Errorf("mux: duplicated route variable %q", v2)
			}
		}
	}
	return nil
}

// checkPairs returns the count of strings passed in, and an error if
// the count is not an even number.
func checkPairs(pairs ...string) (int, error) {
	length := len(pairs)
	if length%2 != 0 {
		return length, fmt.Errorf(
			"mux: number of parameters must be multiple of 2, got %v", pairs)
	}
	return length, nil
}

// mapFromPairsToString converts variadic string parameters to a
// string to string map.
func mapFromPairsToString(pairs ...string) (map[string]string, error) {
	length, err := checkPairs(pairs...)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, length/2)
	for i := 0; i < length; i += 2 {
		m[pairs[i]] = pairs[i+1]
	}
	return m, nil
}

// mapFromPairsToRegex converts variadic string parameters to a
// string to regex map.
func mapFromPairsToRegex(pairs ...string) (map[string]*regexp.Regexp, error) {
	length, err := checkPairs(pairs...)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*regexp.Regexp, length/2)
	for i := 0; i < length; i += 2 {
		regex, err := RegexpCompileFunc(pairs[i+1])
		if err != nil {
			return nil, err
		}
		m[pairs[i]] = regex
	}
	return m, nil
}

// matchInArray returns true if the given string value is in the array.
func matchInArray(arr []string, value string) bool {
	for _, v := range arr {
		if v == value {
			return true
		}
	}
	return false
}

// matchMapWithString returns true if the given key/value pairs exist in a given map.
func matchMapWithString(toCheck map[string]string, toMatch map[string][]string, canonicalKey bool) bool {
	for k, v := range toCheck {
		// Check if key exists.
		if canonicalKey {
			k = http.CanonicalHeaderKey(k)
		}
		if values := toMatch[k]; values == nil {
			return false
		} else if v != "" {
			// If value was defined as an empty string we only check that the
			// key exists. Otherwise we also check for equality.
			valueExists := false
			for _, value := range values {
				if v == value {
					valueExists = true
					break
				}
			}
			if !valueExists {
				return false
			}
		}
	}
	return true
}

// matchMapWithRegex returns true if the given key/value pairs exist in a given map compiled against
// the given regex
func matchMapWithRegex(toCheck map[string]*regexp.Regexp, toMatch map[string][]string, canonicalKey bool) bool {
	for k, v := range toCheck {
		// Check if key exists.
		if canonicalKey {
			k = http.CanonicalHeaderKey(k)
		}
		if values := toMatch[k]; values == nil {
			return false
		} else if v != nil {
			// If value was defined as an empty string we only check that the
			// key exists. Otherwise we also check for equality.
			valueExists := false
			for _, value := range values {
				if v.MatchString(value) {
					valueExists = true
					break
				}
			}
			if !valueExists {
				return false
			}
		}
	}
	return true
}

// methodNotAllowed replies to the request with an HTTP status code 405.
func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusMethodNotAllowed)
}

// methodNotAllowedHandler returns a simple request handler
// that replies to each request with a status code 405.
func methodNotAllowedHandler() http.Handler { return http.HandlerFunc(methodNotAllowed) }
