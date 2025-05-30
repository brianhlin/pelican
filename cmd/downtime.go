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

package main

import (
	"github.com/spf13/cobra"
)

var (
	downtimeCmd = &cobra.Command{
		Use:   "downtime",
		Short: "Manage server's own downtime periods",
		Long: `Provide commands to list, create, update, and delete scheduled downtime periods
for Pelican servers (Origins/Caches). These commands interact with the server's
administrative API endpoint.`,
	}

	serverURLStr  string
	tokenLocation string
)

func init() {
	// Add the server URL as a REQUIRED persistent flag to all subcommands of downtimeCmd
	downtimeCmd.PersistentFlags().StringVarP(&serverURLStr, "server", "s", "", "Web URL of the Pelican server (e.g. https://my-origin.com:8447)")

	// Optional persistent flag
	downtimeCmd.PersistentFlags().StringVarP(&tokenLocation, "token", "t", "", "Path to the admin token file")
}
