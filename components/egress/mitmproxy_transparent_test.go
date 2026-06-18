// Copyright 2026 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCSVHostsNormalizesAndDeduplicates(t *testing.T) {
	require.Equal(t,
		[]string{"dev.azure.com", "example.com"},
		csvHosts(" dev.azure.com.,DEV.AZURE.COM, example.com "),
	)
}

func TestSafeCertName(t *testing.T) {
	require.Equal(t, "dev.azure.com", safeCertName("Dev.Azure.Com."))
	require.Equal(t, "api_example.com", safeCertName("api:example.com"))
}
