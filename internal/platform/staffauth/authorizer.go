package staffauth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gracefulinfra/relay-api/internal/platform/access/accessdb"
	"github.com/gracefulinfra/relay-api/internal/platform/auth"
	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"
)

// ShowResolver maps the ID of a resource named by a path parameter (an episode, a season, ...) to the
// show that owns it. found=false means the resource does not exist, which authorizes nothing.
// Owning modules register resolvers when they land (P1-03 and later); until then a route whose
// parameter has no resolver is denied for everyone except network admins.
type ShowResolver func(ctx context.Context, id uuid.UUID) (showID uuid.UUID, found bool, err error)

// Authorizer implements auth.Authorizer: deny by default, network admins pass everything, everyone else
// needs a grant that satisfies the operation's tier on the operation's show. Grants are read from the
// database on every request, so a revocation or a disable takes effect immediately.
type Authorizer struct {
	Policy    Policy
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	Resolvers map[string]ShowResolver // keyed by path parameter name, for example "episodeId"
}

// Authorize implements auth.Authorizer.
func (z *Authorizer) Authorize(ctx context.Context, actor auth.Actor, r *http.Request) bool {
	tier, ok := z.Policy[r.Pattern]
	if !ok || tier == tierUnset {
		return false
	}
	if tier == Public {
		return true
	}
	if !actor.Enabled {
		return false
	}
	if actor.NetworkAdmin || tier == Staff {
		return true
	}
	if tier == Admin {
		return false
	}

	uid, err := uuid.Parse(actor.ID)
	if err != nil {
		return false
	}
	grants, err := accessdb.New(z.Pool).ListGrantsForUser(ctx, uid)
	if err != nil {
		z.Log.ErrorContext(ctx, "load grants", slog.Any("error", err))
		return false // fail closed
	}
	byShow := make(map[uuid.UUID]Tier, len(grants))
	for _, g := range grants {
		byShow[g.ShowID] = roleRank(relayapi.ShowRole(g.Role))
	}

	params := pathParams(r.Pattern)
	if len(params) == 0 {
		// Collection operations (a list, a search) are not about one show: the caller needs the tier on
		// at least one show, and the handler filters its results to the shows they hold.
		for _, have := range byShow {
			if have >= tier {
				return true
			}
		}
		return false
	}
	show, ok := z.showFor(ctx, r, params)
	return ok && byShow[show] >= tier
}

// showFor resolves the show an operation is about from its first path parameter.
func (z *Authorizer) showFor(ctx context.Context, r *http.Request, params []string) (uuid.UUID, bool) {
	name := params[0]
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, false
	}
	if name == "showId" {
		return id, true
	}
	resolve, ok := z.Resolvers[name]
	if !ok {
		z.Log.WarnContext(ctx, "no show resolver for route; denying", slog.String("route", r.Pattern), slog.String("param", name))
		return uuid.Nil, false
	}
	show, found, err := resolve(ctx, id)
	if err != nil {
		z.Log.ErrorContext(ctx, "resolve show", slog.String("param", name), slog.Any("error", err))
		return uuid.Nil, false
	}
	return show, found
}
