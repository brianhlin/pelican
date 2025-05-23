/***************************************************************
 *
 * Copyright (C) 2024, Pelican Project, Morgridge Institute for Research
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you
 * may not use this file except in compliance with the License.  You may
 * obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 ***************************************************************/

package web_ui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/pelicanplatform/pelican/config"
	pelican_oauth2 "github.com/pelicanplatform/pelican/oauth2"
	"github.com/pelicanplatform/pelican/param"
	"github.com/pelicanplatform/pelican/server_structs"
)

const (
	oauthLoginPath    = "/api/v1.0/auth/oauth/login"
	oauthCallbackPath = "/api/v1.0/auth/oauth/callback"
)

var (
	oauthConfig      *oauth2.Config
	oauthUserInfoUrl = "" // Value will be set at ConfigOAuthClientAPIs
)

// Parse the OAuth2 callback state into a key-val map. Error if keys are duplicated
// state is the url-decoded value of the query parameter "state" in the the OAuth2 callback request
func ParseOAuthState(state string) (metadata map[string]string, err error) {
	metadata = map[string]string{}
	if state == "" {
		return metadata, nil
	}

	stateBytes, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to base64 decode the OAuth state: %v", state)
	}
	state = string(stateBytes)

	keyvals := strings.Split(state, "&")
	metadata = map[string]string{}
	for _, kvStr := range keyvals {
		kvpair := strings.SplitN(kvStr, "=", 2)
		if len(kvpair) < 2 {
			continue
		}
		key := kvpair[0]
		val, err := url.QueryUnescape(kvpair[1])
		if err != nil {
			return nil, errors.Wrapf(err, "failed to unescape the value for the key %s:%s", key, val)
		}
		if _, ok := metadata[key]; ok {
			return nil, fmt.Errorf("duplicated keys %s:%s", key, state)
		}
		metadata[key] = val
	}
	return
}

// Generate the state for the authentication request in OAuth2 code flow.
// The metadata are formatted similar to url query parameters:
//
// key1=val1&key2=val2
//
// where values are url-encoded. We then base64 encode the resulting string
// in order to ensure that over-zealous providers do not treat the final URL
// as a double-encoding attack or somesuch.
func GenerateOAuthState(metadata map[string]string) string {
	metaStr := ""
	for key, val := range metadata {
		metaStr += key + "=" + url.QueryEscape(val) + "&"
	}
	metaStr = strings.TrimSuffix(metaStr, "&")
	return base64.RawURLEncoding.EncodeToString([]byte(metaStr))
}

// Generate a 16B random string and set as the value of ctx session key "oauthstate"
// return a string for OAuth2 "state" query parameter including the random string and other
// metadata
func GenerateCSRFCookie(ctx *gin.Context, metadata map[string]string) (string, error) {
	session := sessions.Default(ctx)

	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	pkceStr := base64.URLEncoding.EncodeToString(b)
	session.Set("oauthstate", pkceStr)
	err = session.Save()
	if err != nil {
		return "", err
	}
	if _, ok := metadata["pkce"]; ok {
		return "", errors.New("key \"pkce\" is not allowed")
	}
	metadata["pkce"] = pkceStr
	metaStr := GenerateOAuthState(metadata)
	return metaStr, nil
}

// Handler to redirect user to the login page of OAuth2 provider
// You can pass an optional next_url as query param if you want the user
// to be redirected back to where they were before hitting the login when
// the user is successfully authenticated against the OAuth2 provider
func handleOAuthLogin(ctx *gin.Context) {
	req := server_structs.OAuthLoginRequest{}
	if ctx.ShouldBindQuery(&req) != nil {
		ctx.JSON(http.StatusBadRequest,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    "Failed to bind next url",
			})
	}

	// CSRF token is required, embed next URL to the state
	csrfState, err := GenerateCSRFCookie(ctx, map[string]string{"nextUrl": req.NextUrl})

	if err != nil {
		log.Errorf("Failed to generate CSRF token: %v", err)
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    "Failed to generate CSRF token",
			})
		return
	}

	redirectUrl := oauthConfig.AuthCodeURL(csrfState)
	ctx.Redirect(http.StatusTemporaryRedirect, redirectUrl)
}

// Given a user name, return the list of groups they belong to
func generateGroupInfo(user string) (groups []string, err error) {
	// Currently, only file-based lookup is supported
	if param.Issuer_GroupSource.GetString() != "file" {
		return
	}
	groupFile := param.Issuer_GroupFile.GetString()
	if groupFile == "" {
		return
	}
	groupBytes, err := os.ReadFile(groupFile)
	if err != nil {
		err = errors.Wrap(err, "failed to read Issuer.GroupFile for group information")
		return
	}
	var groupTable map[string][]string
	if err = json.Unmarshal(groupBytes, &groupTable); err != nil {
		err = errors.Wrapf(err, "failed to parse Issuer.GroupFile (%s) as JSON", groupFile)
		return
	}
	groups = groupTable[user]
	return
}

// Given the maps for the UserInfo and ID token JSON objects, generate
// user/group information according to the current policy.
func generateUserGroupInfo(userInfo map[string]interface{}, idToken map[string]interface{}) (user string, groups []string, err error) {
	claimsSource := maps.Clone(userInfo)
	if param.Issuer_OIDCPreferClaimsFromIDToken.GetBool() {
		maps.Copy(claimsSource, idToken)
	}
	userClaim := param.Issuer_OIDCAuthenticationUserClaim.GetString()
	if userClaim == "" {
		userClaim = "sub"
	}
	userIdentifierIface, ok := claimsSource[userClaim]
	if !ok {
		log.Errorln("User info endpoint did not return a value for the user claim", userClaim)
		err = errors.New("identity provider did not return an identity for logged-in user")
		return
	}
	userIdentifier, ok := userIdentifierIface.(string)
	if !ok {
		log.Errorln("User info endpoint did not return a string for the user claim", userClaim)
		err = errors.New("identity provider did not return an identity for logged-in user")
		return
	}
	if param.Issuer_UserStripDomain.GetBool() {
		lastAt := strings.LastIndex(userIdentifier, "@")
		if lastAt >= 0 {
			userIdentifier = userIdentifier[:strings.LastIndex(userIdentifier, "@")]
		}
	}
	if userIdentifier == "" {
		log.Errorf("'%s' field of user info response from auth provider is empty. Can't determine user identity", userClaim)
		err = errors.New("identity provider returned an empty username")
		return
	}
	user = userIdentifier

	if param.Issuer_GroupSource.GetString() == "oidc" {
		groupClaim := param.Issuer_OIDCGroupClaim.GetString()
		groupList, ok := claimsSource[groupClaim]
		if ok {
			if groupsStr, ok := groupList.(string); ok {
				groupsInfo := strings.Split(groupsStr, ",")
				groups = make([]string, 0, len(groupsInfo))
				for _, groupRaw := range groupsInfo {
					group := strings.TrimSpace(groupRaw)
					if group != "" {
						groups = append(groups, group)
					}
				}
			} else if groupsTmp, ok := groupList.([]interface{}); ok {
				groups = make([]string, 0, len(groupsTmp))
				for _, groupObj := range groupsTmp {
					if groupStr, ok := groupObj.(string); ok {
						groups = append(groups, groupStr)
					}
				}
			}
		}
	} else {
		groups, err = generateGroupInfo(user)
	}
	return
}

// Handle the callback request when the user is successfully authenticated.
// Get the user's info and issue our token for accessing the web UI.
func handleOAuthCallback(ctx *gin.Context) {
	session := sessions.Default(ctx)
	c := context.Background()
	csrfFromSession := session.Get("oauthstate")
	if csrfFromSession == nil {
		ctx.JSON(http.StatusBadRequest,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    "Invalid OAuth callback: CSRF token from cookie is missing",
			})
		return
	}

	req := server_structs.OAuthCallbackRequest{}
	if ctx.ShouldBindQuery(&req) != nil {
		ctx.JSON(http.StatusBadRequest,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Invalid OAuth callback: fail to bind CSRF token from state query: ", ctx.Request.URL),
			})
		return
	}

	stateMap, err := ParseOAuthState(req.State)
	if err != nil {
		ctx.JSON(http.StatusBadRequest,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Invalid OAuth callback: failed to parse state metadata", ctx.Request.URL),
			})
		return
	}
	pkce, ok := stateMap["pkce"]
	if !ok {
		ctx.JSON(http.StatusBadRequest,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Invalid OAuth callback: pkce is missing from the callback state", ctx.Request.URL),
			})
		return
	}

	nextURL := stateMap["nextUrl"]

	if pkce != csrfFromSession {
		ctx.JSON(http.StatusBadRequest,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Invalid OAuth callback: CSRF token doesn't match: ", ctx.Request.URL),
			})
		return
	}

	// We need this token only to get the user's info.
	// We will later issue our own token for user access.
	token, err := oauthConfig.Exchange(c, req.Code)
	if err != nil {
		log.Errorf("Error in exchanging code for token:  %v", err)
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Error in exchanging code for token: ", ctx.Request.URL),
			})
		return
	}

	var idToken = make(map[string]interface{})
	if idTokenRaw := token.Extra("id_token"); idTokenRaw != nil {
		// The token's signature will show as "REDACTED" in the output.
		log.Debugf("Found an OIDC ID token: %v", idTokenRaw)

		// We were given this ID token by the authentication provider, not
		// some third party. If we don't trust the provider, we have greater
		// issues.
		skew, _ := time.ParseDuration("6s")
		idTokenJWT, err := jwt.ParseString(idTokenRaw.(string), jwt.WithVerify(false), jwt.WithAcceptableSkew(skew))
		if err != nil {
			log.Errorf("Error parsing OIDC ID token: %v", err)
			ctx.JSON(http.StatusInternalServerError,
				server_structs.SimpleApiResp{
					Status: server_structs.RespFailed,
					Msg:    fmt.Sprint("Error parsing OIDC ID token: ", ctx.Request.URL),
				})
			return
		}

		idToken, err = idTokenJWT.AsMap(ctx)
		if err != nil {
			log.Errorf("Error converting OIDC ID token to a map: %v", err)
			ctx.JSON(http.StatusInternalServerError,
				server_structs.SimpleApiResp{
					Status: server_structs.RespFailed,
					Msg:    fmt.Sprint("Error converting OIDC ID token to a map: ", ctx.Request.URL),
				})
			return
		}
	} else {
		log.Debugf("Did not find an OIDC ID token")
	}

	client := oauthConfig.Client(c, token)
	client.Transport = config.GetTransport()

	userInfoReq, err := http.NewRequest(http.MethodGet, oauthUserInfoUrl, nil)
	if err != nil {
		log.Errorf("Error creating a new request for user info from auth provider at %s. %v", oauthUserInfoUrl, err)
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Error requesting user info from auth provider: ", err),
			})
		return
	}
	userInfoReq.Header.Add("Authorization", token.TokenType+" "+token.AccessToken)

	resp, err := client.Do(userInfoReq)
	if err != nil {
		log.Errorf("Error requesting user info from auth provider at %s. %v", oauthUserInfoUrl, err)
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Error requesting user info from auth provider: ", err),
			})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("Error getting user info response from auth provider at %s. %v", oauthUserInfoUrl, err)
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Failed to get OAuth2 user info response: ", err),
			})
		return
	}

	if resp.StatusCode != 200 {
		log.Errorf("Error requesting user info from auth provider at %s with status code %d and body %s", oauthUserInfoUrl, resp.StatusCode, string(body))
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Error requesting user info from auth provider with status code ", resp.StatusCode),
			})
		return
	}
	log.Debugf("User info from auth provider: %v", string(body))

	var userInfo map[string]interface{}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		log.Errorf("Error parsing user info from auth provider at %s. %v", oauthUserInfoUrl, err)
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    fmt.Sprint("Error parsing user info from auth provider: ", err),
			})
		return
	}

	user, groups, err := generateUserGroupInfo(userInfo, idToken)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError,
			server_structs.SimpleApiResp{
				Status: server_structs.RespFailed,
				Msg:    err.Error(),
			})
		return
	}

	redirectLocation := "/"
	if nextURL != "" {
		redirectLocation = nextURL
	}

	// Issue our own JWT for web UI access
	setLoginCookie(ctx, user, groups)

	// Redirect user to where they were or root path
	ctx.Redirect(http.StatusTemporaryRedirect, redirectLocation)
}

// Configure OAuth2 client and register related authentication endpoints for Web UI
func ConfigOAuthClientAPIs(engine *gin.Engine) error {
	oauthCommonConfig, provider, err := pelican_oauth2.ServerOIDCClient()
	if err != nil {
		return errors.Wrap(err, "failed to load server OIDC client config")
	}
	// Pelican registry relies on OAuth2 device flow for CLI-based registration
	// and Globus does not support such flow. So users should not use Globus for the registry
	if config.IsServerEnabled(server_structs.RegistryType) && provider == config.Globus {
		return errors.New("you are using Globus as the OIDC auth server. However, Pelican registry server does not support Globus. Please use CILogon as the auth server instead.")
	}

	oauthUserInfoUrl = oauthCommonConfig.Endpoint.UserInfoURL

	ocfg, err := pelican_oauth2.ParsePelicanOAuth(oauthCommonConfig, oauthCallbackPath)
	if err != nil {
		return err
	}
	oauthConfig = &ocfg

	seHandler, err := GetSessionHandler()
	if err != nil {
		return err
	}

	oauthGroup := engine.Group("/api/v1.0/auth/oauth", seHandler, ServerHeaderMiddleware)
	{
		oauthGroup.GET("/login", handleOAuthLogin)
		oauthGroup.GET("/callback", handleOAuthCallback)
	}
	return nil
}
