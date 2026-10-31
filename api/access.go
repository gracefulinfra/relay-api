package api

import (
	"context"

	relayapi "github.com/gracefulinfra/relay-contracts/gen/go/relayapi"
)

// Staff users and grants (P1-02). Delegating methods, rather than embedding the service, because the
// service and Unimplemented would define the same method names at the same depth.

// GetMe implements getMe.
func (s *Server) GetMe(ctx context.Context, r relayapi.GetMeRequestObject) (relayapi.GetMeResponseObject, error) {
	return s.Access.GetMe(ctx, r)
}

// ListStaffUsers implements listStaffUsers.
func (s *Server) ListStaffUsers(ctx context.Context, r relayapi.ListStaffUsersRequestObject) (relayapi.ListStaffUsersResponseObject, error) {
	return s.Access.ListStaffUsers(ctx, r)
}

// ListShowGrants implements listShowGrants.
func (s *Server) ListShowGrants(ctx context.Context, r relayapi.ListShowGrantsRequestObject) (relayapi.ListShowGrantsResponseObject, error) {
	return s.Access.ListShowGrants(ctx, r)
}

// PutShowGrant implements putShowGrant.
func (s *Server) PutShowGrant(ctx context.Context, r relayapi.PutShowGrantRequestObject) (relayapi.PutShowGrantResponseObject, error) {
	return s.Access.PutShowGrant(ctx, r)
}

// DeleteShowGrant implements deleteShowGrant.
func (s *Server) DeleteShowGrant(ctx context.Context, r relayapi.DeleteShowGrantRequestObject) (relayapi.DeleteShowGrantResponseObject, error) {
	return s.Access.DeleteShowGrant(ctx, r)
}
