package app

import "context"

func (b *BackendAPI) OnShutdown(ctx context.Context) error {
	return b.Shutdown(ctx)
}
