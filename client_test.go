package gocloak

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
)

type configAdmin struct {
	UserName string `json:"username"`
	Password string `json:"password"`
	Realm    string `json:"realm"`
}

type configGoCloak struct {
	UserName     string `json:"username"`
	Password     string `json:"password"`
	Realm        string `json:"realm"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type Config struct {
	HostName string        `json:"hostname"`
	Proxy    string        `json:"proxy,omitempty"`
	Admin    configAdmin   `json:"admin"`
	GoCloak  configGoCloak `json:"gocloak"`
}

var (
	config     *Config
	configOnce sync.Once
	setupOnce  sync.Once
	testUserID string
)

const (
	gocloakClientID = "60be66a5-e007-464c-9b74-0e3c2e69e478"
)

func FailIfErr(t *testing.T, err error, msg string, args ...interface{}) {
	if IsObjectAlreadyExists(err) {
		t.Logf("ObjectAlreadyExists error: %s", err.Error())
		return
	}

	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		if len(msg) == 0 {
			msg = "unexpected error"
		} else {
			if len(args) > 0 {
				msg = fmt.Sprintf(msg, args...)
			}
		}
		t.Fatalf("%s:%d: %s: %s", filepath.Base(file), line, msg, err.Error())
	}
}

func FailIf(t *testing.T, cond bool, msg string, args ...interface{}) {
	if cond {
		if len(args) > 0 {
			t.Fatalf(msg, args...)
		} else {
			t.Fatal(msg)
		}
	}
}

func GetConfig(t *testing.T) *Config {
	configOnce.Do(func() {
		rand.Seed(time.Now().UTC().UnixNano())
		configFileName, ok := os.LookupEnv("GOCLOAK_TEST_CONFIG")
		if !ok {
			configFileName = filepath.Join("testdata", "config.json")
		}
		configFile, err := os.Open(configFileName)
		FailIfErr(t, err, "cannot open config.json")
		defer func() {
			err := configFile.Close()
			FailIfErr(t, err, "cannot close config file")
		}()
		data, err := ioutil.ReadAll(configFile)
		FailIfErr(t, err, "cannot read config.json")
		config = &Config{}
		err = json.Unmarshal(data, config)
		FailIfErr(t, err, "cannot parse config.json")
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		if len(config.Proxy) != 0 {
			proxy, err := url.Parse(config.Proxy)
			FailIfErr(t, err, "incorrect proxy url: "+config.Proxy)
			http.DefaultTransport.(*http.Transport).Proxy = http.ProxyURL(proxy)
		}
		if config.GoCloak.UserName == "" {
			config.GoCloak.UserName = "test_user"
		}
	})
	return config
}

func GetClientToken(t *testing.T, client GoCloak) *JWT {
	cfg := GetConfig(t)
	token, err := client.LoginClient(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm)
	FailIfErr(t, err, "Login failed")
	return token
}

func GetUserToken(t *testing.T, client GoCloak) *JWT {
	SetUpTestUser(t, client)
	cfg := GetConfig(t)
	token, err := client.Login(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm,
		cfg.GoCloak.UserName,
		cfg.GoCloak.Password)
	FailIfErr(t, err, "Login failed")
	return token
}

func GetAdminToken(t *testing.T, client GoCloak) *JWT {
	cfg := GetConfig(t)
	token, err := client.LoginAdmin(
		cfg.Admin.UserName,
		cfg.Admin.Password,
		cfg.Admin.Realm)
	FailIfErr(t, err, "Login failed")
	return token
}

func GetRandomName(name string) string {
	s1 := rand.NewSource(time.Now().UnixNano())
	r1 := rand.New(s1)
	randomNumber := r1.Intn(100000)
	return name + strconv.Itoa(randomNumber)
}

func GetRandomNameP(name string) *string {
	r := GetRandomName(name)
	return &r
}

func GetClientByClientID(t *testing.T, client GoCloak, clientID string) *Client {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)
	clients, err := client.GetClients(
		token.AccessToken,
		cfg.GoCloak.Realm,
		GetClientsParams{
			ClientID: &clientID,
		})
	assert.NoError(t, err, "GetClients failed")
	for _, fetchedClient := range clients {
		if fetchedClient.ClientID == nil {
			continue
		}
		if *(fetchedClient.ClientID) == clientID {
			return fetchedClient
		}
	}
	t.Fatal("Client not found")
	return nil
}

func CreateGroup(t *testing.T, client GoCloak) (func(), string) {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)
	group := Group{
		Name: GetRandomNameP("GroupName"),
		Attributes: map[string][]string{
			"foo": {"bar", "alice", "bob", "roflcopter"},
			"bar": {"baz"},
		},
	}
	groupID, err := client.CreateGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		group)
	assert.NoError(t, err, "CreateGroup failed")
	t.Logf("Created Group ID: %s ", groupID)

	tearDown := func() {
		err := client.DeleteGroup(
			token.AccessToken,
			cfg.GoCloak.Realm,
			groupID)
		FailIfErr(t, err, "DeleteGroup failed")
	}
	return tearDown, groupID
}

func SetUpTestUser(t *testing.T, client GoCloak) {
	setupOnce.Do(func() {
		cfg := GetConfig(t)
		token := GetAdminToken(t, client)

		user := User{
			Username:      StringP(cfg.GoCloak.UserName),
			Email:         StringP(cfg.GoCloak.UserName + "@localhost"),
			EmailVerified: BoolP(true),
			Enabled:       BoolP(true),
		}

		createdUserID, err := client.CreateUser(
			token.AccessToken,
			cfg.GoCloak.Realm,
			user)
		FailIfErr(t, err, "CreateUser failed")
		if IsObjectAlreadyExists(err) {
			users, err := client.GetUsers(
				token.AccessToken,
				cfg.GoCloak.Realm,
				GetUsersParams{
					Username: StringP(cfg.GoCloak.UserName),
				})
			FailIfErr(t, err, "GetUsers failed")
			for _, user := range users {
				if PString(user.Username) == cfg.GoCloak.UserName {
					testUserID = PString(user.ID)
					break
				}
			}
		} else {
			FailIfErr(t, err, "CreateUser failed")
			testUserID = createdUserID
		}

		err = client.SetPassword(
			token.AccessToken,
			testUserID,
			cfg.GoCloak.Realm,
			cfg.GoCloak.Password,
			false)
		FailIfErr(t, err, "SetPassword failed")
	})
}

type RestyLogWriter struct {
	io.Writer
	t *testing.T
}

func (w *RestyLogWriter) Errorf(format string, v ...interface{}) {
	w.write("[ERROR] "+format, v...)
}

func (w *RestyLogWriter) Warnf(format string, v ...interface{}) {
	w.write("[WARN] "+format, v...)
}

func (w *RestyLogWriter) Debugf(format string, v ...interface{}) {
	w.write("[DEBUG] "+format, v...)
}

func (w *RestyLogWriter) write(format string, v ...interface{}) {
	w.t.Logf(format, v...)
}

func NewClientWithDebug(t *testing.T) GoCloak {
	cfg := GetConfig(t)
	client := NewClient(cfg.HostName)
	restyClient := client.RestyClient()
	restyClient.SetDebug(true)
	restyClient.SetLogger(&RestyLogWriter{
		t: t,
	})

	cond := func(resp *resty.Response, err error) bool {
		if resp != nil && resp.IsError() {
			e := resp.Error().(*HTTPErrorResponse)
			if e != nil {
				var msg string
				if len(e.ErrorMessage) > 0 {
					msg = e.ErrorMessage
				} else if len(e.Error) > 0 {
					msg = e.Error
				}
				return strings.HasPrefix(msg, "Cached clientScope not found")
			}
		}
		return false
	}
	restyClient.AddRetryCondition(cond)
	restyClient.SetRetryCount(10)

	return client
}

// FailRequest fails requests and returns an error
//   err - returned error or nil to return the default error
//   failN - number of requests to be failed
//   skipN = number of requests to be executed and not failed by this function
func FailRequest(client GoCloak, err error, failN, skipN int) GoCloak {
	client.RestyClient().OnBeforeRequest(
		func(c *resty.Client, r *resty.Request) error {
			if skipN > 0 {
				skipN--
				return nil
			}
			if failN == 0 {
				return nil
			}
			failN--
			if err == nil {
				err = fmt.Errorf("an error for request: %+v", r)
			}
			return err
		},
	)
	return client
}

func ClearRealmCache(t *testing.T, client GoCloak, realm ...string) {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)
	if len(realm) == 0 {
		realm = append(realm, cfg.Admin.Realm, cfg.GoCloak.Realm)
	}
	for _, r := range realm {
		err := client.ClearRealmCache(token.AccessToken, r)
		assert.NoError(t, err, "ClearRealmCache failed for a realm: %s", r)
	}
}

// -----
// Tests
// -----

func TestGocloak_RestyClient(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	restyClient := client.RestyClient()
	assert.NotEqual(t, restyClient, resty.New())
}

func TestGocloak_SetRestyClient(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	newRestyClient := resty.New()
	client.SetRestyClient(newRestyClient)
	restyClient := client.RestyClient()
	assert.Equal(t, newRestyClient, restyClient)
}

func TestGocloak_checkForError(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	FailRequest(client, nil, 1, 0)
	_, err := client.Login("", "", "", "", "")
	assert.Error(t, err, "All requests must fail with NewClientWithError")
	t.Logf("Error: %s", err.Error())
}

// ---------
// API tests
// ---------

func TestGocloak_GetServerInfo(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	serverInfo, err := client.GetServerInfo(
		token.AccessToken,
	)
	assert.NoError(t, err, "Failed to fetch server info")
	t.Logf("Server Info: %+v", serverInfo)

	FailRequest(client, nil, 1, 0)
	_, err = client.GetServerInfo(
		token.AccessToken,
	)
	assert.Error(t, err)
}

func TestGocloak_GetUserInfo(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetClientToken(t, client)
	userInfo, err := client.GetUserInfo(
		token.AccessToken,
		cfg.GoCloak.Realm)
	assert.NoError(t, err, "Failed to fetch userinfo")
	t.Log(userInfo)
	FailRequest(client, nil, 1, 0)
	_, err = client.GetUserInfo(
		token.AccessToken,
		cfg.GoCloak.Realm)
	assert.Error(t, err, "")
}

func TestGocloak_RequestPermission(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	token, err := client.RequestPermission(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm,
		cfg.GoCloak.UserName,
		cfg.GoCloak.Password,
		"Permission foo # 3")
	FailIfErr(t, err, "login failed")

	rptResult, err := client.RetrospectToken(
		token.AccessToken,
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm)
	t.Log(rptResult)
	FailIfErr(t, err, "inspection failed")
	FailIf(t, !PBool(rptResult.Active), "Inactive Token oO")
}

func TestGocloak_GetCerts(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	certs, err := client.GetCerts(cfg.GoCloak.Realm)
	FailIfErr(t, err, "get certs")
	t.Log(certs)
}

func TestGocloak_LoginClient_UnknownRealm(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	_, err := client.LoginClient(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		"ThisRealmDoesNotExist")
	assert.Error(t, err, "Login shouldn't be successful")
	assert.EqualError(t, err, "404 Not Found: Realm does not exist")
}

func TestGocloak_GetIssuer(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	issuer, err := client.GetIssuer(cfg.GoCloak.Realm)
	t.Log(issuer)
	FailIfErr(t, err, "get issuer")
}

func TestGocloak_RetrospectToken_InactiveToken(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)

	rptResult, err := client.RetrospectToken(
		"foobar",
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm)
	t.Log(rptResult)
	FailIfErr(t, err, "inspection failed")
	FailIf(t, PBool(rptResult.Active), "That should never happen. Token is active")

}

func TestGocloak_RetrospectToken(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetClientToken(t, client)

	rptResult, err := client.RetrospectToken(
		token.AccessToken,
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm)
	t.Log(rptResult)
	FailIfErr(t, err, "Inspection failed")
	FailIf(t, !PBool(rptResult.Active), "Inactive Token oO")
}

func TestGocloak_DecodeAccessToken(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetClientToken(t, client)

	resultToken, claims, err := client.DecodeAccessToken(
		token.AccessToken,
		cfg.GoCloak.Realm,
	)
	assert.NoError(t, err)
	t.Log(resultToken)
	t.Log(claims)
}

func TestGocloak_DecodeAccessTokenCustomClaims(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetClientToken(t, client)

	claims := jwt.MapClaims{}
	resultToken, err := client.DecodeAccessTokenCustomClaims(
		token.AccessToken,
		cfg.GoCloak.Realm,
		claims,
	)
	assert.NoError(t, err)
	t.Log(resultToken)
	t.Log(claims)
}

func TestGocloak_RefreshToken(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetClientToken(t, client)

	token, err := client.RefreshToken(
		token.RefreshToken,
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm)
	t.Log(token)
	FailIfErr(t, err, "RefreshToken failed")
}

func TestGocloak_UserAttributeContains(t *testing.T) {
	t.Parallel()

	attributes := map[string][]string{}
	attributes["foo"] = []string{"bar", "alice", "bob", "roflcopter"}
	attributes["bar"] = []string{"baz"}

	client := NewClientWithDebug(t)
	ok := client.UserAttributeContains(attributes, "foo", "alice")
	FailIf(t, !ok, "UserAttributeContains")
}

func TestGocloak_GetKeyStoreConfig(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	config, err := client.GetKeyStoreConfig(
		token.AccessToken,
		cfg.GoCloak.Realm)
	t.Log(config)
	FailIfErr(t, err, "GetKeyStoreConfig")
}

func TestGocloak_Login(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	_, err := client.Login(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm,
		cfg.GoCloak.UserName,
		cfg.GoCloak.Password)
	FailIfErr(t, err, "Login failed")
}

func TestGocloak_GetToken(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	newToken, err := client.GetToken(
		cfg.GoCloak.Realm,
		TokenOptions{
			ClientID:      &cfg.GoCloak.ClientID,
			ClientSecret:  &cfg.GoCloak.ClientSecret,
			Username:      &cfg.GoCloak.UserName,
			Password:      &cfg.GoCloak.Password,
			GrantType:     StringP("password"),
			ResponseTypes: []string{"token", "id_token"},
			Scopes:        []string{"openid", "offline_access"},
		},
	)
	assert.NoError(t, err, "Login failed")
	t.Logf("New token: %+v", *newToken)
	assert.Equal(t, newToken.RefreshExpiresIn, 0, "Got a refresh token instead of offline")
	assert.NotEmpty(t, newToken.IDToken, "Got an empty if token")
}

func TestGocloak_LoginClient(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	_, err := client.LoginClient(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm)
	FailIfErr(t, err, "LoginClient failed")
}

func TestGocloak_LoginAdmin(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	_, err := client.LoginAdmin(
		cfg.Admin.UserName,
		cfg.Admin.Password,
		cfg.Admin.Realm)
	FailIfErr(t, err, "LoginAdmin failed")
}

func TestGocloak_SetPassword(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()

	err := client.SetPassword(
		token.AccessToken,
		userID,
		cfg.GoCloak.Realm,
		"passwort1234!",
		false)
	FailIfErr(t, err, "Failed to set password")
}

func TestGocloak_CreateListGetUpdateDeleteGetChildGroup(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	// Create
	tearDown, groupID := CreateGroup(t, client)
	// Delete
	defer tearDown()

	// List
	createdGroup, err := client.GetGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID,
	)
	assert.NoError(t, err, "GetGroup failed")
	t.Logf("Created Group: %+v", createdGroup)
	assert.Equal(t, groupID, *(createdGroup.ID))

	err = client.UpdateGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		Group{},
	)
	assert.Error(t, err, "Should fail because of missing ID of the group")

	createdGroup.Name = GetRandomNameP("GroupName")
	err = client.UpdateGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*createdGroup,
	)
	assert.NoError(t, err, "UpdateGroup failed")

	updatedGroup, err := client.GetGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID,
	)
	assert.NoError(t, err, "GetGroup failed")
	assert.Equal(t, *(createdGroup.Name), *(updatedGroup.Name))

	childGroupID, err := client.CreateChildGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID,
		Group{
			Name: GetRandomNameP("GroupName"),
		},
	)
	assert.NoError(t, err, "CreateChildGroup failed")

	_, err = client.GetGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		childGroupID,
	)
	assert.NoError(t, err, "GetGroup failed")
}

func CreateClientRole(t *testing.T, client GoCloak) (func(), string) {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)

	roleName := GetRandomName("Role")
	t.Logf("Creating Client Role: %s", roleName)
	clientRoleID, err := client.CreateClientRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		Role{
			Name: &roleName,
		})
	t.Logf("Created Client Role ID: %s", clientRoleID)
	assert.Equal(t, roleName, clientRoleID)

	assert.NoError(t, err, "CreateClientRole failed")
	tearDown := func() {
		err := client.DeleteClientRole(
			token.AccessToken,
			cfg.GoCloak.Realm,
			gocloakClientID,
			roleName)
		assert.NoError(t, err, "DeleteClientRole failed")
	}
	return tearDown, roleName
}

func TestGocloak_CreateClientRole(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	tearDown, _ := CreateClientRole(t, client)
	tearDown()
}

func TestGocloak_GetClientRole(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	tearDown, roleName := CreateClientRole(t, client)
	defer tearDown()
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)
	role, err := client.GetClientRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		roleName,
	)
	assert.NoError(t, err, "GetClientRoleI failed")
	assert.NotNil(t, role)
	token = GetAdminToken(t, client)
	role, err = client.GetClientRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		"Fake-Role-Name",
	)
	assert.Error(t, err)
	assert.Nil(t, role)
}

func CreateClientScope(t *testing.T, client GoCloak, scope *ClientScope) (func(), string) {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)

	if scope == nil {
		scope = &ClientScope{
			ID:   GetRandomNameP("client-scope-id-"),
			Name: GetRandomNameP("client-scope-name-"),
		}
	}

	t.Logf("Creating Client Scope: %+v", scope)
	clientScopeID, err := client.CreateClientScope(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*scope,
	)
	if !NilOrEmpty(scope.ID) {
		assert.Equal(t, clientScopeID, *(scope.ID))
	}
	assert.NoError(t, err, "CreateClientScope failed")
	tearDown := func() {
		err := client.DeleteClientScope(
			token.AccessToken,
			cfg.GoCloak.Realm,
			clientScopeID,
		)
		assert.NoError(t, err, "DeleteClientScope failed")
	}
	return tearDown, clientScopeID
}

func TestGocloak_CreateClientScope_DeleteClientScope(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	defer ClearRealmCache(t, client)
	tearDown, _ := CreateClientScope(t, client, nil)
	tearDown()
}

func TestGocloak_ListAddRemoveDefaultClientScopes(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	defer ClearRealmCache(t, client)

	scope := ClientScope{
		ID:       GetRandomNameP("client-scope-id-"),
		Name:     GetRandomNameP("client-scope-name-"),
		Protocol: StringP("openid-connect"),
		ClientScopeAttributes: &ClientScopeAttributes{
			IncludeInTokenScope: StringP("true"),
		},
	}

	tearDown, scopeID := CreateClientScope(t, client, &scope)
	defer tearDown()

	scopesBeforeAdding, err := client.GetClientsDefaultScopes(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
	)
	assert.NoError(t, err, "GetClientsDefaultScopes failed")

	err = client.AddDefaultScopeToClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		scopeID,
	)
	assert.NoError(t, err, "AddDefaultScopeToClient failed")

	scopesAfterAdding, err := client.GetClientsDefaultScopes(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
	)
	assert.NoError(t, err, "GetClientsDefaultScopes failed")

	assert.NotEqual(t, len(scopesBeforeAdding), len(scopesAfterAdding), "scope should have been added")

	err = client.RemoveDefaultScopeFromClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		scopeID,
	)
	assert.NoError(t, err, "RemoveDefaultScopeFromClient failed")

	scopesAfterRemoving, err := client.GetClientsDefaultScopes(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
	)
	assert.NoError(t, err, "GetClientsDefaultScopes failed")

	assert.Equal(t, len(scopesAfterRemoving), len(scopesBeforeAdding), "scope should have been removed")
}

func TestGocloak_ListAddRemoveOptionalClientScopes(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	defer ClearRealmCache(t, client)

	scope := ClientScope{
		ID:       GetRandomNameP("client-scope-id-"),
		Name:     GetRandomNameP("client-scope-name-"),
		Protocol: StringP("openid-connect"),
		ClientScopeAttributes: &ClientScopeAttributes{
			IncludeInTokenScope: StringP("true"),
		},
	}
	tearDown, scopeID := CreateClientScope(t, client, &scope)
	defer tearDown()

	scopesBeforeAdding, err := client.GetClientsOptionalScopes(token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID)
	assert.NoError(t, err, "GetClientsOptionalScopes failed")

	err = client.AddOptionalScopeToClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		scopeID)
	assert.NoError(t, err, "AddOptionalScopeToClient failed")

	scopesAfterAdding, err := client.GetClientsOptionalScopes(token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID)
	assert.NoError(t, err, "GetClientsOptionalScopes failed")

	assert.NotEqual(t, len(scopesAfterAdding), len(scopesBeforeAdding), "scope should have been added")

	err = client.RemoveOptionalScopeFromClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		scopeID)
	assert.NoError(t, err, "RemoveOptionalScopeFromClient failed")

	scopesAfterRemoving, err := client.GetClientsOptionalScopes(token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID)
	assert.NoError(t, err, "GetClientsOptionalScopes failed")

	assert.Equal(t, len(scopesBeforeAdding), len(scopesAfterRemoving), "scope should have been removed")
}

func TestGocloak_GetDefaultOptionalClientScopes(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	scopes, err := client.GetDefaultOptionalClientScopes(
		token.AccessToken,
		cfg.GoCloak.Realm)

	assert.NoError(t, err, "GetDefaultOptionalClientScopes failed")

	assert.NotEqual(t, 0, len(scopes), "there should be default optional client scopes")
}

func TestGocloak_GetDefaultDefaultClientScopes(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	scopes, err := client.GetDefaultDefaultClientScopes(
		token.AccessToken,
		cfg.GoCloak.Realm)

	assert.NoError(t, err, "GetDefaultDefaultClientScopes failed")

	assert.NotEqual(t, 0, len(scopes), "there should be default default client scopes")
}

func TestGocloak_GetClientScope(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	tearDown, scopeID := CreateClientScope(t, client, nil)
	defer tearDown()

	// Getting exact client scope
	createdClientScope, err := client.GetClientScope(
		token.AccessToken,
		cfg.GoCloak.Realm,
		scopeID,
	)
	assert.NoError(t, err, "GetClientScope failed")
	// Checking that GetClientScope returns same client scope
	assert.NotNil(t, createdClientScope.ID)
	assert.Equal(t, scopeID, *(createdClientScope.ID))
}

func TestGocloak_GetClientScopes(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	// Getting client scopes
	scopes, err := client.GetClientScopes(
		token.AccessToken,
		cfg.GoCloak.Realm)
	assert.NoError(t, err, "GetClientScopes failed")
	// Checking that GetClientScopes returns scopes
	assert.NotZero(t, len(scopes), "there should be client scopes")
}

func TestGocloak_GetClientScopeMappingClientRoles(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	tearDown, scopeID := CreateClientScope(t, client, nil)
	defer tearDown()

	// Getting client scope mapping roles
	roles, err := client.GetClientScopeMappingClientRoles(
		token.AccessToken,
		cfg.GoCloak.Realm,
		scopeID,
		gocloakClientID)
	assert.NoError(t, err, "GetClientScopes failed")
	// Checking that GetClientScopes returns scopes
	assert.NotZero(t, len(roles), "there should be client scopes")
}

func TestGocloak_AddClientScopeMappingClientRoles(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	tearDown, scopeID := CreateClientScope(t, client, nil)
	defer tearDown()

	roleName := GetRandomName("Role")
	t.Logf("Creating Client Role: %s", roleName)
	clientRoleID, err := client.CreateClientRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		Role{
			Name: &roleName,
		})
	t.Logf("Created Client Role ID: %s", clientRoleID)

	roles, err := client.GetClientRoles(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID)

	// Getting client scope mapping roles
	err = client.AddClientScopeMappingClientRoles(
		token.AccessToken,
		cfg.GoCloak.Realm,
		scopeID,
		gocloakClientID,
		roles)
	assert.NoError(t, err, "GetClientScopes failed")
	// Checking that GetClientScopes returns scopes
	assert.NotZero(t, len(roles), "there should be client scopes")

	roles, err = client.GetClientScopeMappingClientRoles(
		token.AccessToken,
		cfg.GoCloak.Realm,
		scopeID,
		gocloakClientID)
	assert.NoError(t, err, "GetClientScopes failed")
	// Checking that GetClientScopes returns scopes
	assert.NotZero(t, len(roles), "there should be client scopes")
}

func TestGocloak_CreateListGetUpdateDeleteClient(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	clientID := GetRandomNameP("ClientID")
	t.Logf("Client ID: %s", *clientID)

	// Creating a client
	createdClientID, err := client.CreateClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		Client{
			ClientID: clientID,
			Name:     GetRandomNameP("Name"),
			BaseURL:  StringP("http://example.com"),
		},
	)
	assert.NoError(t, err, "CreateClient failed")

	// Looking for a created client
	clients, err := client.GetClients(
		token.AccessToken,
		cfg.GoCloak.Realm,
		GetClientsParams{
			ClientID: clientID,
		},
	)
	assert.NoError(t, err, "CreateClients failed")
	assert.Len(t, clients, 1, "GetClients should return exact 1 client")
	assert.Equal(t, createdClientID, *(clients[0].ID))
	t.Logf("Clients: %+v", clients)

	// Getting exact client
	createdClient, err := client.GetClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		createdClientID,
	)
	assert.NoError(t, err, "GetClient failed")
	t.Logf("Created client: %+v", createdClient)
	// Checking that GetClient returns same client
	assert.Equal(t, clients[0], createdClient)

	// Updating the client

	// Should fail
	err = client.UpdateClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		Client{},
	)
	assert.Error(t, err, "Should fail because of missing ID of the client")

	// Update existing client
	createdClient.Name = GetRandomNameP("Name")
	err = client.UpdateClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*createdClient,
	)
	assert.NoError(t, err, "GetClient failed")

	// Getting updated client
	updatedClient, err := client.GetClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		createdClientID,
	)
	assert.NoError(t, err, "GetClient failed")
	t.Logf("Update client: %+v", createdClient)
	assert.Equal(t, *createdClient, *updatedClient)

	// Deleting the client
	err = client.DeleteClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		createdClientID,
	)
	assert.NoError(t, err, "DeleteClient failed")

	// Verifying that the client was deleted
	clients, err = client.GetClients(
		token.AccessToken,
		cfg.GoCloak.Realm,
		GetClientsParams{
			ClientID: clientID,
		},
	)
	assert.NoError(t, err, "CreateClients failed")
	assert.Len(t, clients, 0, "GetClients should not return any clients")
}

func TestGocloak_GetGroups(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	_, err := client.GetGroups(
		token.AccessToken,
		cfg.GoCloak.Realm,
		GetGroupsParams{})
	FailIfErr(t, err, "GetGroups failed")
}

func TestGocloak_GetGroupsFull(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, groupID := CreateGroup(t, client)
	defer tearDown()

	groups, err := client.GetGroups(
		token.AccessToken,
		cfg.GoCloak.Realm,
		GetGroupsParams{
			Full: BoolP(true),
		})
	assert.NoError(t, err, "GetGroups failed")

	for _, group := range groups {
		if NilOrEmpty(group.ID) {
			continue
		}
		if *(group.ID) == groupID {
			ok := client.UserAttributeContains(group.Attributes, "foo", "alice")
			assert.True(t, ok, "UserAttributeContains")
			return
		}
	}

	assert.Fail(t, "GetGroupsFull failed")
}

func TestGocloak_GetGroupFull(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, groupID := CreateGroup(t, client)
	defer tearDown()

	createdGroup, err := client.GetGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID,
	)
	assert.NoError(t, err, "GetGroup failed")

	ok := client.UserAttributeContains(createdGroup.Attributes, "foo", "alice")
	assert.True(t, ok, "UserAttributeContains")
}

func TestGocloak_GetGroupMembers(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()

	tearDownGroup, groupID := CreateGroup(t, client)
	defer tearDownGroup()

	err := client.AddUserToGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		groupID,
	)
	assert.NoError(t, err, "AddUserToGroup failed")

	users, err := client.GetGroupMembers(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID,
		GetGroupsParams{},
	)
	assert.NoError(t, err, "AddUserToGroup failed")

	assert.Equal(
		t,
		1,
		len(users),
	)
}

func TestGocloak_GetClientRoles(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	testClient := GetClientByClientID(t, client, cfg.GoCloak.ClientID)

	_, err := client.GetClientRoles(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*(testClient.ID))
	FailIfErr(t, err, "GetClientRoles failed")
}

func TestGocloak_GetRoleMappingByGroupID(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, groupID := CreateGroup(t, client)
	defer tearDown()

	_, err := client.GetRoleMappingByGroupID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID)
	FailIfErr(t, err, "GetRoleMappingByGroupID failed")
}

func TestGocloak_GetRoleMappingByUserID(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()

	_, err := client.GetRoleMappingByUserID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID)
	FailIfErr(t, err, "GetRoleMappingByUserID failed")
}

func TestGocloak_ExecuteActionsEmail_UpdatePassword(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()

	params := ExecuteActionsEmail{
		ClientID: &(cfg.GoCloak.ClientID),
		UserID:   &userID,
		Actions:  []string{"UPDATE_PASSWORD"},
	}

	err := client.ExecuteActionsEmail(
		token.AccessToken,
		cfg.GoCloak.Realm,
		params)

	if err != nil {
		if err.Error() == "500 Internal Server Error: Failed to send execute actions email" {
			return
		}
		FailIfErr(t, err, "ExecuteActionsEmail failed")
	}
}

func TestGocloak_Logout(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetUserToken(t, client)

	err := client.Logout(
		cfg.GoCloak.ClientID,
		cfg.GoCloak.ClientSecret,
		cfg.GoCloak.Realm,
		token.RefreshToken)
	FailIfErr(t, err, "Logout failed")
}

func TestGocloak_GetRealm(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	r, err := client.GetRealm(
		token.AccessToken,
		cfg.GoCloak.Realm)
	t.Logf("%+v", r)
	FailIfErr(t, err, "GetRealm failed")
}

func TestGocloak_GetRealms(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	r, err := client.GetRealms(token.AccessToken)
	t.Logf("%+v", r)
	FailIfErr(t, err, "GetRealms failed")
}

// -----------
// Realm
// -----------

func CreateRealm(t *testing.T, client GoCloak) (func(), string) {
	token := GetAdminToken(t, client)

	realmName := GetRandomName("Realm")
	t.Logf("Creating Realm: %s", realmName)
	realmID, err := client.CreateRealm(
		token.AccessToken,
		RealmRepresentation{
			Realm: &realmName,
		})
	assert.NoError(t, err, "CreateRealm failed")
	assert.Equal(t, realmID, realmName)
	tearDown := func() {
		err := client.DeleteRealm(
			token.AccessToken,
			realmName)
		assert.NoError(t, err, "DeleteRealm failed")
	}
	return tearDown, realmName
}

func TestGocloak_CreateRealm(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	tearDown, _ := CreateRealm(t, client)
	defer tearDown()
}

func TestGocloak_ClearRealmCache(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	ClearRealmCache(t, client)
}

// -----------
// Realm Roles
// -----------

func CreateRealmRole(t *testing.T, client GoCloak) (func(), string) {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)

	roleName := GetRandomName("Role")
	t.Logf("Creating RoleName: %s", roleName)
	realmRoleID, err := client.CreateRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		Role{
			Name:        &roleName,
			ContainerID: StringP("asd"),
		})
	assert.NoError(t, err, "CreateRealmRole failed")
	assert.Equal(t, roleName, realmRoleID)
	tearDown := func() {
		err := client.DeleteRealmRole(
			token.AccessToken,
			cfg.GoCloak.Realm,
			roleName)
		assert.NoError(t, err, "DeleteRealmRole failed")
	}
	return tearDown, roleName
}

func TestGocloak_CreateRealmRole(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)
	tearDown, _ := CreateRealmRole(t, client)
	defer tearDown()
}

func TestGocloak_GetRealmRole(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, roleName := CreateRealmRole(t, client)
	defer tearDown()

	role, err := client.GetRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		roleName)
	FailIfErr(t, err, "GetRealmRole failed")
	t.Logf("Role: %+v", *role)
	FailIf(
		t,
		*(role.Name) != roleName,
		"GetRealmRole returns unexpected result. Expected: %s; Actual: %+v",
		roleName, role)
}

func TestGocloak_GetRealmRoles(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, _ := CreateRealmRole(t, client)
	defer tearDown()

	roles, err := client.GetRealmRoles(
		token.AccessToken,
		cfg.GoCloak.Realm)
	FailIfErr(t, err, "GetRealmRoles failed")
	t.Logf("Roles: %+v", roles)
}

func TestGocloak_UpdateRealmRole(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	newRoleName := GetRandomName("Role")
	_, oldRoleName := CreateRealmRole(t, client)

	err := client.UpdateRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		oldRoleName,
		Role{
			Name: &newRoleName,
		})
	assert.NoError(t, err, "UpdateRealmRole failed")
	err = client.DeleteRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		oldRoleName)
	assert.Error(
		t,
		err,
		"Role with old name was deleted successfully, but it shouldn't. Old role: %s; Updated role: %s",
		oldRoleName, newRoleName)
	err = client.DeleteRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		newRoleName)
	assert.NoError(t, err, "DeleteRealmRole failed")
}

func TestGocloak_DeleteRealmRole(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	_, roleName := CreateRealmRole(t, client)

	err := client.DeleteRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		roleName)
	FailIfErr(t, err, "DeleteRealmRole failed")
}

func TestGocloak_AddRealmRoleToUser_DeleteRealmRoleFromUser(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()
	tearDownRole, roleName := CreateRealmRole(t, client)
	defer tearDownRole()
	role, err := client.GetRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		roleName)
	assert.NoError(t, err, "GetRealmRole failed")

	roles := []Role{*role}
	err = client.AddRealmRoleToUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		roles,
	)
	assert.NoError(t, err, "AddRealmRoleToUser failed")
	err = client.DeleteRealmRoleFromUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		roles,
	)
	assert.NoError(t, err, "DeleteRealmRoleFromUser failed")
}

func TestGocloak_GetRealmRolesByUserID(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()
	tearDownRole, roleName := CreateRealmRole(t, client)
	defer tearDownRole()
	role, err := client.GetRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		roleName)
	assert.NoError(t, err)

	err = client.AddRealmRoleToUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		[]Role{
			*role,
		})
	assert.NoError(t, err)

	roles, err := client.GetRealmRolesByUserID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID)
	assert.NoError(t, err)
	t.Logf("User roles: %+v", roles)
	for _, r := range roles {
		if r.Name == nil {
			continue
		}
		if *(r.Name) == *(role.Name) {
			return
		}
	}
	assert.Fail(t, "The role has not been found in the assined roles. Role: %+v", *role)
}

func TestGocloak_GetRealmRolesByGroupID(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, groupID := CreateGroup(t, client)
	defer tearDown()

	_, err := client.GetRealmRolesByGroupID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		groupID)
	FailIfErr(t, err, "GetRealmRolesByGroupID failed")
}

func TestGocloak_AddRealmRoleComposite(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, compositeRole := CreateRealmRole(t, client)
	defer tearDown()

	tearDown, role := CreateRealmRole(t, client)
	defer tearDown()

	roleModel, err := client.GetRealmRole(token.AccessToken, cfg.GoCloak.Realm, role)
	FailIfErr(t, err, "Can't get just created role with GetRealmRole")

	err = client.AddRealmRoleComposite(token.AccessToken,
		cfg.GoCloak.Realm, compositeRole, []Role{*roleModel})
	FailIfErr(t, err, "AddRealmRoleComposite failed")
}

func TestGocloak_DeleteRealmRoleComposite(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, compositeRole := CreateRealmRole(t, client)
	defer tearDown()

	tearDown, role := CreateRealmRole(t, client)
	defer tearDown()

	roleModel, err := client.GetRealmRole(token.AccessToken, cfg.GoCloak.Realm, role)
	FailIfErr(t, err, "Can't get just created role with GetRealmRole")

	err = client.AddRealmRoleComposite(token.AccessToken,
		cfg.GoCloak.Realm, compositeRole, []Role{*roleModel})
	FailIfErr(t, err, "AddRealmRoleComposite failed")

	err = client.DeleteRealmRoleComposite(token.AccessToken,
		cfg.GoCloak.Realm, compositeRole, []Role{*roleModel})
	FailIfErr(t, err, "DeleteRealmRoleComposite failed")
}

// -----
// Users
// -----

func CreateUser(t *testing.T, client GoCloak) (func(), string) {
	cfg := GetConfig(t)
	token := GetAdminToken(t, client)

	user := User{
		FirstName: GetRandomNameP("FirstName"),
		LastName:  GetRandomNameP("LastName"),
		Email:     StringP(GetRandomName("email") + "@localhost"),
		Enabled:   BoolP(true),
		Attributes: map[string][]string{
			"foo": {"bar", "alice", "bob", "roflcopter"},
			"bar": {"baz"},
		},
	}
	user.Username = user.Email

	userID, err := client.CreateUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		user)
	FailIfErr(t, err, "CreateUser failed")
	user.ID = &userID
	t.Logf("Created User: %+v", user)
	tearDown := func() {
		err := client.DeleteUser(
			token.AccessToken,
			cfg.GoCloak.Realm,
			*(user.ID))
		FailIfErr(t, err, "DeleteUser")
	}

	return tearDown, *(user.ID)
}

func TestGocloak_CreateUser(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)

	tearDown, _ := CreateUser(t, client)
	defer tearDown()
}

func TestGocloak_CreateUserCustomAttributes(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()

	fetchedUser, err := client.GetUserByID(token.AccessToken,
		cfg.GoCloak.Realm,
		userID)
	FailIfErr(t, err, "GetUserByID failed")
	ok := client.UserAttributeContains(fetchedUser.Attributes, "foo", "alice")
	FailIf(t, !ok, "User doesn't have custom attributes")
	ok = client.UserAttributeContains(fetchedUser.Attributes, "foo2", "alice")
	FailIf(t, ok, "User's custom attributes contains unexpected attribute")
	t.Log(fetchedUser)
}

func TestGocloak_GetUserByID(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()

	fetchedUser, err := client.GetUserByID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID)
	FailIfErr(t, err, "GetUserById failed")
	t.Log(fetchedUser)
}

func TestGocloak_GetUsers(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	users, err := client.GetUsers(
		token.AccessToken,
		cfg.GoCloak.Realm,
		GetUsersParams{
			Username: &(cfg.GoCloak.UserName),
		})
	FailIfErr(t, err, "GetUsers failed")
	t.Log(users)
}

func TestGocloak_GetUserCount(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	count, err := client.GetUserCount(
		token.AccessToken,
		cfg.GoCloak.Realm)
	t.Logf("Users in Realm: %d", count)
	FailIfErr(t, err, "GetUserCount failed")
}

func TestGocloak_AddUserToGroup(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()

	tearDownGroup, groupID := CreateGroup(t, client)
	defer tearDownGroup()

	err := client.AddUserToGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		groupID,
	)
	FailIfErr(t, err, "AddUserToGroup failed")
}

func TestGocloak_DeleteUserFromGroup(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()

	tearDownGroup, groupID := CreateGroup(t, client)
	defer tearDownGroup()
	err := client.AddUserToGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		groupID,
	)
	FailIfErr(t, err, "AddUserToGroup failed")
	err = client.DeleteUserFromGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		groupID,
	)
	FailIfErr(t, err, "DeleteUserFromGroup failed")
}

func TestGocloak_GetUserGroups(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()

	tearDownGroup, groupID := CreateGroup(t, client)
	defer tearDownGroup()

	err := client.AddUserToGroup(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		groupID,
	)
	assert.NoError(t, err)
	groups, err := client.GetUserGroups(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID)
	assert.NoError(t, err)
	assert.NotEqual(
		t,
		len(groups),
		0,
	)
	assert.Equal(
		t,
		groupID,
		*(groups[0].ID))
}

func TestGocloak_DeleteUser(t *testing.T) {
	t.Parallel()
	client := NewClientWithDebug(t)

	tearDown, _ := CreateUser(t, client)
	defer tearDown()
}

func TestGocloak_UpdateUser(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()
	user, err := client.GetUserByID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID)
	FailIfErr(t, err, "GetUserByID failed")
	user.FirstName = GetRandomNameP("UpdateUserFirstName")
	err = client.UpdateUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*user)
	FailIfErr(t, err, "UpdateUser failed")
}

func TestGocloak_UpdateUserSetEmptyEmail(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDown, userID := CreateUser(t, client)
	defer tearDown()
	user, err := client.GetUserByID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
	)
	assert.NoError(t, err)
	user.Email = StringP("")
	err = client.UpdateUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*user)
	assert.NoError(t, err)
	user, err = client.GetUserByID(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
	)
	assert.NoError(t, err)
	assert.Nil(t, user.Email)
}

func TestGocloak_GetUsersByRoleName(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	tearDownUser, userID := CreateUser(t, client)
	defer tearDownUser()

	tearDownRole, roleName := CreateRealmRole(t, client)
	defer tearDownRole()

	role, err := client.GetRealmRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		roleName)
	assert.NoError(t, err)
	err = client.AddRealmRoleToUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		userID,
		[]Role{
			*role,
		})
	assert.NoError(t, err)

	users, err := client.GetUsersByRoleName(
		token.AccessToken,
		cfg.GoCloak.Realm,
		roleName)
	assert.NoError(t, err)

	assert.NotEqual(
		t,
		len(users),
		0,
	)
	assert.Equal(
		t,
		userID,
		*(users[0].ID),
	)
}

func TestGocloak_GetUserSessions(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	_, err := client.GetToken(
		cfg.GoCloak.Realm,
		TokenOptions{
			ClientID:     &(cfg.GoCloak.ClientID),
			ClientSecret: &(cfg.GoCloak.ClientSecret),
			Username:     &(cfg.GoCloak.UserName),
			Password:     &(cfg.GoCloak.Password),
			GrantType:    StringP("password"),
		},
	)
	FailIfErr(t, err, "Login failed")
	token := GetAdminToken(t, client)
	sessions, err := client.GetUserSessions(
		token.AccessToken,
		cfg.GoCloak.Realm,
		testUserID,
	)
	FailIfErr(t, err, "GetUserSessions failed")
	FailIf(t, len(sessions) == 0, "GetUserSessions returned an empty list")
}

func TestGocloak_GetUserOfflineSessionsForClient(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	_, err := client.GetToken(
		cfg.GoCloak.Realm,
		TokenOptions{
			ClientID:      &(cfg.GoCloak.ClientID),
			ClientSecret:  &(cfg.GoCloak.ClientSecret),
			Username:      &(cfg.GoCloak.UserName),
			Password:      &(cfg.GoCloak.Password),
			GrantType:     StringP("password"),
			ResponseTypes: []string{"token", "id_token"},
			Scopes:        []string{"openid", "offline_access"},
		},
	)
	FailIfErr(t, err, "Login failed")
	token := GetAdminToken(t, client)
	sessions, err := client.GetUserOfflineSessionsForClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		testUserID,
		gocloakClientID,
	)
	FailIfErr(t, err, "GetUserOfflineSessionsForClient failed")
	FailIf(t, len(sessions) == 0, "GetUserOfflineSessionsForClient returned an empty list")
}

func TestGocloak_GetClientUserSessions(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	_, err := client.GetToken(
		cfg.GoCloak.Realm,
		TokenOptions{
			ClientID:     &(cfg.GoCloak.ClientID),
			ClientSecret: &(cfg.GoCloak.ClientSecret),
			Username:     &(cfg.GoCloak.UserName),
			Password:     &(cfg.GoCloak.Password),
			GrantType:    StringP("password"),
		},
	)
	FailIfErr(t, err, "Login failed")
	token := GetAdminToken(t, client)
	sessions, err := client.GetClientUserSessions(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
	)
	FailIfErr(t, err, "GetClientUserSessions failed")
	FailIf(t, len(sessions) == 0, "GetClientUserSessions returned an empty list")
}

func TestGocloak_CreateDeleteClientProtocolMapper(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	id := GetRandomName("protocol-mapper-id-")
	testClient := GetClientByClientID(t, client, cfg.GoCloak.ClientID)

	found := false
	for _, protocolMapper := range testClient.ProtocolMappers {
		if protocolMapper == nil || NilOrEmpty(protocolMapper.ID) {
			continue
		}
		if *(protocolMapper.ID) == id {
			found = true
			break
		}
	}
	assert.False(
		t,
		found,
		"default client should not have a protocol mapper with ID: %s", id,
	)

	token := GetAdminToken(t, client)
	createdID, err := client.CreateClientProtocolMapper(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*(testClient.ID),
		ProtocolMapperRepresentation{
			ID:             &id,
			Name:           StringP("test"),
			Protocol:       StringP("openid-connect"),
			ProtocolMapper: StringP("oidc-usermodel-attribute-mapper"),
			Config: map[string]string{
				"access.token.claim":   "true",
				"aggregate.attrs":      "",
				"claim.name":           "test",
				"id.token.claim":       "true",
				"jsonType.label":       "String",
				"multivalued":          "",
				"user.attribute":       "test",
				"userinfo.token.claim": "true",
			},
		},
	)
	assert.NoError(t, err, "CreateClientProtocolMapper failed")
	assert.Equal(t, id, createdID)
	testClientAfter := GetClientByClientID(t, client, cfg.GoCloak.ClientID)

	found = false
	for _, protocolMapper := range testClientAfter.ProtocolMappers {
		if protocolMapper == nil || NilOrEmpty(protocolMapper.ID) {
			continue
		}
		if *(protocolMapper.ID) == id {
			found = true
			break
		}
	}
	assert.True(
		t,
		found,
		"protocol mapper has not been created",
	)
	err = client.DeleteClientProtocolMapper(
		token.AccessToken,
		cfg.GoCloak.Realm,
		*(testClient.ID),
		id,
	)
	assert.NoError(t, err, "DeleteClientProtocolMapper failed")
	testClientAgain := GetClientByClientID(t, client, cfg.GoCloak.ClientID)

	found = false
	for _, protocolMapper := range testClientAgain.ProtocolMappers {
		if protocolMapper == nil || NilOrEmpty(protocolMapper.ID) {
			continue
		}
		if *(protocolMapper.ID) == id {
			found = true
			break
		}
	}
	assert.False(
		t,
		found,
		"default client should not have a protocol mapper with ID: %s", id,
	)
}

func TestGocloak_GetClientOfflineSessions(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	_, err := client.GetToken(
		cfg.GoCloak.Realm,
		TokenOptions{
			ClientID:      &(cfg.GoCloak.ClientID),
			ClientSecret:  &(cfg.GoCloak.ClientSecret),
			Username:      &(cfg.GoCloak.UserName),
			Password:      &(cfg.GoCloak.Password),
			GrantType:     StringP("password"),
			ResponseTypes: []string{"token", "id_token"},
			Scopes:        []string{"openid", "offline_access"},
		},
	)
	FailIfErr(t, err, "Login failed")
	token := GetAdminToken(t, client)
	sessions, err := client.GetClientOfflineSessions(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
	)
	FailIfErr(t, err, "GetClientOfflineSessions failed")
	FailIf(t, len(sessions) == 0, "GetClientOfflineSessions returned an empty list")
}

func TestGoCloak_ClientSecret(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	testClient := Client{
		ID:                      GetRandomNameP("gocloak-client-id-"),
		ClientID:                GetRandomNameP("gocloak-client-secret-client-id-"),
		Secret:                  StringP("initial-secret-key"),
		ServiceAccountsEnabled:  BoolP(true),
		StandardFlowEnabled:     BoolP(true),
		Enabled:                 BoolP(true),
		FullScopeAllowed:        BoolP(true),
		Protocol:                StringP("openid-connect"),
		RedirectURIs:            []string{"localhost"},
		ClientAuthenticatorType: StringP("client-secret"),
	}

	clientID, err := client.CreateClient(
		token.AccessToken,
		cfg.GoCloak.Realm,
		testClient,
	)
	assert.NoError(t, err, "CreateClient failed")
	assert.Equal(t, *(testClient.ID), clientID)

	oldCreds, err := client.GetClientSecret(
		token.AccessToken,
		cfg.GoCloak.Realm,
		clientID,
	)
	assert.NoError(t, err, "GetClientSecret failed")

	regeneratedCreds, err := client.RegenerateClientSecret(
		token.AccessToken,
		cfg.GoCloak.Realm,
		clientID,
	)
	assert.NoError(t, err, "RegenerateClientSecret failed")

	assert.NotEqual(t, *(oldCreds.Value), *(regeneratedCreds.Value))

	err = client.DeleteClient(token.AccessToken, cfg.GoCloak.Realm, clientID)
	assert.NoError(t, err, "DeleteClient failed")
}

func TestGoCloak_ClientServiceAccount(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)

	serviceAccount, err := client.GetClientServiceAccount(token.AccessToken, cfg.GoCloak.Realm, gocloakClientID)
	assert.NoError(t, err)

	assert.NotNil(t, serviceAccount.ID)
	assert.NotNil(t, serviceAccount.Username)
	assert.NotEqual(t, gocloakClientID, *(serviceAccount.ID))
	assert.Equal(t, "service-account-gocloak", *(serviceAccount.Username))
}

func TestGocloak_AddClientRoleToUser_DeleteClientRoleFromUser(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	SetUpTestUser(t, client)
	tearDown1, roleName1 := CreateClientRole(t, client)
	defer tearDown1()
	token := GetAdminToken(t, client)
	role1, err := client.GetClientRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		roleName1,
	)
	assert.NoError(t, err, "GetClientRole failed")
	tearDown2, roleName2 := CreateClientRole(t, client)
	defer tearDown2()
	role2, err := client.GetClientRole(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		roleName2,
	)
	assert.NoError(t, err, "GetClientRole failed")
	roles := []Role{*role1, *role2}
	err = client.AddClientRoleToUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		testUserID,
		roles,
	)
	assert.NoError(t, err, "AddClientRoleToUser failed")

	err = client.DeleteClientRoleFromUser(
		token.AccessToken,
		cfg.GoCloak.Realm,
		gocloakClientID,
		testUserID,
		roles,
	)
	assert.NoError(t, err, "DeleteClientRoleFromUser failed")
}

func TestGocloak_CreateDeleteClientScopeWithMappers(t *testing.T) {
	t.Parallel()
	cfg := GetConfig(t)
	client := NewClientWithDebug(t)
	token := GetAdminToken(t, client)
	defer ClearRealmCache(t, client)

	id := GetRandomName("client-scope-id-")
	rolemapperID := GetRandomName("client-rolemapper-id-")
	audiencemapperID := GetRandomName("client-audiencemapper-id-")

	createdID, err := client.CreateClientScope(
		token.AccessToken,
		cfg.GoCloak.Realm,
		ClientScope{
			ID:          &id,
			Name:        StringP("test-scope"),
			Description: StringP("testing scope"),
			Protocol:    StringP("openid-connect"),
			ClientScopeAttributes: &ClientScopeAttributes{
				ConsentScreenText:      StringP("false"),
				DisplayOnConsentScreen: StringP("true"),
				IncludeInTokenScope:    StringP("false"),
			},
			ProtocolMappers: []*ProtocolMappers{
				{
					ID:              &rolemapperID,
					Name:            StringP("roles"),
					Protocol:        StringP("openid-connect"),
					ProtocolMapper:  StringP("oidc-usermodel-client-role-mapper"),
					ConsentRequired: BoolP(false),
					ProtocolMappersConfig: &ProtocolMappersConfig{
						UserinfoTokenClaim:                 StringP("false"),
						AccessTokenClaim:                   StringP("true"),
						IDTokenClaim:                       StringP("true"),
						ClaimName:                          StringP("test"),
						Multivalued:                        StringP("true"),
						UsermodelClientRoleMappingClientID: StringP("test"),
					},
				},
				{
					ID:              &audiencemapperID,
					Name:            StringP("audience"),
					Protocol:        StringP("openid-connect"),
					ProtocolMapper:  StringP("oidc-audience-mapper"),
					ConsentRequired: BoolP(false),
					ProtocolMappersConfig: &ProtocolMappersConfig{
						UserinfoTokenClaim:     StringP("false"),
						IDTokenClaim:           StringP("true"),
						AccessTokenClaim:       StringP("true"),
						IncludedClientAudience: StringP("test"),
					},
				},
			},
		},
	)
	assert.NoError(t, err, "CreateClientScope failed")
	assert.Equal(t, id, createdID)
	clientScopeActual, err := client.GetClientScope(token.AccessToken, cfg.GoCloak.Realm, id)
	assert.NoError(t, err)

	assert.NotNil(t, clientScopeActual, "client scope has not been created")
	assert.Len(t, clientScopeActual.ProtocolMappers, 2, "unexpected number of protocol mappers created")
	err = client.DeleteClientScope(
		token.AccessToken,
		cfg.GoCloak.Realm,
		id,
	)
	assert.NoError(t, err, "DeleteClientScope failed")
	clientScopeActual, err = client.GetClientScope(token.AccessToken, cfg.GoCloak.Realm, id)
	assert.EqualError(t, err, "404 Not Found: Could not find client scope")
	assert.Nil(t, clientScopeActual, "client scope has not been deleted")
}
