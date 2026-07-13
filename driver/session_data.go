package driver

import "context"

// SessionData reads and interprets one runtime-owned persisted session.
// TokenStatistics derives its result from Raw.
type SessionData struct {
	ctx   context.Context
	load  func(context.Context) (RawSessionData, error)
	parse func(context.Context, RawSessionData) (TokenUsage, error)
}

// NewSessionData builds an operation-scoped session data accessor. The loader
// owns runtime-specific storage discovery; the parser owns runtime-specific
// token semantics. The accessor must not outlive ctx.
func NewSessionData(
	ctx context.Context,
	load func(context.Context) (RawSessionData, error),
	parse func(context.Context, RawSessionData) (TokenUsage, error),
) *SessionData {
	return &SessionData{ctx: ctx, load: load, parse: parse}
}

func (d *SessionData) Raw() (RawSessionData, error) {
	if err := d.ctx.Err(); err != nil {
		return RawSessionData{}, err
	}
	if d.load == nil {
		return RawSessionData{}, NewUnsupportedError("raw session data are not supported")
	}
	return d.load(d.ctx)
}

func (d *SessionData) TokenStatistics() (TokenUsage, error) {
	if d.parse == nil {
		return TokenUsage{}, NewUnsupportedError("session token statistics are not supported")
	}
	raw, err := d.Raw()
	if err != nil {
		return TokenUsage{}, err
	}
	if err := d.ctx.Err(); err != nil {
		return TokenUsage{}, err
	}
	return d.parse(d.ctx, raw)
}
