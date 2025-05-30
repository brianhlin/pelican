/***************************************************************
 *
 * Copyright (C) 2025, Pelican Project, Morgridge Institute for Research
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

// Provide auto-generated information about the pelican version and build
package version

// This block of variables will be overwritten at build time
var (
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
	// Pelican version
	version = "dev"
)

// Returns the version of the current binary
func GetVersion() string {
	return version
}

// Overrides the version of the current binary
//
// Intended mainly for use in unit tests
func SetVersion(newVersion string) {
	version = newVersion
}

func GetBuiltCommit() string {
	return commit
}

func SetBuiltCommit(newCommit string) {
	commit = newCommit
}

func GetBuiltDate() string {
	return date
}

func SetBuiltDate(builtDate string) {
	date = builtDate
}

func GetBuiltBy() string {
	return builtBy
}

func SetBuiltBy(newBuiltBy string) {
	builtBy = newBuiltBy
}
