module example.com/rich

go 1.21

require (
	github.com/go-chi/chi/v5 v5.0.0
	github.com/spf13/cobra v1.0.0
	google.golang.org/grpc v1.0.0
)

replace github.com/go-chi/chi/v5 => ./stubs/chi

replace github.com/spf13/cobra => ./stubs/cobra

replace google.golang.org/grpc => ./stubs/grpc
