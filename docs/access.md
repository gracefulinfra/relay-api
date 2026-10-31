# Staff authentication and show-scoped access (P1-02)

Keycloak (realm `relay-staff`) says **who** a staff member is; relay-api decides **what** they may do
(decision A9). Code: `internal/platform/staffauth` (token validation, policy, authorizer),
`internal/platform/access` (staff users, grants, bootstrap), `internal/platform/audit`.

## Request flow

`Authenticate` (every `/v0` request except `/v0/public/*`) → router → `Authorize` (per operation) →
idempotency → handler.

1. **Token.** `Authorization: Bearer <access token>`, exactly one header. Accepted when: RS256 signature
   verifies against the realm JWKS (cached, refetched on an unknown `kid`, so key rotation needs no
   restart); `iss` equals `RELAY_OIDC_ISSUER`; `aud` contains `RELAY_OIDC_AUDIENCE` (`relay-api`);
   not expired; `nbf` not more than 30 s in the future. Any failure is a bare `401`.
2. **Identity.** Staff users are keyed by `(issuer, subject)`. The first valid token provisions a row
   with `enabled=false` and an audit event `staff_user.provision`. Only the display name is stored: no
   email, no token.
3. **Authorization.** The policy is the contract's `x-relay-authz`, read from the embedded OpenAPI by
   `PolicyFromContract`:

   | Tier | Passes when |
   | --- | --- |
   | `public` | always (`/v0/public/*`; ops and health are on the separate ops port) |
   | `staff` | the user is enabled |
   | `viewer+` / `producer+` / `editor+` | enabled, and holds that role or higher on the operation's show (`viewer` < `producer` < `show_editor`) |
   | `admin` | enabled and `networkAdmin` |

   A network admin passes every tier. A disabled user gets `403` on every non-public operation. A route
   with no policy entry is denied, even to an admin. Grants are read on every request, so a revoke or a
   disable applies immediately.
4. **Which show.** `{showId}` routes use that ID. A route keyed by another resource (`{episodeId}`, ...)
   needs a `ShowResolver` registered by the owning module (P1-03 onward); until one exists the route is
   denied for non-admins and a warning is logged. A route with no path parameter (a list) needs the tier
   on at least one show, and its handler must filter results to the caller's shows.

## Granting access

New sign-ins appear in `GET /staff-users` (admin) as disabled users. `PUT /shows/{showId}/grants/{staffUserId}`
creates or changes a grant **and enables the user**. `DELETE` revokes. Both write an audit event in the
request transaction with the before/after role.

## First network admin

Nothing can grant the first admin through the API. The person signs in once (so a row exists), then:

```bash
kubectl -n relay exec deploy/relay-api -- /relay-api bootstrap-admin --subject <keycloak-user-id>
```

`--issuer` defaults to `RELAY_OIDC_ISSUER`. The command makes that user an enabled network admin, writes
`staff_user.bootstrap_admin` (no actor), and **refuses to run again** once any network admin exists
(`ErrAdminExists`; the check and the grant share one transaction under an advisory lock). Find the subject
in the Keycloak admin console (Users → ID).

## Audit log

`platform.audit_events`: actor, action, resource, `before`/`after` JSON, request ID, client IP (the TCP
peer; behind the Gateway that is the proxy, see follow-ups). Diffs hold only the changed fields of a
non-secret resource (a role, a boolean), never tokens or personal data.
