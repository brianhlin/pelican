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

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pelicanplatform/pelican/config"
	"github.com/pelicanplatform/pelican/server_utils"
)

// Create tmpdir, change cwd, and setup clean up functions
func setupTestRun(t *testing.T) string {
	config.ResetIssuerPrivateKeys()
	wd, err := os.Getwd()
	require.NoError(t, err)

	tmpDir := t.TempDir()
	err = os.Chdir(tmpDir)
	require.NoError(t, err)

	t.Cleanup(func() {
		err := os.Chdir(wd)
		require.NoError(t, err)
		server_utils.ResetTestState()
	})
	return tmpDir
}

func checkKeys(t *testing.T, privateKey, publicKey string) {
	_, err := config.LoadPrivateKey(privateKey, false)
	require.NoError(t, err)

	jwks, err := jwk.ReadFile(publicKey)
	require.NoError(t, err)
	require.Equal(t, 1, jwks.Len())
	key, ok := jwks.Key(0)
	assert.True(t, ok)
	err = key.Validate()
	assert.NoError(t, err)

	// The "alg" and "kid" keys must explicitly be added to the JWK.
	// Thus, we test that this actually happened.
	// See also: https://github.com/PelicanPlatform/pelican/issues/2084
	_, ok = key.Get("alg")
	assert.True(t, ok)
	_, ok = key.Get("kid")
	assert.True(t, ok)
}

func TestKeygenMain(t *testing.T) {
	config.ResetIssuerPrivateKeys()

	t.Cleanup(func() {
		server_utils.ResetTestState()
	})

	t.Run("no-args-gen-to-wd", func(t *testing.T) {
		tempDir := setupTestRun(t)

		privateKeyPath = ""
		publicKeyPath = ""
		err := keygenMain(nil, []string{})
		require.NoError(t, err)

		checkKeys(
			t,
			filepath.Join(tempDir, "private-key.pem"),
			filepath.Join(tempDir, "issuer-pub.jwks"),
		)
	})

	t.Run("private-arg-present", func(t *testing.T) {
		tempWd := setupTestRun(t)
		tmpDir := filepath.Join(tempWd, "tmp")

		privateKeyPath = filepath.Join(tmpDir, "test.pk")
		publicKeyPath = ""
		err := keygenMain(nil, []string{})
		require.NoError(t, err)

		checkKeys(
			t,
			privateKeyPath,
			filepath.Join(tempWd, "issuer-pub.jwks"),
		)
	})

	t.Run("public-arg-present", func(t *testing.T) {
		tempWd := setupTestRun(t)
		tmpDir := filepath.Join(tempWd, "tmp")

		privateKeyPath = ""
		publicKeyPath = filepath.Join(tmpDir, "test.pub")
		err := keygenMain(nil, []string{})
		require.NoError(t, err)

		checkKeys(
			t,
			filepath.Join(tempWd, "private-key.pem"),
			publicKeyPath,
		)
	})

	t.Run("private-arg-with-newline", func(t *testing.T) {
		tempWd := setupTestRun(t)
		tmpDir := filepath.Join(tempWd, "tmp")

		privateKeyPath = filepath.Join(tmpDir, "test.pk")
		privateKeyPath += "\n"
		publicKeyPath = ""
		err := keygenMain(nil, []string{})
		require.NoError(t, err)

		checkKeys(
			t,
			privateKeyPath,
			filepath.Join(tempWd, "issuer-pub.jwks"),
		)
	})

	t.Run("public-arg-with-newline", func(t *testing.T) {
		tempWd := setupTestRun(t)
		tmpDir := filepath.Join(tempWd, "tmp")

		privateKeyPath = ""
		publicKeyPath = filepath.Join(tmpDir, "test.pub")
		publicKeyPath += "\n"
		err := keygenMain(nil, []string{})
		require.NoError(t, err)

		checkKeys(
			t,
			filepath.Join(tempWd, "private-key.pem"),
			publicKeyPath,
		)
	})
}

func TestKeygenMainWithExistingFile(t *testing.T) {
	server_utils.ResetTestState()
	t.Cleanup(func() {
		server_utils.ResetTestState()
	})

	// If a private key file exists, the derived public key file should be created by the command
	t.Run("private-key-exists", func(t *testing.T) {
		tempDir := t.TempDir()
		publicKeyPath = filepath.Join(tempDir, "issuer-pub.jwks")

		// Create a private key file
		privateKey, err := config.GeneratePEM(tempDir)
		require.NoError(t, err)
		assert.NotEmpty(t, privateKey.KeyID())

		// There should be only one file in the temp directory, which is the private key file
		files, err := os.ReadDir(tempDir)
		require.NoError(t, err)
		require.Len(t, files, 1)
		// Set the private key path to the existing private key file
		privateKeyPath = filepath.Join(tempDir, files[0].Name())

		err = keygenMain(nil, []string{})
		require.NoError(t, err)

		// Check that the public key file is created
		assert.FileExists(t, filepath.Join(tempDir, "issuer-pub.jwks"))

		// Check that the public key file contains the correct key
		jwks, err := jwk.ReadFile(filepath.Join(tempDir, "issuer-pub.jwks"))
		require.NoError(t, err)
		assert.Equal(t, 1, jwks.Len())
		key, ok := jwks.Key(0)
		assert.True(t, ok)
		assert.Equal(t, privateKey.KeyID(), key.KeyID())
	})

	// If a public key file exists, the command should fail
	t.Run("public-key-exists", func(t *testing.T) {
		tempDir := t.TempDir()
		err := os.WriteFile(filepath.Join(tempDir, "test.pub"), []byte{}, 0644)
		require.NoError(t, err)
		privateKeyPath = filepath.Join(tempDir, "test.pk")
		publicKeyPath = filepath.Join(tempDir, "test.pub")
		err = keygenMain(nil, []string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "file exists for public key")
	})

}
