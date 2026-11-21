package runner

import "context"

type ExecutionOptions struct {
	Directory   string
	Environment map[string]string
	Guard       func(context.Context, string, []string) error
}
type executionKey struct{}

func Options(ctx context.Context) ExecutionOptions {
	o, _ := ctx.Value(executionKey{}).(ExecutionOptions)
	return o
}
func WithDirectory(ctx context.Context, path string) context.Context {
	o := Options(ctx)
	o.Directory = path
	return context.WithValue(ctx, executionKey{}, o)
}
func WithEnvironment(ctx context.Context, values map[string]string) context.Context {
	o := Options(ctx)
	merged := map[string]string{}
	for k, v := range o.Environment {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}
	o.Environment = merged
	return context.WithValue(ctx, executionKey{}, o)
}
func WithCommandGuard(ctx context.Context, guard func(context.Context, string, []string) error) context.Context {
	o := Options(ctx)
	o.Guard = guard
	return context.WithValue(ctx, executionKey{}, o)
}
