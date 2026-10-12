package api

import (
	"context"
	"errors"
	"net/http"

	pb "github.com/Valentin-Kaiser/backup/pkg/proto/api/v1"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/audit"
	"github.com/Valentin-Kaiser/backup/worker/portal/pkg/login"
	"github.com/valentin-kaiser/go-core/logging/log"
	"github.com/valentin-kaiser/go-core/web/xrpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// CookieName is the name of the session cookie.
const CookieName = "portal_session"

// Auth implements the sign-in. The portal has one password and no users, whoever signed in can do everything.
type Auth struct {
	pb.UnimplementedAuthServer
	store *login.Store
	audit *audit.Log
	// secure marks the cookie Secure, set when the portal itself serves TLS.
	secure bool
}

var _ pb.AuthServer = (*Auth)(nil)

// NewAuth creates the service. audit may be nil.
func NewAuth(store *login.Store, secure bool, a *audit.Log) *Auth {
	return &Auth{store: store, secure: secure, audit: a}
}

// Access tells whether a request carries a valid session.
func (a *Auth) Access(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	return err == nil && a.store.Valid(c.Value)
}

// require fails with 401 unless the caller has a session.
func (a *Auth) require(ctx context.Context) error {
	if r, ok := xrpc.GetRequest(ctx); ok && a.Access(r) {
		return nil
	}
	return fail(ctx, http.StatusUnauthorized, "sign in required")
}

func (a *Auth) record(ctx context.Context, action, result, detail string) {
	if a.audit != nil {
		a.audit.Add(audit.Entry{IP: requestIP(ctx), Action: action, Result: result, Detail: detail})
	}
}

func (a *Auth) setCookie(ctx context.Context, value string, maxAge int) {
	if w, ok := xrpc.GetResponseWriter(ctx); ok {
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: value, Path: "/", HttpOnly: true, Secure: a.secure,
			SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
		})
	}
}

// Login checks the password and starts a session.
func (a *Auth) Login(ctx context.Context, in *pb.LoginRequest) (*pb.Session, error) {
	ip := requestIP(ctx)
	token, err := a.store.Login(in.GetPassword(), ip)
	switch {
	case errors.Is(err, login.ErrInvalidLogin):
		log.Warn().Field("ip", ip).Msg("failed sign-in")
		a.record(ctx, audit.LoginFailed, audit.Denied, "wrong password")
		return nil, fail(ctx, http.StatusUnauthorized, err.Error())
	case errors.Is(err, login.ErrLocked):
		log.Warn().Field("ip", ip).Msg("sign-in refused, the address is locked")
		a.record(ctx, audit.LoginFailed, audit.Denied, "address locked after too many wrong passwords")
		return nil, fail(ctx, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, login.ErrNoPassword):
		return nil, fail(ctx, http.StatusServiceUnavailable, err.Error())
	case err != nil:
		log.Error().Err(err).Msg("sign-in failed")
		return nil, fail(ctx, http.StatusInternalServerError, "sign-in failed")
	}
	a.setCookie(ctx, token, int(login.SessionLifetime.Seconds()))
	log.Info().Field("ip", ip).Msg("signed in")
	a.record(ctx, audit.LoginOK, audit.OK, "")
	return &pb.Session{SignedIn: true}, nil
}

// Logout ends the session of the caller.
func (a *Auth) Logout(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	if r, ok := xrpc.GetRequest(ctx); ok {
		if c, err := r.Cookie(CookieName); err == nil {
			if a.store.Valid(c.Value) {
				a.record(ctx, audit.Logout, audit.OK, "")
			}
			a.store.Logout(c.Value)
		}
	}
	a.setCookie(ctx, "", -1)
	return &emptypb.Empty{}, nil
}

// GetSession answers for a valid session and fails with 401 otherwise.
func (a *Auth) GetSession(ctx context.Context, _ *emptypb.Empty) (*pb.Session, error) {
	if err := a.require(ctx); err != nil {
		return nil, err
	}
	return &pb.Session{SignedIn: true}, nil
}
