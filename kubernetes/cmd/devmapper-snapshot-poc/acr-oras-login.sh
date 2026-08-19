#!/bin/sh
# Copyright 2026 Alibaba Group Holding Ltd.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: acr-oras-login <registry.azurecr.io>" >&2
  exit 2
fi
: "${AZURE_CLIENT_ID:?AZURE_CLIENT_ID is required}"
: "${AZURE_TENANT_ID:?AZURE_TENANT_ID is required}"
: "${AZURE_FEDERATED_TOKEN_FILE:?AZURE_FEDERATED_TOKEN_FILE is required}"
registry=$1
assertion=$(cat "$AZURE_FEDERATED_TOKEN_FILE")
access_token=$(curl --fail --silent --show-error \
  --request POST \
  --data-urlencode "client_id=$AZURE_CLIENT_ID" \
  --data-urlencode 'scope=https://management.azure.com//.default' \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode 'client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer' \
  --data-urlencode "client_assertion=$assertion" \
  "https://login.microsoftonline.com/$AZURE_TENANT_ID/oauth2/v2.0/token" | jq -er .access_token)
refresh_token=$(curl --fail --silent --show-error \
  --request POST \
  --data-urlencode 'grant_type=access_token' \
  --data-urlencode "service=$registry" \
  --data-urlencode "tenant=$AZURE_TENANT_ID" \
  --data-urlencode "access_token=$access_token" \
  "https://$registry/oauth2/exchange" | jq -er .refresh_token)
printf '%s' "$refresh_token" | oras login "$registry" \
  --username 00000000-0000-0000-0000-000000000000 \
  --password-stdin
