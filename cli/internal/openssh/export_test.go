package openssh

import "context"

// Start and ValidateAlias are the production-unexported session helpers,
// re-exported in tests so the external test package can still drive them
// without going through Connect.
func (a *Adapter) Start(ctx context.Context, alias string) (*Session, error) {
	return a.start(ctx, alias)
}

func (a *Adapter) ValidateAlias(ctx context.Context, alias string) error {
	return a.validateAlias(ctx, alias)
}
