package runner

import (
	"context"
	"fmt"
	"strings"
)

// Combined is for human-readable capability/dry-run text, never JSON discovery.
func Combined(ctx context.Context, r Runner, name string, args ...string) (string, error) {
	parent, _ := ctx.Value(outputKey{}).(func(OutputEvent))
	var b strings.Builder
	overflow := false
	observed := WithOutput(ctx, func(e OutputEvent) {
		if parent != nil {
			parent(e)
		}
		if e.Kind == "output" {
			if b.Len()+len(e.Text) > 2<<20 {
				overflow = true
			} else {
				b.WriteString(e.Text)
			}
		}
	})
	out, e := r.Run(observed, name, args...)
	if overflow {
		return b.String(), fmt.Errorf("combined command output exceeded 2 MiB")
	}
	if b.Len() > 0 {
		return b.String(), e
	}
	return out, e
}
