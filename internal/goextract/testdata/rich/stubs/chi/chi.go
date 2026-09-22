// Package chi is a stub of the router's registration surface.
package chi

import "net/http"

type Router interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
	Method(method, pattern string, h http.Handler)
	Handle(pattern string, h http.Handler)
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

type Mux struct{ routes []string }

func NewRouter() *Mux { return &Mux{} }

func (m *Mux) Get(pattern string, h http.HandlerFunc)  { m.routes = append(m.routes, "GET "+pattern) }
func (m *Mux) Post(pattern string, h http.HandlerFunc) { m.routes = append(m.routes, "POST "+pattern) }
func (m *Mux) Method(method, pattern string, h http.Handler) {
	m.routes = append(m.routes, method+" "+pattern)
}
func (m *Mux) Handle(pattern string, h http.Handler)            { m.routes = append(m.routes, pattern) }
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {}
