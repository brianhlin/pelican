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
	"crypto/elliptic"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/pelicanplatform/pelican/config"
)

func createJWKS(key jwk.Key) (jwk.Set, error) {
	jwks := jwk.NewSet()

	pkey, err := jwk.PublicKeyOf(key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to generate public key from key %s", key.KeyID())
	}

	if err = jwks.AddKey(pkey); err != nil {
		return nil, errors.Wrapf(err, "failed to add public key %s to new JWKS", key.KeyID())
	}

	return jwks, nil
}

func keygenMain(cmd *cobra.Command, args []string) error {
	wd, err := os.Getwd()
	if err != nil {
		return errors.Wrap(err, "failed to get the current working directory")
	}
	if privateKeyPath == "" {
		privateKeyPath = filepath.Join(wd, "private-key.pem")
	} else {
		privateKeyPath = filepath.Clean(strings.TrimSpace(privateKeyPath))
	}

	if err = os.MkdirAll(filepath.Dir(privateKeyPath), 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory for private key at %s", filepath.Dir(privateKeyPath))
	}

	if publicKeyPath == "" {
		publicKeyPath = filepath.Join(wd, "issuer-pub.jwks")
	} else {
		publicKeyPath = filepath.Clean(strings.TrimSpace(publicKeyPath))
	}

	if err = os.MkdirAll(filepath.Dir(publicKeyPath), 0755); err != nil {
		return errors.Wrapf(err, "failed to create directory for public key at %s", filepath.Dir(publicKeyPath))
	}

	// Check if public key file exists; if so, fail
	_, err = os.Stat(publicKeyPath)
	if err == nil {
		return fmt.Errorf("file exists for public key under %s", publicKeyPath)
	}

	// Check if private key file exists
	privKeyExists := false
	_, err = os.Stat(privateKeyPath)
	if err == nil {
		privKeyExists = true
		log.Warnf("Private key file already exists at %s. Using existing key to generate public key.", privateKeyPath)
	} else if !os.IsNotExist(err) {
		return errors.Wrapf(err, "error checking private key at %s", privateKeyPath)
	}

	if !privKeyExists {
		if err := config.GeneratePrivateKey(privateKeyPath, elliptic.P256(), false); err != nil {
			return errors.Wrapf(err, "failed to generate new private key at %s", privateKeyPath)
		}
	}

	privKey, err := config.LoadSinglePEM(privateKeyPath)
	if err != nil {
		return errors.Wrapf(err, "failed to load private key from %s", privateKeyPath)
	}

	pubJWKS, err := createJWKS(privKey)
	if err != nil {
		return err
	}

	bytes, err := json.MarshalIndent(pubJWKS, "", "	")
	if err != nil {
		return errors.Wrap(err, "failed to generate json from jwks")
	}
	if err = os.WriteFile(publicKeyPath, bytes, 0644); err != nil {
		return errors.Wrap(err, "fail to write the public key to the file")
	}
	fmt.Printf("Successfully generated keys at: \nPrivate key: %s\nPublic Key: %s\n", privateKeyPath, publicKeyPath)
	return nil
}
