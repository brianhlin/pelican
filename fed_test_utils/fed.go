//go:build !windows

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

package fed_test_utils

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"

	"github.com/pelicanplatform/pelican/config"
	"github.com/pelicanplatform/pelican/director"
	"github.com/pelicanplatform/pelican/launchers"
	"github.com/pelicanplatform/pelican/param"
	"github.com/pelicanplatform/pelican/server_structs"
	"github.com/pelicanplatform/pelican/server_utils"
	"github.com/pelicanplatform/pelican/test_utils"
	"github.com/pelicanplatform/pelican/token"
	"github.com/pelicanplatform/pelican/token_scopes"
)

type (
	FedTest struct {
		AdvertiseCancel context.CancelFunc
		Exports         []server_utils.OriginExport
		Token           string
		Ctx             context.Context
		Egrp            *errgroup.Group
		Pids            []int
	}
)

var (
	//go:embed resources/default.yaml
	fedTestDefaultConfig string
)

// Start up a new Pelican federation for unit testing
func NewFedTest(t *testing.T, originConfig string) (ft *FedTest) {
	ft = &FedTest{}
	director.ResetState()

	if originConfig == "" {
		originConfig = fedTestDefaultConfig
	}

	ctx, cancel, egrp := test_utils.TestContext(context.Background(), t)
	shutdownCtx, shutdownCancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, director.AdvertiseShutdownKey, shutdownCtx)
	ctx = context.WithValue(ctx, server_utils.DirectorDiscoveryShutdownKey, shutdownCtx)
	ft.Ctx = ctx
	ft.AdvertiseCancel = shutdownCancel
	ft.Egrp = egrp

	tmpPathPattern := "Pelican-FedTest*"
	tmpPath, err := os.MkdirTemp("", tmpPathPattern)
	require.NoError(t, err)

	// Explicitly run tmpPath cleanup AFTER cancel and egrp are done -- otherwise we end up
	// with a race condition where removing tmpPath might happen while the server is still
	// using it, resulting in "error: unlinkat <tmpPath>: directory not empty"
	t.Cleanup(func() {
		cancel()
		if err := egrp.Wait(); err != nil && err != context.Canceled && err != http.ErrServerClosed {
			require.NoError(t, err)
		}
		err := os.RemoveAll(tmpPath)
		require.NoError(t, err)
		// Throw in a config.Reset for good measure. Keeps our env squeaky clean!
		server_utils.ResetTestState()
	})

	modules := server_structs.ServerType(0)
	modules.Set(server_structs.BrokerType)
	modules.Set(server_structs.CacheType)
	modules.Set(server_structs.OriginType)
	modules.Set(server_structs.DirectorType)
	modules.Set(server_structs.RegistryType)
	// TODO: the cache startup routines not sequenced correctly for the downloads
	// to immediately work through the cache.  For now, unit tests will just use the origin.
	modules.Set(server_structs.LocalCacheType)

	permissions := os.FileMode(0755)
	err = os.Chmod(tmpPath, permissions)
	require.NoError(t, err)

	viper.Set("ConfigDir", tmpPath)
	// Configure all relevant logging levels. We don't let the XRootD
	// log levels inherit from the global log level, because the many
	// fed tests we run back-to-back would otherwise generate a lot of
	// log output.
	viper.Set(param.Logging_Level.GetName(), "debug")
	viper.Set(param.Logging_Origin_Cms.GetName(), "error")
	viper.Set(param.Logging_Origin_Xrd.GetName(), "error")
	viper.Set(param.Logging_Origin_Ofs.GetName(), "error")
	viper.Set(param.Logging_Origin_Oss.GetName(), "error")
	viper.Set(param.Logging_Origin_Http.GetName(), "error")
	viper.Set(param.Logging_Origin_Scitokens.GetName(), "fatal")
	viper.Set(param.Logging_Origin_Xrootd.GetName(), "info")
	viper.Set(param.Logging_Cache_Ofs.GetName(), "error")
	viper.Set(param.Logging_Cache_Pss.GetName(), "error")
	viper.Set(param.Logging_Cache_PssSetOpt.GetName(), "error")
	viper.Set(param.Logging_Cache_Http.GetName(), "error")
	viper.Set(param.Logging_Cache_Xrd.GetName(), "error")
	viper.Set(param.Logging_Cache_Xrootd.GetName(), "error")
	viper.Set(param.Logging_Cache_Scitokens.GetName(), "fatal")
	viper.Set(param.Logging_Cache_Pfc.GetName(), "info")

	viper.Set(param.TLSSkipVerify.GetName(), true)

	// Instead of using "0" as a port directly in the config, which lets XRootD find its own port,
	// we need to know the port in advance for configuring the issuer URLs for each export. To do that
	// without hardcoding the ports (which we can't guarantee are available in the test env), we'll
	// get a few unique, available ports and use them for the origin, cache, and web UIs. This introduces
	// a race condition, however, because it's possible the ports are consumed between getting them from this
	// function and binding the servers to them
	ports, err := test_utils.GetUniqueAvailablePorts(3)
	require.NoError(t, err)
	require.Len(t, ports, 3)

	// Disable functionality we're not using (and is difficult to make work on Mac)
	viper.Set(param.Registry_DbLocation.GetName(), filepath.Join(t.TempDir(), "ns-registry.sqlite"))
	viper.Set(param.Registry_RequireOriginApproval.GetName(), false)
	viper.Set(param.Registry_RequireCacheApproval.GetName(), false)
	viper.Set(param.Director_CacheSortMethod.GetName(), "distance")
	viper.Set(param.Director_DbLocation.GetName(), filepath.Join(t.TempDir(), "director.sqlite"))
	viper.Set(param.Origin_EnableCmsd.GetName(), false)
	viper.Set(param.Origin_EnableVoms.GetName(), false)
	viper.Set(param.Origin_Port.GetName(), ports[0])
	viper.Set(param.Origin_RunLocation.GetName(), filepath.Join(tmpPath, "origin"))
	viper.Set(param.Origin_DbLocation.GetName(), filepath.Join(t.TempDir(), "origin.sqlite"))
	viper.Set(param.Origin_TokenAudience.GetName(), "")
	viper.Set(param.Cache_Port.GetName(), ports[1])
	viper.Set(param.Cache_RunLocation.GetName(), filepath.Join(tmpPath, "cache"))
	viper.Set(param.Cache_StorageLocation.GetName(), filepath.Join(tmpPath, "xcache-data"))
	viper.Set(param.Cache_DbLocation.GetName(), filepath.Join(t.TempDir(), "cache.sqlite"))
	viper.Set(param.Server_EnableUI.GetName(), false)
	viper.Set(param.Server_WebPort.GetName(), ports[2])
	// Unix domain sockets have a maximum length of 108 bytes, so we need to make sure our
	// socket path is short enough to fit within that limit. Mac OS X has long temporary path
	// names, so we need to make sure our socket path is short enough to fit within that limit.
	viper.Set(param.LocalCache_RunLocation.GetName(), filepath.Join(tmpPath, "lc"))
	viper.Set(param.Server_DbLocation.GetName(), filepath.Join(t.TempDir(), "server.sqlite"))

	// Set the Director's start time to 6 minutes ago. This prevents it from sending an HTTP 429 for
	// unknown prefixes.
	directorStartTime := time.Now().Add(-6 * time.Minute)
	director.SetStartupTime(directorStartTime)

	err = config.InitServer(ctx, modules)
	require.NoError(t, err)

	// Read in any config we may have set
	var importedConf any
	viper.SetConfigType("yaml")
	err = viper.MergeConfig(strings.NewReader(originConfig))
	require.NoError(t, err, "error reading config")

	err = yaml.Unmarshal([]byte(originConfig), &importedConf)
	require.NoError(t, err, "error unmarshalling into interface")

	confMap := importedConf.(map[string]any)

	if originRaw, exists := confMap["Origin"]; exists {
		originMap := originRaw.(map[string]any)

		overrideTemp := func(storageDir string, exportMap map[string]any) {
			exportMap["StoragePrefix"] = storageDir

			// Change the permissions of the temporary origin directory
			permissions = os.FileMode(0755)
			err = os.Chmod(storageDir, permissions)
			require.NoError(t, err)

			// Change ownership on the temporary origin directory so files can be uploaded
			uinfo, err := config.GetDaemonUserInfo()
			require.NoError(t, err)
			require.NoError(t, os.Chown(storageDir, uinfo.Uid, uinfo.Gid))

			// Start off with a Hello World file we can use for testing in each of our exports
			err = os.WriteFile(filepath.Join(storageDir, "hello_world.txt"), []byte("Hello, World!"), os.FileMode(0644))
			require.NoError(t, err)
		}

		// Override the test directory from the config file with our temp directory
		if exportsRaw, exists := originMap["Exports"]; exists {
			for i, item := range exportsRaw.([]any) {
				originDir, err := os.MkdirTemp("", fmt.Sprintf("Export%d", i))
				assert.NoError(t, err)
				t.Cleanup(func() {
					err := os.RemoveAll(originDir)
					require.NoError(t, err)
				})

				exportMap := item.(map[string]any)
				overrideTemp(originDir, exportMap)
			}
		} else {
			originDir, err := os.MkdirTemp("", fmt.Sprintf("Export%s", "test"))
			assert.NoError(t, err)
			t.Cleanup(func() {
				err := os.RemoveAll(originDir)
				require.NoError(t, err)
			})

			overrideTemp(originDir, originMap)
		}
	}

	confDir := t.TempDir()
	outputPath := filepath.Join(confDir, "tempfile_*.yaml")

	outputData, err := yaml.Marshal(&importedConf)
	require.NoError(t, err, "error marshalling struct into yaml format")

	err = os.WriteFile(outputPath, outputData, 0644)
	require.NoError(t, err, "error writing out temporary config file for fed test")

	viper.Set("config", outputPath)

	servers, _, err := launchers.LaunchModules(ctx, modules)
	require.NoError(t, err)

	ft.Pids = make([]int, 0, 2)
	for _, server := range servers {
		ft.Pids = append(ft.Pids, server.GetPids()...)
	}

	// Set up discovery for federation metadata hosting. This needs to be done AFTER launching
	// servers, because they populate the param values we use to set the metadata.
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/pelican-configuration" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(fmt.Sprintf(`{
				"director_endpoint": "%s",
				"namespace_registration_endpoint": "%s",
				"broker_endpoint": "%s",
				"jwks_uri": "%s"
			}`, param.Server_ExternalWebUrl.GetString(), param.Server_ExternalWebUrl.GetString(), param.Server_ExternalWebUrl.GetString(), param.Server_ExternalWebUrl.GetString())))
			assert.NoError(t, err)
		} else {
			http.NotFound(w, r)
		}
	}
	discoveryServer := httptest.NewTLSServer(http.HandlerFunc(handler))
	t.Cleanup(discoveryServer.Close)
	viper.Set(param.Federation_DiscoveryUrl.GetName(), discoveryServer.URL)

	desiredURL := param.Server_ExternalWebUrl.GetString() + "/api/v1.0/health"
	err = server_utils.WaitUntilWorking(ctx, "GET", desiredURL, "director", 200, false)
	require.NoError(t, err)

	httpc := http.Client{
		Transport: config.GetTransport(),
	}
	resp, err := httpc.Get(desiredURL)
	require.NoError(t, err)

	assert.Equal(t, resp.StatusCode, http.StatusOK)

	responseBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	expectedResponse := struct {
		Msg string `json:"message"`
	}{}
	err = json.Unmarshal(responseBody, &expectedResponse)
	require.NoError(t, err)
	assert.NotEmpty(t, expectedResponse.Msg)

	issuer, err := config.GetServerIssuerURL()
	require.NoError(t, err)
	tokConf := token.NewWLCGToken()
	tokConf.Lifetime = time.Duration(time.Minute)
	tokConf.Issuer = issuer
	tokConf.Subject = "test"
	tokConf.AddAudienceAny()
	tokConf.AddResourceScopes(token_scopes.NewResourceScope(token_scopes.Wlcg_Storage_Read, "/hello_world.txt"))

	token, err := tokConf.CreateToken()
	require.NoError(t, err)

	ft.Token = token

	ft.Exports, err = server_utils.GetOriginExports()
	require.NoError(t, err)

	return
}
