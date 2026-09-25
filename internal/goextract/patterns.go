package goextract

import (
	"strings"

	"github.com/Ullauri/shapediff/internal/config"
)

// Pattern tables of spec section 7.3. A callee key is
// "<import path>.<TypeName>.<Method>" for methods (pointer receivers
// stripped, type parameters dropped) or "<import path>.<Func>" for
// functions. Interface methods use the interface's type name, so a call
// through chi.Router matches as well as one on *chi.Mux.

// httpPattern describes a route registration call.
type httpPattern struct {
	method   string // fixed HTTP method, "" when the route literal or an arg carries it
	methArg  int    // arg index of the method string when method == "" and methArg >= 0
	routeArg int
	handler  int
}

var httpVerbs = []string{"Get", "Post", "Put", "Patch", "Delete", "Head", "Options", "Connect", "Trace"}

func buildHTTPPatterns() map[string]httpPattern {
	m := map[string]httpPattern{
		"net/http.HandleFunc":                      {routeArg: 0, handler: 1, methArg: -1},
		"net/http.Handle":                          {routeArg: 0, handler: 1, methArg: -1},
		"net/http.ServeMux.HandleFunc":             {routeArg: 0, handler: 1, methArg: -1},
		"net/http.ServeMux.Handle":                 {routeArg: 0, handler: 1, methArg: -1},
		"github.com/gorilla/mux.Router.HandleFunc": {routeArg: 0, handler: 1, methArg: -1},
		"github.com/gorilla/mux.Router.Handle":     {routeArg: 0, handler: 1, methArg: -1},
	}
	for _, chi := range []string{"github.com/go-chi/chi/v5.Mux", "github.com/go-chi/chi/v5.Router", "github.com/go-chi/chi.Mux", "github.com/go-chi/chi.Router"} {
		for _, v := range httpVerbs {
			m[chi+"."+v] = httpPattern{method: strings.ToUpper(v), routeArg: 0, handler: 1, methArg: -1}
		}
		m[chi+".Method"] = httpPattern{methArg: 0, routeArg: 1, handler: 2}
		m[chi+".MethodFunc"] = httpPattern{methArg: 0, routeArg: 1, handler: 2}
		m[chi+".Handle"] = httpPattern{routeArg: 0, handler: 1, methArg: -1}
		m[chi+".HandleFunc"] = httpPattern{routeArg: 0, handler: 1, methArg: -1}
	}
	for _, t := range []string{"github.com/gin-gonic/gin.Engine", "github.com/gin-gonic/gin.RouterGroup", "github.com/gin-gonic/gin.IRoutes", "github.com/gin-gonic/gin.IRouter",
		"github.com/labstack/echo/v4.Echo", "github.com/labstack/echo/v4.Group", "github.com/labstack/echo.Echo"} {
		for _, v := range httpVerbs {
			m[t+"."+strings.ToUpper(v)] = httpPattern{method: strings.ToUpper(v), routeArg: 0, handler: 1, methArg: -1}
		}
		m[t+".Handle"] = httpPattern{methArg: 0, routeArg: 1, handler: 2}
		m[t+".Add"] = httpPattern{methArg: 0, routeArg: 1, handler: 2}
	}
	return m
}

// workerPattern is a (name literal, handler) registration.
type workerPattern struct{ nameArg, handler int }

func buildWorkerPatterns(cfg *config.Config) map[string]workerPattern {
	m := map[string]workerPattern{
		"github.com/hibiken/asynq.ServeMux.HandleFunc":            {0, 1},
		"github.com/hibiken/asynq.ServeMux.Handle":                {0, 1},
		"github.com/RichardKnop/machinery/v1.Server.RegisterTask": {0, 1},
		"github.com/RichardKnop/machinery/v2.Server.RegisterTask": {0, 1},
	}
	for _, p := range cfg.Entrypoints["worker"] {
		if p.Call != "" {
			m[p.Call] = workerPattern{0, 1}
		}
	}
	return m
}

// cronPattern is a (schedule, handler) registration; the handler names the
// entry point because a schedule is not an identity.
type cronPattern struct{ handler int }

func buildCronPatterns(cfg *config.Config) map[string]cronPattern {
	m := map[string]cronPattern{
		"github.com/robfig/cron/v3.Cron.AddFunc": {1},
		"github.com/robfig/cron/v3.Cron.AddJob":  {1},
		"github.com/robfig/cron.Cron.AddFunc":    {1},
		"github.com/robfig/cron.Cron.AddJob":     {1},
	}
	for _, p := range cfg.Entrypoints["cron"] {
		if p.Call != "" {
			m[p.Call] = cronPattern{1}
		}
	}
	return m
}

// sinkPattern classifies a callee by package prefix.
type sinkPattern struct {
	kind string
	name string // sink name; "" means last path element of the package
}

// sinkPackages maps an import path prefix to a sink kind (section 7.3's
// table). Longest prefix wins.
func buildSinkPackages(cfg *config.Config) map[string]sinkPattern {
	m := map[string]sinkPattern{
		"database/sql":                   {"db", "sql"},
		"github.com/jackc/pgx":           {"db", "pgx"},
		"gorm.io/gorm":                   {"db", "gorm"},
		"entgo.io/ent":                   {"db", "ent"},
		"github.com/jmoiron/sqlx":        {"db", "sqlx"},
		"github.com/hibiken/asynq":       {"queue", "asynq"},
		"github.com/riverqueue/river":    {"queue", "river"},
		"github.com/segmentio/kafka-go":  {"queue", "kafka"},
		"github.com/IBM/sarama":          {"queue", "kafka"},
		"github.com/Shopify/sarama":      {"queue", "kafka"},
		"github.com/rabbitmq/amqp091-go": {"queue", "amqp"},
		"github.com/streadway/amqp":      {"queue", "amqp"},
		"cloud.google.com/go/pubsub":     {"queue", "pubsub"},
		"github.com/redis/go-redis":      {"cache", "redis"},
		"github.com/go-redis/redis":      {"cache", "redis"},
		"github.com/gomodule/redigo":     {"cache", "redis"},
	}
	for kind, pats := range cfg.Sinks {
		for _, p := range pats {
			if p.Package != "" {
				m[p.Package] = sinkPattern{kind, ""}
			}
		}
	}
	return m
}

// httpClientCallees are the net/http outbound calls; the sink is
// sink:http_client:http.
var httpClientCallees = map[string]bool{
	"net/http.Client.Do": true, "net/http.Client.Get": true, "net/http.Client.Post": true,
	"net/http.Client.PostForm": true, "net/http.Client.Head": true,
	"net/http.Get": true, "net/http.Post": true, "net/http.PostForm": true, "net/http.Head": true,
}

// queue sinks are only the producer side. Only these methods on the queue
// packages count; everything else (server setup, options) is not a sink.
var queueProducerMethods = map[string]bool{
	"Enqueue": true, "EnqueueContext": true, "Insert": true, "InsertMany": true, "InsertTx": true,
	"WriteMessages": true, "SendMessage": true, "SendMessages": true, "Publish": true,
	"PublishWithContext": true, "PublishWithDeferredConfirm": true,
}

// setupCall reports whether a db/cache/queue method is connection or
// transaction plumbing rather than data access, and so not a sink.
func setupCall(method string) bool {
	switch method {
	case "Open", "OpenDB", "Connect", "ConnectConfig", "New", "NewClient", "NewPool", "NewWithConfig", "NewWriter", "NewReader", "NewProducer", "NewConsumer", "NewSyncProducer", "NewAsyncProducer",
		"Close", "Ping", "PingContext", "Begin", "BeginTx", "Commit", "Rollback", "Prepare", "PrepareContext",
		"SetMaxOpenConns", "SetMaxIdleConns", "SetConnMaxLifetime", "SetConnMaxIdleTime", "Stats", "Driver", "Conn", "WithContext", "Session", "Dial", "DialContext", "Options", "Channel", "Err":
		return true
	}
	return false
}

// readVerb reports whether a db/cache method name reads rather than writes.
func readVerb(method string) bool {
	for _, p := range []string{"Query", "Get", "Select", "Find", "First", "Scan", "Count", "Exists", "List", "Row", "Rows", "Take", "Last", "Pluck", "HGet", "MGet", "Keys", "Exists", "TTL", "Load"} {
		if strings.HasPrefix(method, p) {
			return true
		}
	}
	return false
}

// sqlVerbReads reports whether a sqlc query text reads.
func sqlVerbReads(sql string) bool {
	s := strings.TrimSpace(strings.ToUpper(sql))
	// skip leading sqlc comment lines
	for strings.HasPrefix(s, "--") {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			return true
		}
		s = strings.TrimSpace(s[i+1:])
	}
	return strings.HasPrefix(s, "SELECT") || strings.HasPrefix(s, "WITH")
}
